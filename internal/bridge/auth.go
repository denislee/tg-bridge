package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// recoverablePhoneErrs are Telegram RPC errors from SendCode/SignIn that mean
// the user-supplied phone number was wrong or unusable — the daemon should
// loop back and ask for a different phone instead of exiting.
var recoverablePhoneErrs = []string{
	"PHONE_NUMBER_INVALID",
	"PHONE_NUMBER_BANNED",
	"PHONE_NUMBER_FLOOD",
	"PHONE_NUMBER_OCCUPIED",
	"PHONE_NUMBER_UNOCCUPIED",
	"PHONE_PASSWORD_FLOOD",
	"PHONE_CODE_EXPIRED",
	"PHONE_CODE_EMPTY",
	// AUTH_RESTART: server abandoned a prior partial auth; retrying from
	// SendCode (i.e. asking the user to re-submit the phone) clears it.
	"AUTH_RESTART",
}

// AuthState reports what the authenticator is waiting for.
type AuthState string

const (
	AuthIdle       AuthState = "idle"
	AuthNeedPhone  AuthState = "need_phone"
	AuthNeedCode   AuthState = "need_code"
	AuthNeedPass   AuthState = "need_password"
	AuthAuthorized AuthState = "authorized"
	AuthError      AuthState = "error"
)

// httpAuthenticator implements auth.UserAuthenticator. Each prompt method
// publishes a state and blocks until the HTTP API feeds a value.
type httpAuthenticator struct {
	mu        sync.Mutex
	state     AuthState
	lastErr   error
	phoneCh   chan string
	codeCh    chan string
	passwdCh  chan string
}

func newHTTPAuthenticator() *httpAuthenticator {
	return &httpAuthenticator{
		state:    AuthIdle,
		phoneCh:  make(chan string, 1),
		codeCh:   make(chan string, 1),
		passwdCh: make(chan string, 1),
	}
}

func (a *httpAuthenticator) setState(s AuthState, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = s
	a.lastErr = err
}

func (a *httpAuthenticator) Status() (AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state, a.lastErr
}

// Submit* are called by HTTP handlers. Returns false if no prompt is waiting.
func (a *httpAuthenticator) SubmitPhone(p string) bool    { return trySend(a.phoneCh, p) }
func (a *httpAuthenticator) SubmitCode(c string) bool     { return trySend(a.codeCh, c) }
func (a *httpAuthenticator) SubmitPassword(p string) bool { return trySend(a.passwdCh, p) }

func trySend(ch chan string, v string) bool {
	select {
	case ch <- v:
		return true
	default:
		return false
	}
}

// --- auth.UserAuthenticator implementation ---

func (a *httpAuthenticator) Phone(ctx context.Context) (string, error) {
	a.setState(AuthNeedPhone, nil)
	select {
	case p := <-a.phoneCh:
		return p, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *httpAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	a.setState(AuthNeedCode, nil)
	select {
	case c := <-a.codeCh:
		return c, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *httpAuthenticator) Password(ctx context.Context) (string, error) {
	a.setState(AuthNeedPass, nil)
	select {
	case p := <-a.passwdCh:
		return p, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *httpAuthenticator) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return nil
}

func (a *httpAuthenticator) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("signup not supported; register the account on an official client first")
}

// runAuth replicates auth.Flow.Run but loops on user-recoverable errors at
// each stage (bad phone, bad/expired code, wrong 2FA) instead of returning,
// so a typo doesn't crash the daemon — the user can just re-POST.
func (b *Bridge) runAuth(ctx context.Context) error {
	ac := b.client.Auth()

	status, err := ac.Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if status.Authorized {
		return nil
	}

	for {
		phone, err := b.auth.Phone(ctx)
		if err != nil {
			return fmt.Errorf("get phone: %w", err)
		}

		sentCode, err := ac.SendCode(ctx, phone, auth.SendCodeOptions{})
		if err != nil {
			if tgerr.Is(err, recoverablePhoneErrs...) {
				b.auth.setState(AuthNeedPhone, err)
				b.log.Warn("send code rejected, awaiting new phone", "err", err)
				continue
			}
			return fmt.Errorf("send code: %w", err)
		}
		sc, ok := sentCode.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("unexpected sent code type %T", sentCode)
		}

		signInErr := b.signInWithCodeRetry(ctx, ac, phone, sc)
		if signInErr == nil {
			return nil
		}
		if errors.Is(signInErr, auth.ErrPasswordAuthNeeded) {
			return b.passwordLoop(ctx, ac)
		}
		if tgerr.Is(signInErr, recoverablePhoneErrs...) {
			b.auth.setState(AuthNeedPhone, signInErr)
			b.log.Warn("sign in rejected, awaiting new phone", "err", signInErr)
			continue
		}
		return fmt.Errorf("sign in: %w", signInErr)
	}
}

// signInWithCodeRetry calls SignIn, looping on PHONE_CODE_INVALID so a code
// typo only re-prompts the code (same phone, same code hash).
func (b *Bridge) signInWithCodeRetry(ctx context.Context, ac *auth.Client, phone string, sc *tg.AuthSentCode) error {
	for {
		code, err := b.auth.Code(ctx, sc)
		if err != nil {
			return fmt.Errorf("get code: %w", err)
		}
		_, err = ac.SignIn(ctx, phone, code, sc.PhoneCodeHash)
		if err == nil {
			return nil
		}
		if tgerr.Is(err, "PHONE_CODE_INVALID") {
			b.auth.setState(AuthNeedCode, err)
			b.log.Warn("invalid code, awaiting retry")
			continue
		}
		return err
	}
}

func (b *Bridge) passwordLoop(ctx context.Context, ac *auth.Client) error {
	for {
		password, err := b.auth.Password(ctx)
		if err != nil {
			return fmt.Errorf("get password: %w", err)
		}
		_, err = ac.Password(ctx, password)
		if err == nil {
			return nil
		}
		if errors.Is(err, auth.ErrPasswordInvalid) {
			b.auth.setState(AuthNeedPass, err)
			b.log.Warn("invalid 2FA password, awaiting retry")
			continue
		}
		return fmt.Errorf("sign in with password: %w", err)
	}
}

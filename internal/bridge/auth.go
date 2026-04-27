package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

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

// runAuth replicates auth.Flow.Run but loops on an invalid password instead of
// returning, so a wrong 2FA entry doesn't crash the daemon — the user can just
// POST /v1/auth/password again.
func (b *Bridge) runAuth(ctx context.Context) error {
	ac := b.client.Auth()

	status, err := ac.Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if status.Authorized {
		return nil
	}

	phone, err := b.auth.Phone(ctx)
	if err != nil {
		return fmt.Errorf("get phone: %w", err)
	}

	sentCode, err := ac.SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return fmt.Errorf("send code: %w", err)
	}
	sc, ok := sentCode.(*tg.AuthSentCode)
	if !ok {
		return fmt.Errorf("unexpected sent code type %T", sentCode)
	}

	code, err := b.auth.Code(ctx, sc)
	if err != nil {
		return fmt.Errorf("get code: %w", err)
	}

	_, signInErr := ac.SignIn(ctx, phone, code, sc.PhoneCodeHash)
	if signInErr == nil {
		return nil
	}
	if !errors.Is(signInErr, auth.ErrPasswordAuthNeeded) {
		return fmt.Errorf("sign in: %w", signInErr)
	}

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

package bridge

import (
	"context"
	"errors"
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

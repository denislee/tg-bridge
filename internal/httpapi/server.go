package httpapi

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"time"

	"tg-bridge/internal/bridge"
	"tg-bridge/internal/config"
)

// Server wraps http.Server with the bridge and config.
type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	br     *bridge.Bridge
	http   *http.Server
}

func New(cfg *config.Config, log *slog.Logger, br *bridge.Bridge, cert tls.Certificate) *Server {
	mux := http.NewServeMux()
	s := &Server{cfg: cfg, log: log, br: br}
	s.registerRoutes(mux)

	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.requestLog(mux),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Auth flow (no bearer required — the token is what auth establishes).
	mux.HandleFunc("GET /v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /v1/auth/phone", s.handleAuthPhone)
	mux.HandleFunc("POST /v1/auth/code", s.handleAuthCode)
	mux.HandleFunc("POST /v1/auth/password", s.handleAuthPassword)

	// Bearer-protected endpoints.
	mux.Handle("GET /v1/me", s.withAuth(s.handleMe))
	mux.Handle("GET /v1/chats", s.withAuth(s.handleListChats))
	mux.Handle("GET /v1/chats/{id}/messages", s.withAuth(s.handleGetMessages))
	mux.Handle("POST /v1/chats/{id}/messages", s.withAuth(s.handleSendMessage))
	mux.Handle("POST /v1/chats/{id}/read", s.withAuth(s.handleMarkRead))
	mux.Handle("GET /v1/media/{id}", s.withAuth(s.handleGetMedia))
	mux.Handle("GET /v1/events", s.withAuth(s.handleEvents))
}

// ListenAndServe starts the TLS server and blocks.
func (s *Server) ListenAndServe() error {
	s.log.Info("http listening", "addr", s.cfg.Listen)
	return s.http.ListenAndServeTLS("", "")
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

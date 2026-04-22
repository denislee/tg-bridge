package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"tg-bridge/internal/config"
)

type ctxKey string

const clientCtxKey ctxKey = "client"

// withAuth extracts the bearer token, matches it to a configured client,
// and injects the client config into the request context.
func (s *Server) withAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		client := s.cfg.TokenToClient(token)
		if client == nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), clientCtxKey, client)
		h(w, r.WithContext(ctx))
	})
}

func clientFromCtx(ctx context.Context) *config.ClientConfig {
	v, _ := ctx.Value(clientCtxKey).(*config.ClientConfig)
	return v
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	// Fallback: ?token=... (useful for WS clients that can't set headers)
	return r.URL.Query().Get("token")
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.code,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// Hijack is needed for WebSocket upgrades.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

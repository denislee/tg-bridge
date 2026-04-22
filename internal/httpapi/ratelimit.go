package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a tiny per-IP token bucket. It's intended to raise the cost
// of brute-forcing auth endpoints, not to be a full DoS shield.
type rateLimiter struct {
	rate  float64 // tokens added per second
	burst float64 // max tokens

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
	}
}

func (r *rateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[ip]
	if !ok {
		// Simple bound on map growth. If this ever matters we can switch
		// to an LRU; for a single-user bridge it won't.
		if len(r.buckets) > 10000 {
			for k := range r.buckets {
				delete(r.buckets, k)
				break
			}
		}
		b = &bucket{tokens: r.burst, last: now}
		r.buckets[ip] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * r.rate
	if b.tokens > r.burst {
		b.tokens = r.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP returns the remote IP, stripping the port. Trust-the-socket only;
// no X-Forwarded-For handling by design (the bridge is a TLS terminator).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) withRateLimit(rl *rateLimiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		h(w, r)
	}
}

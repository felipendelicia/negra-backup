// internal/api/ratelimit.go
package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a fixed-window per-IP limiter. The login endpoint had no throttling
// at all, leaving ADMIN_PASSWORD open to unlimited offline-speed guessing over HTTP.
type rateLimiter struct {
	mu       sync.Mutex
	hits     map[string]*window
	limit    int
	interval time.Duration
}

type window struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, interval time.Duration) *rateLimiter {
	rl := &rateLimiter{
		hits:     make(map[string]*window),
		limit:    limit,
		interval: interval,
	}
	go rl.reapLoop()
	return rl
}

// reapLoop drops expired windows so the map cannot grow without bound from an
// attacker cycling source addresses.
func (rl *rateLimiter) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		rl.mu.Lock()
		for k, w := range rl.hits {
			if now.After(w.reset) {
				delete(rl.hits, k)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	w, ok := rl.hits[key]
	if !ok || now.After(w.reset) {
		rl.hits[key] = &window{count: 1, reset: now.Add(rl.interval)}
		return true
	}
	if w.count >= rl.limit {
		return false
	}
	w.count++
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// middleware throttles by client IP. It deliberately ignores X-Forwarded-For:
// trusting a client-supplied header would let an attacker bypass the limit by
// rotating its value. Behind a proxy, set the real IP upstream.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			respondError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

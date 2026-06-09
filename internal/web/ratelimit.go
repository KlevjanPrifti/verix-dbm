package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a tiny in-process fixed-window limiter keyed by client IP. It's
// sized for an internal admin tool (few clients); state is bounded by periodic
// pruning of expired windows. It guards the auth endpoints against brute-force
// and redirect spam — not a substitute for an edge WAF on a hostile-internet
// deployment, but a sensible floor.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string]*window
	max    int
	period time.Duration
}

type window struct {
	count int
	reset time.Time
}

func newRateLimiter(max int, period time.Duration) *rateLimiter {
	rl := &rateLimiter{hits: map[string]*window{}, max: max, period: period}
	go rl.prune()
	return rl
}

func (rl *rateLimiter) prune() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		rl.mu.Lock()
		now := time.Now()
		for k, w := range rl.hits {
			if now.After(w.reset) {
				delete(rl.hits, k)
			}
		}
		rl.mu.Unlock()
	}
}

// allow reports whether the key may proceed, recording the attempt.
func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	w, ok := rl.hits[key]
	if !ok || now.After(w.reset) {
		rl.hits[key] = &window{count: 1, reset: now.Add(rl.period)}
		return true
	}
	if w.count >= rl.max {
		return false
	}
	w.count++
	return true
}

// middleware rejects requests from a client IP that exceeds the window with 429.
func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return rl.middlewareBy(clientIP)(next)
}

// middlewareBy is middleware keyed by an arbitrary function (e.g. the session
// user instead of the IP, so users behind a shared NAT aren't lumped together).
func (rl *rateLimiter) middlewareBy(key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.allow(key(r)) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return ip
}

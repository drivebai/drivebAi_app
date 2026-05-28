package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// RateLimiter implements a per-IP sliding-window counter.
// A background goroutine periodically evicts expired entries so the
// visitors map does not grow without bound.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*rlEntry
	rate     int           // max requests per window
	window   time.Duration // window duration
	stop     chan struct{}  // signals the cleanup goroutine to exit
}

type rlEntry struct {
	count     int
	windowEnd time.Time
}

// NewRateLimiter creates a RateLimiter and starts its background cleanup goroutine.
func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*rlEntry),
		rate:     rate,
		window:   window,
		stop:     make(chan struct{}),
	}
	go rl.cleanup() // background worker: evicts stale entries
	return rl
}

// cleanup runs until Stop is called, evicting entries whose window has expired.
// This demonstrates goroutine + channel + ticker concurrency patterns.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, e := range rl.visitors {
				if now.After(e.windowEnd) {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()

		case <-rl.stop:
			return
		}
	}
}

// Stop signals the background goroutine to exit (call during graceful shutdown).
func (rl *RateLimiter) Stop() {
	close(rl.stop)
}

// Allow returns true when the IP is within its rate limit, false when throttled.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	e, exists := rl.visitors[ip]

	if !exists || now.After(e.windowEnd) {
		// First request in this window, or window has rolled over.
		rl.visitors[ip] = &rlEntry{count: 1, windowEnd: now.Add(rl.window)}
		return true
	}

	if e.count >= rl.rate {
		return false
	}
	e.count++
	return true
}

// RateLimit returns a chi-compatible middleware that applies the given RateLimiter.
// Throttled requests receive 429 Too Many Requests with the standard error shape.
func RateLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !rl.Allow(ip) {
				httputil.WriteError(w, http.StatusTooManyRequests, models.ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the real client IP, honouring X-Forwarded-For when present.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain a comma-separated list; take the first.
		parts := strings.SplitN(xff, ",", 2)
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		addr = addr[:i]
	}
	return addr
}

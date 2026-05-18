package middleware

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
	rw.wroteHeader = true
}

// Hijack delegates to the underlying ResponseWriter so that WebSocket
// upgrades (gorilla/websocket) can take over the raw TCP connection.
// Without this, w.(http.Hijacker) returns false and upgrader.Upgrade()
// fails with "response does not implement http.Hijacker".
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter %T does not implement http.Hijacker", rw.ResponseWriter)
	}
	return h.Hijack()
}

// Logger creates a logging middleware
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			wrapped := wrapResponseWriter(w)
			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.status,
				"duration_ms", duration.Milliseconds(),
				"ip", r.RemoteAddr,
			)
		})
	}
}

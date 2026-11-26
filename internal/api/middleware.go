package api

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/doketfire/doketfire/internal/auth"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 || !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func validRequestID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		duration := time.Since(start)
		s.recordRequestLog(r, rec.status, rec.bytes, duration)
		s.logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", duration),
			slog.String("ip", clientIP(r, s.cfg.TrustProxy)),
			slog.String("request_id", w.Header().Get("X-Request-ID")),
		)
	})
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.logger.Error("panic serving request",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// gzipMiddleware compresses JSON responses. SSE streams, files and redirects
// are passed through untouched so flushing and Content-Length stay correct.
func (s *Server) gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token := strings.TrimSpace(part)
		if i := strings.IndexByte(token, ';'); i >= 0 {
			token = strings.TrimSpace(token[:i])
		}
		if strings.EqualFold(token, "gzip") {
			return true
		}
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer   *gzip.Writer
	started  bool
	compress bool
}

// gzipPool reuses compressor buffers; flate allocates ~1 MiB per writer.
var gzipPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.started {
		return
	}
	g.started = true
	contentType := g.Header().Get("Content-Type")
	g.compress = status != http.StatusNoContent && status != http.StatusNotModified &&
		strings.HasPrefix(contentType, "application/json")
	if g.compress {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length")
		g.writer = gzipPool.Get().(*gzip.Writer)
		g.writer.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.started {
		g.WriteHeader(http.StatusOK)
	}
	if !g.compress {
		return g.ResponseWriter.Write(p)
	}
	return g.writer.Write(p)
}

func (g *gzipResponseWriter) Flush() {
	if g.writer != nil {
		_ = g.writer.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) Close() error {
	if g.writer == nil {
		return nil
	}
	err := g.writer.Close()
	gzipPool.Put(g.writer)
	g.writer = nil
	return err
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/_/") {
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Settings are read on every request: PATCH /api/admin/settings must
		// take effect without a restart or a data race with this middleware.
		origins := s.settings.Get().CORSOrigins
		allowed := map[string]bool{}
		wildcard := false
		for _, o := range origins {
			o = strings.TrimSpace(o)
			if o == "*" {
				wildcard = true
			} else if o != "" {
				allowed[o] = true
			}
		}
		origin := r.Header.Get("Origin")
		if origin != "" && (wildcard || allowed[origin]) {
			if wildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Last-Event-ID, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			if origin != "" && !wildcard && !allowed[origin] {
				writeError(w, http.StatusForbidden, "cors_denied", "origin is not allowed", nil)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if h := r.Header.Get("Authorization"); h != "" {
			parts := strings.SplitN(h, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid Authorization header", nil)
				return
			}
			token = strings.TrimSpace(parts[1])
		} else if strings.HasPrefix(r.URL.Path, "/api/files/") {
			token = r.URL.Query().Get("token")
		}
		if token != "" {
			id, err := s.auth.Identify(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token", nil)
				return
			}
			r = withIdentity(r, id)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := identityFrom(r)
	if id == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
		return nil
	}
	return id
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := s.requireUser(w, r)
	if id == nil {
		return nil
	}
	if !id.IsAdmin() {
		writeError(w, http.StatusForbidden, "forbidden", "superuser access required", nil)
		return nil
	}
	return id
}

// ---- rate limiting ----

type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	buckets map[string]*rateBucket
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(capacity int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		window:  window,
		buckets: map[string]*rateBucket{},
	}
}

// allow consumes one token using the capacity supplied by the caller, so a
// settings change applies immediately without mutating shared limiter state.
func (l *rateLimiter) allow(key string, now time.Time, capacity int) bool {
	if capacity < 1 {
		capacity = 1
	}
	limit := float64(capacity)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 10000 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.window*2 {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &rateBucket{tokens: limit, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last)
	if elapsed > 0 {
		b.tokens += elapsed.Seconds() * (limit / l.window.Seconds())
		if b.tokens > limit {
			b.tokens = limit
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sensitiveAuthPath(r) {
			key := clientIP(r, s.cfg.TrustProxy)
			if !s.limiter.allow(key, time.Now(), s.settings.Get().AuthRateLimit) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sensitiveAuthPath(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p := r.URL.Path
	return strings.HasSuffix(p, "/auth-with-password") ||
		strings.HasSuffix(p, "/auth-register") ||
		p == "/api/admin/setup"
}

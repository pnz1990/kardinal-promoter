// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package accesslog writes a structured access log for the controller's HTTP
// APIs (the UI API and the Bundle API): who authenticated, which requests
// were refused and why, and which requests changed something, optionally
// every request and the client's address. It never logs credentials:
// request headers and bodies are not read, only the response status and,
// for refusals, the start of the response body (kardinal's own message).
package accesslog

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Config selects what is logged.
type Config struct {
	// AllRequests logs every request, not only logins, refusals and writes.
	AllRequests bool
	// SourceIP adds the client address (source_ip).
	SourceIP bool
	// TrustedProxies are the proxies whose X-Forwarded-For is believed when
	// SourceIP is set. Others' X-Forwarded-For is ignored.
	TrustedProxies []*net.IPNet
	// PerSecond bounds the lines written per second (0: DefaultPerSecond).
	// Lines past it are counted and reported in one summary line.
	PerSecond int
}

// DefaultPerSecond is the default bound on access log lines per second.
const DefaultPerSecond = 50

// maxReason is how much of a refusal's response body is logged.
const maxReason = 256

// Logger writes access log lines.
type Logger struct {
	cfg Config
	log zerolog.Logger
	now func() time.Time

	mu          sync.Mutex
	windowStart time.Time
	written     int
	dropped     int
}

// New returns a Logger writing to log.
func New(cfg Config, log zerolog.Logger) *Logger {
	if cfg.PerSecond <= 0 {
		cfg.PerSecond = DefaultPerSecond
	}
	return &Logger{cfg: cfg, log: log, now: time.Now}
}

// Entry is what the handlers learn about a request while serving it: the
// authentication middleware fills it in through FromContext.
type Entry struct {
	// User and Groups are the authenticated caller.
	User   string
	Groups []string
	// Auth is how the caller authenticated: "tokenreview", "static-token",
	// or "" (no authentication, or none succeeded).
	Auth string
	// Login is set when the request's credentials were checked with the API
	// server (a TokenReview, not a cached result): a login.
	Login bool
}

type entryKey struct{}

// FromContext returns the request's Entry, or a throwaway one when the
// request is not logged, so callers never check for nil.
func FromContext(ctx context.Context) *Entry {
	if e, ok := ctx.Value(entryKey{}).(*Entry); ok {
		return e
	}
	return &Entry{}
}

// WithEntry returns ctx carrying e (for tests and for middleware that runs
// outside Middleware).
func WithEntry(ctx context.Context, e *Entry) context.Context {
	return context.WithValue(ctx, entryKey{}, e)
}

// Middleware logs the requests of server ("ui", "bundle-api") that next
// serves.
func (l *Logger) Middleware(server string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := l.now()
		e := &Entry{}
		rw := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r.WithContext(WithEntry(r.Context(), e)))
		kind := l.kind(r, rw.status, e)
		if kind == "" {
			return
		}
		if !l.allow() {
			return
		}
		ev := l.log.Info()
		if rw.status >= 400 {
			ev = l.log.Warn()
		}
		ev = ev.Str("access", kind).Str("server", server).Str("method", r.Method).Str("path", r.URL.Path).
			Int("status", rw.status).Int64("durationMs", l.now().Sub(start).Milliseconds())
		if e.User != "" {
			ev = ev.Str("user", e.User)
			if len(e.Groups) > 0 {
				ev = ev.Strs("groups", e.Groups)
			}
		}
		if e.Auth != "" {
			ev = ev.Str("auth", e.Auth)
		}
		if rw.status == http.StatusUnauthorized || rw.status == http.StatusForbidden || rw.status == http.StatusTooManyRequests {
			ev = ev.Str("reason", strings.TrimSpace(rw.reason.String()))
		}
		if l.cfg.SourceIP {
			ev = ev.Str("sourceIP", l.sourceIP(r))
		}
		ev.Msg("api access")
	})
}

// kind is why a request is logged, or "" when it is not.
func (l *Logger) kind(r *http.Request, status int, e *Entry) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
		return "denied"
	case isWrite(r.Method):
		return "write"
	case e.Login:
		return "login"
	case l.cfg.AllRequests:
		return "request"
	}
	return ""
}

func isWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// allow applies the per-second bound. When a second ends with lines
// dropped, one summary line reports how many.
func (l *Logger) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.windowStart) >= time.Second {
		if l.dropped > 0 {
			l.log.Warn().Int("dropped", l.dropped).Int("perSecond", l.cfg.PerSecond).
				Msg("api access log: lines dropped over the rate limit")
		}
		l.windowStart, l.written, l.dropped = now, 0, 0
	}
	if l.written >= l.cfg.PerSecond {
		l.dropped++
		return false
	}
	l.written++
	return true
}

// sourceIP is the client address: the peer, or, when the peer is a trusted
// proxy, the nearest X-Forwarded-For address that is not one.
func (l *Logger) sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !l.trusted(host) {
		return host
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		if net.ParseIP(h) == nil {
			return host // not an address: do not trust the header
		}
		if !l.trusted(h) {
			return h
		}
		host = h
	}
	return host
}

func (l *Logger) trusted(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range l.cfg.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseCIDRs parses comma-separated CIDRs (or single addresses).
func ParseCIDRs(list []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if ip := net.ParseIP(s); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// recorder captures the status and the start of a refusal's body.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	reason      strings.Builder
}

func (r *recorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = http.StatusOK, true
	}
	if r.status >= 400 && r.reason.Len() < maxReason {
		n := min(len(b), maxReason-r.reason.Len())
		r.reason.Write(b[:n])
	}
	return r.ResponseWriter.Write(b)
}

// Flush passes through, for streaming handlers.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

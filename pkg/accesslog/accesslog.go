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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// droppedTotal counts access log lines not written, by kind.
var droppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "kardinal_api_access_log_dropped_total",
	Help: "API access log lines not written because their kind was over its per-second budget (denied, request). Logins and writes are never dropped.",
}, []string{"kind"})

func init() { ctrlmetrics.Registry.MustRegister(droppedTotal) }

// Config selects what is logged.
type Config struct {
	// AllRequests logs every request, not only logins, refusals and writes.
	AllRequests bool
	// SourceIP adds the client address (source_ip).
	SourceIP bool
	// TrustedProxies are the proxies whose X-Forwarded-For is believed when
	// SourceIP is set. Others' X-Forwarded-For is ignored.
	TrustedProxies []*net.IPNet
	// PerSecond bounds the refusal lines (denied) and, separately, the
	// all-requests lines (request) written per second (0: DefaultPerSecond).
	// Logins and writes are never dropped. Dropped lines are counted
	// (kardinal_api_access_log_dropped_total) and reported by kind every
	// ReportEvery.
	PerSecond int
	// ReportEvery is how often dropped-line counts are logged (0:
	// DefaultReportEvery).
	ReportEvery time.Duration
}

// DefaultPerSecond is the default per-kind bound on denied and request lines.
const DefaultPerSecond = 50

// DefaultReportEvery is how often dropped-line counts are logged by default.
const DefaultReportEvery = 10 * time.Second

// staticLoginEvery: a static-token request is logged as a login when the
// token was not seen on that server for this long (like the TokenReview
// cache, so a polling UI is one login, not one per poll).
const staticLoginEvery = 30 * time.Second

// maxReason is how much of a refusal's response body is logged, and
// maxPath how much of the request path.
const (
	maxReason = 256
	maxPath   = 256
)

// Logger writes access log lines.
type Logger struct {
	cfg Config
	log zerolog.Logger
	now func() time.Time

	mu          sync.Mutex
	windowStart time.Time
	written     map[string]int
	dropped     map[string]int
	lastStatic  map[string]time.Time
}

// New returns a Logger writing to log.
func New(cfg Config, log zerolog.Logger) *Logger {
	if cfg.PerSecond <= 0 {
		cfg.PerSecond = DefaultPerSecond
	}
	if cfg.ReportEvery <= 0 {
		cfg.ReportEvery = DefaultReportEvery
	}
	return &Logger{cfg: cfg, log: log, now: time.Now,
		written: map[string]int{}, dropped: map[string]int{}, lastStatic: map[string]time.Time{}}
}

// Start reports dropped-line counts every ReportEvery until ctx ends (a
// manager Runnable in the controller).
func (l *Logger) Start(ctx context.Context) error {
	t := time.NewTicker(l.cfg.ReportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			l.report()
			return nil
		case <-t.C:
			l.report()
		}
	}
}

// report logs and resets the dropped counts.
func (l *Logger) report() {
	l.mu.Lock()
	dropped := l.dropped
	l.dropped = map[string]int{}
	l.mu.Unlock()
	if len(dropped) == 0 {
		return
	}
	ev := l.log.Warn().Int("perSecond", l.cfg.PerSecond)
	for k, n := range dropped {
		ev = ev.Int("dropped_"+k, n)
	}
	ev.Msg("api access log: lines dropped over the per-second budget")
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
		if e.Auth == "static-token" && l.staticLogin(server) {
			e.Login = true
		}
		kind := l.kind(r, rw.status, e)
		if kind == "" || !l.allow(kind) {
			return
		}
		ev := l.log.Info()
		if rw.status >= 400 {
			ev = l.log.Warn()
		}
		ev = ev.Str("access", kind).Str("server", server).Str("method", r.Method).Str("path", clip(r.URL.Path, maxPath)).
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
		if refused(rw.status) {
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
	case refused(status):
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

// refused is a status the access log records as a refusal: 401, 403, 429,
// and 503 (authentication unavailable: the review API failed, fail-closed).
func refused(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	return false
}

// allow applies the per-second budget of kind. Logins and writes always
// pass; denied and request lines each have PerSecond a second.
func (l *Logger) allow(kind string) bool {
	if kind == "login" || kind == "write" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.windowStart) >= time.Second {
		l.windowStart, l.written = now, map[string]int{}
	}
	if l.written[kind] >= l.cfg.PerSecond {
		l.dropped[kind]++
		droppedTotal.WithLabelValues(kind).Inc()
		return false
	}
	l.written[kind]++
	return true
}

// staticLogin reports whether a static-token request on server is a login:
// the first in staticLoginEvery.
func (l *Logger) staticLogin(server string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.lastStatic[server]; ok && now.Sub(last) < staticLoginEvery {
		return false
	}
	l.lastStatic[server] = now
	return true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
	// Every X-Forwarded-For header line, in order: a proxy may append a line
	// instead of extending the first one.
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
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

// NeedLeaderElection is false: every replica serves the APIs and logs.
func (l *Logger) NeedLeaderElection() bool { return false }

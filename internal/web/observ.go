package web

// Observability: structured request logging, Prometheus metrics, a readiness
// probe, and audit-event mirroring. Kept together so the operational surface an
// SRE team consumes lives in one file.

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/store"
)

// metrics holds the Prometheus registry and the app-level collectors. The Go
// runtime and process collectors are registered too, so memory, goroutines, GC,
// file descriptors, and CPU are exported for free.
type metrics struct {
	reg       *prometheus.Registry
	handler   http.Handler
	reqTotal  *prometheus.CounterVec   // method, route, status
	reqDur    *prometheus.HistogramVec // method, route
	authTotal *prometheus.CounterVec   // result
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &metrics{
		reg: reg,
		reqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "verixdbm", Subsystem: "http", Name: "requests_total",
			Help: "Total HTTP requests by method, matched route, and status code.",
		}, []string{"method", "route", "status"}),
		reqDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "verixdbm", Subsystem: "http", Name: "request_duration_seconds",
			Help: "HTTP request latency by method and matched route.", Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		authTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "verixdbm", Subsystem: "auth", Name: "logins_total",
			Help: "Login outcomes by result (success|failure).",
		}, []string{"result"}),
	}
	reg.MustRegister(m.reqTotal, m.reqDur, m.authTotal)
	m.handler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m
}

// isInfraPath reports whether a path is an operational endpoint that should not
// be logged per-request or counted in the HTTP metrics (they are scraped/probed
// frequently and would drown the signal).
func isInfraPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}

// observe is the combined request-logging + metrics middleware. It wraps the
// response once, then records both a structured log line and the Prometheus
// counters/histogram, labelled by the matched chi route pattern (not the raw
// path) to keep cardinality bounded.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInfraPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		dur := time.Since(start)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "other" // unmatched (404): don't label by raw path
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		s.metrics.reqDur.WithLabelValues(r.Method, route).Observe(dur.Seconds())
		s.metrics.reqTotal.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		attrs := []any{
			"method", r.Method,
			"route", route,
			"path", r.URL.Path,
			"status", status,
			"bytes", ww.BytesWritten(),
			"duration_ms", dur.Milliseconds(),
			"ip", clientIP(r),
			"request_id", middleware.GetReqID(r.Context()),
		}
		if u, ok := auth.FromContext(r.Context()); ok && u.Email != "" {
			attrs = append(attrs, "user", u.Email)
		}
		s.log.Log(r.Context(), level, "http_request", attrs...)
	})
}

// metricsHandler serves the Prometheus exposition. When DBM_METRICS_TOKEN is set
// it requires that token as a Bearer credential; otherwise /metrics is open
// (typical when the port is only reachable on a private network).
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if tok := s.cfg.MetricsToken; tok != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	s.metrics.handler.ServeHTTP(w, r)
}

// readyz is the readiness probe: it reports 200 only when the metadata store is
// reachable. /healthz stays a static liveness check (the process is up); this
// reflects the ability to actually serve. OIDC reachability is validated at
// startup, so it isn't re-probed here.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "store": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ObserveAudit mirrors every audit event to the structured log (so a customer's
// log shipper forwards it to their SIEM) and feeds the auth-outcome metric. It
// is wired to the store via OnAudit, so both auth and handler audit events flow
// through it.
func (s *Server) ObserveAudit(a store.Audit) {
	s.log.Info("audit",
		"action", a.Action,
		"user", a.User,
		"conn_id", a.ConnID,
		"detail", a.Detail,
		"success", a.Success,
	)
	switch a.Action {
	case "auth_login":
		s.metrics.authTotal.WithLabelValues("success").Inc()
	case "auth_login_failed":
		s.metrics.authTotal.WithLabelValues("failure").Inc()
	}
}

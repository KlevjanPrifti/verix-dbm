package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"verix-dbm/internal/config"
	"verix-dbm/internal/store"
)

func TestIsInfraPath(t *testing.T) {
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		if !isInfraPath(p) {
			t.Errorf("%s should be an infra path", p)
		}
	}
	for _, p := range []string{"/api/me", "/", "/metricsx", "/api/audit"} {
		if isInfraPath(p) {
			t.Errorf("%s should not be an infra path", p)
		}
	}
}

func TestObserveAuditFeedsAuthMetric(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: newMetrics()}

	s.ObserveAudit(store.Audit{Action: "auth_login", Success: true})
	s.ObserveAudit(store.Audit{Action: "auth_login_failed", Success: false})
	s.ObserveAudit(store.Audit{Action: "pg_query", Success: true}) // not an auth event

	if v := testutil.ToFloat64(s.metrics.authTotal.WithLabelValues("success")); v != 1 {
		t.Errorf("auth success counter = %v, want 1", v)
	}
	if v := testutil.ToFloat64(s.metrics.authTotal.WithLabelValues("failure")); v != 1 {
		t.Errorf("auth failure counter = %v, want 1", v)
	}
}

func TestMetricsHandlerTokenGate(t *testing.T) {
	s := &Server{cfg: &config.Config{MetricsToken: "secret"}, metrics: newMetrics()}

	// No token -> 401.
	rec := httptest.NewRecorder()
	s.metricsHandler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}

	// Correct bearer -> 200 with exposition.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.metricsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("with token: status = %d, want 200", rec.Code)
	}

	// No token configured -> open.
	open := &Server{cfg: &config.Config{}, metrics: newMetrics()}
	rec = httptest.NewRecorder()
	open.metricsHandler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("open metrics: status = %d, want 200", rec.Code)
	}
}

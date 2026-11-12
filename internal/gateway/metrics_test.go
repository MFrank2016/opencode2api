package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/telemetry"
)

func newMetricsGateway(enabled bool, token string, monitor *telemetry.Metrics) *Gateway {
	cfg := config.Config{}
	cfg.Metrics = config.MetricsConfig{Enabled: enabled, Token: token, Path: "/metrics"}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor:    monitor,
		transports: &proxyTransports{},
	}
}

func TestMetricsRendersExpectedSeries(t *testing.T) {
	monitor := telemetry.Monitor()
	monitor.RecordFreeLimit()
	g := newMetricsGateway(true, "", monitor)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rec := httptest.NewRecorder()
	g.handleMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`opencode2api_requests_total{outcome="success"}`,
		`opencode2api_requests_total{outcome="error"}`,
		`opencode2api_requests_active`,
		`opencode2api_streams_active`,
		`opencode2api_proxies{state="healthy"}`,
		`opencode2api_proxies{state="unhealthy"}`,
		`opencode2api_rotation_cooling_proxies`,
		`opencode2api_free_limit_events_total 1`,
		`opencode2api_tokens_total{type="input"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsRequiresTokenFromNonLoopback(t *testing.T) {
	g := newMetricsGateway(true, "", telemetry.Monitor())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "10.0.0.5:4242"
	rec := httptest.NewRecorder()
	g.handleMetrics(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMetricsAcceptsConfiguredToken(t *testing.T) {
	g := newMetricsGateway(true, "secret", telemetry.Monitor())

	unauthorized := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	unauthorized.RemoteAddr = "10.0.0.5:4242"
	unauthorized.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	g.handleMetrics(rec, unauthorized)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", rec.Code)
	}

	authorized := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	authorized.RemoteAddr = "10.0.0.5:4242"
	authorized.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	g.handleMetrics(rec, authorized)
	if rec.Code != http.StatusOK {
		t.Fatalf("right token status = %d, want 200", rec.Code)
	}
}

func TestMetricsRouteOnlyRegisteredWhenEnabled(t *testing.T) {
	disabled := newMetricsGateway(false, "", telemetry.Monitor())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rec := httptest.NewRecorder()
	disabled.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d, want 404", rec.Code)
	}

	enabled := newMetricsGateway(true, "", telemetry.Monitor())
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	rec = httptest.NewRecorder()
	enabled.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled status = %d, want 200", rec.Code)
	}
}

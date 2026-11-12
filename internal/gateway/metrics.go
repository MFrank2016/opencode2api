package gateway

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"opencode2api/internal/telemetry"
)

// handleMetrics serves the Prometheus text exposition. Access is controlled by
// metrics.token; without a token only a loopback caller is accepted, so an
// operator behind a reverse proxy must configure one before enabling it.
func (g *Gateway) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !g.metricsAuthorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	g.writeMetrics(w)
}

func (g *Gateway) metricsAuthorized(r *http.Request) bool {
	token := strings.TrimSpace(g.cfg.Metrics.Token)
	if token != "" {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// writeMetrics renders the current counters. Labels are deliberately bounded
// (outcome, tier, state, token type): per-model or per-key labels would make
// the series cardinality grow without limit.
func (g *Gateway) writeMetrics(w http.ResponseWriter) {
	var snap telemetry.MonitorSnapshot
	if g.monitor != nil {
		snap = g.monitor.Snapshot()
	}
	fmt.Fprintf(w, "# HELP opencode2api_uptime_seconds Seconds since the process started.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_uptime_seconds gauge\n")
	fmt.Fprintf(w, "opencode2api_uptime_seconds %d\n", snap.UptimeSeconds)

	fmt.Fprintf(w, "# HELP opencode2api_requests_total Requests finished since start, by outcome.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_requests_total counter\n")
	fmt.Fprintf(w, "opencode2api_requests_total{outcome=\"success\"} %d\n", snap.Lifetime.Success)
	fmt.Fprintf(w, "opencode2api_requests_total{outcome=\"error\"} %d\n", snap.Lifetime.Errors)

	fmt.Fprintf(w, "# HELP opencode2api_requests_active Requests currently in flight.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_requests_active gauge\n")
	fmt.Fprintf(w, "opencode2api_requests_active %d\n", snap.Active)

	fmt.Fprintf(w, "# HELP opencode2api_streams_active Streaming responses currently in flight.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_streams_active gauge\n")
	fmt.Fprintf(w, "opencode2api_streams_active %d\n", snap.ActiveStreams)

	fmt.Fprintf(w, "# HELP opencode2api_requests_last_hour_total Requests in the last hour, by tier.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_requests_last_hour_total gauge\n")
	for tier, count := range snap.Tiers {
		fmt.Fprintf(w, "opencode2api_requests_last_hour_total{tier=%q} %d\n", tier, count)
	}

	fmt.Fprintf(w, "# HELP opencode2api_tokens_total Upstream-reported tokens since start.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_tokens_total counter\n")
	tokens := snap.Usage.Lifetime.Tokens
	fmt.Fprintf(w, "opencode2api_tokens_total{type=\"input\"} %d\n", tokens.Input)
	fmt.Fprintf(w, "opencode2api_tokens_total{type=\"output\"} %d\n", tokens.Output)
	fmt.Fprintf(w, "opencode2api_tokens_total{type=\"cached\"} %d\n", tokens.Cached)
	fmt.Fprintf(w, "opencode2api_tokens_total{type=\"reasoning\"} %d\n", tokens.Reasoning)

	healthy, unhealthy := 0, 0
	if g.transports != nil {
		total, ok := g.transports.healthCounts()
		healthy, unhealthy = ok, total-ok
	}
	fmt.Fprintf(w, "# HELP opencode2api_proxies Configured proxy routes by health state.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_proxies gauge\n")
	fmt.Fprintf(w, "opencode2api_proxies{state=\"healthy\"} %d\n", healthy)
	fmt.Fprintf(w, "opencode2api_proxies{state=\"unhealthy\"} %d\n", unhealthy)

	fmt.Fprintf(w, "# HELP opencode2api_rotation_cooling_proxies Exits cooling after a failure or free-limit answer.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_rotation_cooling_proxies gauge\n")
	fmt.Fprintf(w, "opencode2api_rotation_cooling_proxies %d\n", g.coolingProxyCount())

	fmt.Fprintf(w, "# HELP opencode2api_free_limit_events_total Exhausted-allowance answers seen since start.\n")
	fmt.Fprintf(w, "# TYPE opencode2api_free_limit_events_total counter\n")
	fmt.Fprintf(w, "opencode2api_free_limit_events_total %d\n", g.monitor.FreeLimitEvents())
}

// coolingProxyCount counts exits currently held out of the anonymous cursor by
// a failure cool-down or an exhausted allowance.
func (g *Gateway) coolingProxyCount() int {
	if g.transports == nil {
		return 0
	}
	now := time.Now().UnixNano()
	count := 0
	for _, proxy := range g.transports.items {
		if proxy == nil {
			continue
		}
		if proxy.blockedUntil.Load() > now || proxy.freeLimitUntil.Load() > now {
			count++
		}
	}
	return count
}

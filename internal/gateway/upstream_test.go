package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

// stubRoundTripper answers every request with a fixed status so the anonymous
// rotation loop can be exercised without a network.
type stubRoundTripper struct{ status int }

func (s stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    r,
	}, nil
}

func newAnonymousLoopGateway(proxyCount, status int) *Gateway {
	proxies := make([]*proxyTransport, 0, proxyCount)
	for i := 0; i < proxyCount; i++ {
		proxy := &proxyTransport{
			index:  i,
			name:   "test-proxy",
			client: &http.Client{Transport: stubRoundTripper{status: status}},
			bun:    &http.Client{Transport: stubRoundTripper{status: status}},
		}
		proxy.healthy.Store(true)
		proxies = append(proxies, proxy)
	}
	transports := &proxyTransports{items: proxies}
	cfg := config.Config{}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
		anonymous:  anonymousPool(true, transports, 15*time.Second, anonymousBackoff{}),
		catalog:    models.Catalog(config.TierZen, nil),
	}
}

// runAnonymousLoop drives doAnonymousUpstream once and reports the returned
// status and the number of upstream attempts it made.
func runAnonymousLoop(t *testing.T, status, proxyCount int) (int, int) {
	t.Helper()
	g := newAnonymousLoopGateway(proxyCount, status)
	// A diagnostic context keeps the loop test free of proxy-health side
	// effects; the rotation decision under test is independent of them.
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true}
	bodies := map[config.Tier][]byte{
		config.TierZen: []byte(`{"model":"test-free","messages":[{"role":"user","content":"hi"}]}`),
	}
	ids := identity.RequestIDs{Session: "ses_0123456789abcdefghijklmnop", Request: "req_test", Project: "proj"}
	resp, err, attempts := g.doAnonymousUpstream(ctx, route, bodies, ids, 0)
	if err != nil {
		t.Fatalf("doAnonymousUpstream returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("doAnonymousUpstream returned a nil response")
	}
	return resp.StatusCode, attempts
}

// A request-shape rejection (400) is deterministic: every proxy answers the
// same way, so the loop must stop after the first attempt instead of rotating
// through the whole pool. Rotating burned the request budget and made the
// client wait ~300s for an oversized-context rejection.
func TestAnonymousLoopShortCircuitsOnNonRetryable4xx(t *testing.T) {
	status, attempts := runAnonymousLoop(t, http.StatusBadRequest, 8)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (must not rotate on a non-retryable 4xx)", attempts)
	}
}

// Rate limits, authentication failures, and server errors are proxy-dependent:
// another exit IP can succeed, so the loop must still try every proxy.
func TestAnonymousLoopRotatesOnRetryableStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			got, attempts := runAnonymousLoop(t, status, 6)
			if got != status {
				t.Fatalf("status = %d, want %d", got, status)
			}
			if attempts < 6 {
				t.Fatalf("attempts = %d, want >= 6 (retryable status must rotate across proxies)", attempts)
			}
		})
	}
}

func TestAnonymousLoopReturnsOnFirstSuccess(t *testing.T) {
	status, attempts := runAnonymousLoop(t, http.StatusOK, 8)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// newTierGateway builds a Gateway whose anonymous lane and key tiers all
// answer with the given status, so the route-level fallback can be observed
// by counting attempts.
func newTierGateway(status int, zenKeys, goKeys []string) (*Gateway, error) {
	proxies := make([]*proxyTransport, 0, 4)
	for i := 0; i < 4; i++ {
		proxy := &proxyTransport{index: i, name: "test-proxy", client: &http.Client{Transport: stubRoundTripper{status: status}}, bun: &http.Client{Transport: stubRoundTripper{status: status}}}
		proxy.healthy.Store(true)
		proxies = append(proxies, proxy)
	}
	transports := &proxyTransports{items: proxies}
	cooldown := 15 * time.Second
	zenNodes, err := nodePool(zenKeys, transports, cooldown)
	if err != nil {
		return nil, err
	}
	goNodes, err := nodePool(goKeys, transports, cooldown)
	if err != nil {
		return nil, err
	}
	cfg := config.Config{}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	cfg.Upstream.Zen = "http://zen.test"
	cfg.Upstream.Go = "http://go.test"
	cfg.Retry.MaxAttempts = 3
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
		zenNodes:   zenNodes,
		goNodes:    goNodes,
		anonymous:  anonymousPool(true, transports, cooldown, anonymousBackoff{}),
		catalog:    models.Catalog(config.TierZen, nil),
	}, nil
}

func routeBodies() map[config.Tier][]byte {
	body := []byte(`{"model":"test-free","messages":[{"role":"user","content":"hi"}]}`)
	return map[config.Tier][]byte{config.TierZen: body, config.TierGo: body}
}

func testRequestIDs() identity.RequestIDs {
	return identity.RequestIDs{Session: "ses_0123456789abcdefghijklmnop", Request: "req_test", Project: "proj"}
}

// A non-retryable client error from the anonymous lane is deterministic: no
// key tier accepts the same request shape, so the route must end there rather
// than re-sending the request to every authenticated tier.
func TestRouteEndsOnNonRetryable4xxAfterAnonymous(t *testing.T) {
	g, err := newTierGateway(http.StatusBadRequest, []string{"zen-key"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true, KeyTiers: []config.Tier{config.TierZen}}
	resp, _, attempts, err := g.doUpstreamTiers(ctx, route, routeBodies(), testRequestIDs(), 0)
	if err != nil {
		t.Fatalf("doUpstreamTiers error = %v, want the 400 to end the route", err)
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resp = %v, want a 400 response", resp)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (must not fall back to key tiers)", attempts)
	}
}

// A non-retryable client error from one key tier must also end the route
// instead of rotating through the remaining tiers.
func TestRouteEndsOnNonRetryable4xxAcrossKeyTiers(t *testing.T) {
	g, err := newTierGateway(http.StatusBadRequest, []string{"zen-key"}, []string{"go-key"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: false, KeyTiers: []config.Tier{config.TierZen, config.TierGo}}
	resp, _, attempts, err := g.doUpstreamTiers(ctx, route, routeBodies(), testRequestIDs(), 0)
	if err != nil {
		t.Fatalf("doUpstreamTiers error = %v, want the 400 to end the route", err)
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resp = %v, want a 400 response", resp)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (must not fall back to the next key tier)", attempts)
	}
}

package gateway

import (
	"context"
	"errors"
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

// stubRoundTripper answers every request with a fixed status and body so the
// anonymous rotation loop can be exercised without a network. An empty body
// stands in for the smallest possible JSON object.
type stubRoundTripper struct {
	status int
	body   string
}

func (s stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	body := s.body
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
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
// another exit IP can succeed, so the loop must still try every proxy. The
// 429 here carries no free-tier allowance marker, so it stays a per-exit rate
// limit: only the shared-allowance answer below ends the walk.
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

// newFreeUsageLimitGateway builds an anonymous pool of proxyCount always-healthy
// nodes whose upstream answers with the free tier's exhausted-allowance 429.
func newFreeUsageLimitGateway(proxyCount int) *Gateway {
	const body = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`
	proxies := make([]*proxyTransport, 0, proxyCount)
	for i := 0; i < proxyCount; i++ {
		proxy := &proxyTransport{
			index:  i,
			name:   "test-proxy",
			client: &http.Client{Transport: stubRoundTripper{status: http.StatusTooManyRequests, body: body}},
			bun:    &http.Client{Transport: stubRoundTripper{status: http.StatusTooManyRequests, body: body}},
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

// The free tier counts its allowance per credential across every exit IP, so
// the same 429 comes back on the next proxy. The loop must stop after the
// first attempt and hand that response to the caller: walking the remaining
// exits only multiplies the upstream requests and the latency by the number
// of exits left, which is what a 32-attempt, 53-second request looked like.
func TestAnonymousLoopStopsOnFreeUsageLimit(t *testing.T) {
	g := newFreeUsageLimitGateway(8)
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true}
	bodies := map[config.Tier][]byte{
		config.TierZen: []byte(`{"model":"test-free","messages":[{"role":"user","content":"hi"}]}`),
	}
	ids := identity.RequestIDs{Session: "ses_0123456789abcdefghijklmnop", Request: "req_test", Project: "proj"}

	resp, err, attempts := g.doAnonymousUpstream(ctx, route, bodies, ids, 0)
	if !errors.Is(err, errFreeUsageLimit) {
		t.Fatalf("err = %v, want errFreeUsageLimit", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %v, want the upstream 429", resp)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (an exhausted allowance must not walk the pool)", attempts)
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
	return newTierGatewayWithBody(status, "", zenKeys, goKeys)
}

// newTierGatewayWithBody is newTierGateway with a fixed upstream body, so a
// test can give every lane the same error payload.
func newTierGatewayWithBody(status int, body string, zenKeys, goKeys []string) (*Gateway, error) {
	proxies := make([]*proxyTransport, 0, 4)
	for i := 0; i < 4; i++ {
		proxy := &proxyTransport{index: i, name: "test-proxy", client: &http.Client{Transport: stubRoundTripper{status: status, body: body}}, bun: &http.Client{Transport: stubRoundTripper{status: status, body: body}}}
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

// With no authenticated tier configured, an exhausted allowance is the
// client's answer: the route returns the upstream 429 unchanged, so its
// Retry-After reaches the caller, and it does so after a single attempt.
func TestRouteReturnsFreeUsageLimitWithNoKeyTier(t *testing.T) {
	g := newFreeUsageLimitGateway(8)
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true}
	resp, _, attempts, err := g.doUpstreamTiers(ctx, route, routeBodies(), testRequestIDs(), 0)
	if err != nil {
		t.Fatalf("doUpstreamTiers error = %v, want the 429 handed to the client", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %v, want a 429 response", resp)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no key tier is configured)", attempts)
	}
}

// A key that serves a free model draws on the same shared allowance as the
// anonymous lane, so its 429 ends the key rotation too: every remaining key
// would answer identically.
func TestRouteStopsOnFreeUsageLimitAcrossKeyTiers(t *testing.T) {
	const body = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`
	g, err := newTierGatewayWithBody(http.StatusTooManyRequests, body, []string{"zen-key-1", "zen-key-2", "zen-key-3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithDiagnosticRequest(context.Background())
	route := models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: false, KeyTiers: []config.Tier{config.TierZen}}
	resp, _, attempts, err := g.doUpstreamTiers(ctx, route, routeBodies(), testRequestIDs(), 0)
	if err != nil {
		t.Fatalf("doUpstreamTiers error = %v, want the 429 handed to the client", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %v, want a 429 response", resp)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (an exhausted allowance must not rotate keys)", attempts)
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

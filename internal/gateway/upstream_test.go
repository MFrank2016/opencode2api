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
		}
		proxy.healthy.Store(true)
		proxies = append(proxies, proxy)
	}
	transports := &transportPool{items: proxies}
	cfg := config.Config{}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
		anonymous:  newAnonymousPool(true, transports, 15*time.Second),
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
			if attempts != 6 {
				t.Fatalf("attempts = %d, want 6 (retryable status must rotate)", attempts)
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

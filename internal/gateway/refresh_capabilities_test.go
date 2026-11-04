package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingCapabilitiesTransport records how many proxy routes the capability
// refresh tried. A slow proxy must not let the 30-second refresh budget be
// consumed by hundreds of sequential attempts, which is what left the catalog
// with only two models when the decodo pool was large.
type countingCapabilitiesTransport struct {
	mu       sync.Mutex
	calls    int
	response *http.Response
	err      error
}

func (c *countingCapabilitiesTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if c.response != nil {
		return c.response, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    r,
	}, nil
}

func (c *countingCapabilitiesTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newCapabilitiesGateway(proxyCount int) (*Gateway, *countingCapabilitiesTransport) {
	rt := &countingCapabilitiesTransport{err: context.DeadlineExceeded}
	transports := &proxyTransports{}
	for i := 0; i < proxyCount; i++ {
		proxy := &proxyTransport{
			index:  i,
			name:   "proxy-" + string(rune('a'+i)),
			client: &http.Client{Transport: rt},
			bun:    &http.Client{Transport: rt},
		}
		proxy.healthy.Store(true)
		transports.items = append(transports.items, proxy)
	}
	return &Gateway{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
	}, rt
}

// A large proxy pool must not be walked from end to end for one capability
// refresh: the refresh has a shared 30-second budget, and a dead proxy near
// the front would otherwise starve the model listing. Cap the attempts to a
// small fixed number of healthy routes.
func TestCapabilityRefreshLimitsProxyAttempts(t *testing.T) {
	g, rt := newCapabilitiesGateway(200)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := g.refreshProtocolCapabilities(ctx)
	if err == nil {
		t.Fatal("expected the capability refresh to fail with all proxies down")
	}
	if got := rt.count(); got > maxCapabilityRefreshProxies {
		t.Fatalf("capability refresh tried %d proxies, want at most %d", got, maxCapabilityRefreshProxies)
	}
}

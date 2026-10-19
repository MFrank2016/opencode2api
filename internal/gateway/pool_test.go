package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// newTestAnonymousPool builds an anonymous pool of n always-healthy nodes with
// the given base cooldown, mirroring newAnonymousPool's production wiring.
func newTestAnonymousPool(t *testing.T, n int, cooldown time.Duration) (*anonymousPool, []*anonymousNode) {
	t.Helper()
	items := make([]*proxyTransport, 0, n)
	for i := 0; i < n; i++ {
		proxy := &proxyTransport{index: i, name: fmt.Sprintf("proxy-%d", i)}
		proxy.healthy.Store(true)
		items = append(items, proxy)
	}
	pool := newAnonymousPool(true, &transportPool{items: items}, cooldown)
	if len(pool.nodes) != n {
		t.Fatalf("pool built with %d nodes, want %d", len(pool.nodes), n)
	}
	return pool, pool.nodes
}

func responseWithStatus(status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header)}
}

// A 403 is a request-level rejection (credential or upstream policy), not a
// proxy failure: every exit IP gets the same answer, so cooling the node only
// drains the pool. See the FreeTierError regression this guards against.
func TestAnonymousMarkFailureDoesNotCoolOnForbidden(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, responseWithStatus(http.StatusForbidden), nil)

	if got := node.cooldownUntil.Load(); got != 0 {
		t.Fatalf("403 set cooldownUntil=%d, want 0 (no cooldown)", got)
	}
	if got := node.failures.Load(); got != 0 {
		t.Fatalf("403 counted failures=%d, want 0", got)
	}
}

func TestAnonymousMarkFailureDoesNotCoolOnUnauthorized(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, responseWithStatus(http.StatusUnauthorized), nil)

	if got := node.cooldownUntil.Load(); got != 0 {
		t.Fatalf("401 set cooldownUntil=%d, want 0 (no cooldown)", got)
	}
}

// The core regression: a burst of global 403s across every node must leave the
// pool usable. Before the fix this cooled every node and Next() returned nil,
// surfacing as the misleading "no healthy anonymous proxies available".
func TestAnonymousGlobalForbiddenDoesNotDrainPool(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 4, 15*time.Second)
	for _, node := range nodes {
		pool.MarkFailure(node, responseWithStatus(http.StatusForbidden), nil)
	}

	cursor := pool.CursorFor("session-affinity")
	if got := cursor.Next(); got == nil {
		t.Fatal("pool drained after global 403: Next() returned nil")
	}
}

// A per-IP rate limit is proxy-specific: another exit IP may succeed, so the
// node must cool down.
func TestAnonymousMarkFailureCoolsOnTooManyRequests(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, responseWithStatus(http.StatusTooManyRequests), nil)

	if node.cooldownUntil.Load() <= time.Now().UnixNano() {
		t.Fatal("429 did not cool the node down")
	}
}

func TestAnonymousMarkFailureCoolsOnServerError(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, responseWithStatus(http.StatusBadGateway), nil)

	if node.cooldownUntil.Load() <= time.Now().UnixNano() {
		t.Fatal("502 did not cool the node down")
	}
}

func TestAnonymousMarkFailureCoolsOnTransportError(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, nil, errors.New("dial tcp: connection refused"))

	if node.cooldownUntil.Load() <= time.Now().UnixNano() {
		t.Fatal("transport error did not cool the node down")
	}
}

// A longer Retry-After must still win over the base cooldown.
func TestAnonymousMarkFailureHonorsRetryAfter(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	resp := responseWithStatus(http.StatusTooManyRequests)
	resp.Header.Set("Retry-After", "120")
	pool.MarkFailure(node, resp, nil)

	minUntil := time.Now().Add(100 * time.Second).UnixNano()
	if got := node.cooldownUntil.Load(); got < minUntil {
		t.Fatalf("cooldownUntil=%d did not honor Retry-After: 120s", got)
	}
}

// When a healthy proxy is available, Next() must prefer it over a cooling one.
// (The v1.4 upstream added a cooling-node fallback for when every static proxy
// is unavailable, so the right invariant now is ordering, not exclusion.)
func TestAnonymousNextPrefersHealthyOverCooling(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 2, 15*time.Second)
	pool.MarkFailure(nodes[0], responseWithStatus(http.StatusTooManyRequests), nil)
	cursor := pool.CursorFor("")
	first := cursor.Next()
	if first == nil {
		t.Fatal("Next() returned nil")
	}
	if first == nodes[0] {
		t.Fatal("Next() returned the cooling node first; should prefer the healthy node")
	}
	if first != nodes[1] {
		t.Fatal("Next() did not return the healthy node first")
	}
}

func TestAnonymousMarkSuccessResetsCooldown(t *testing.T) {
	pool, nodes := newTestAnonymousPool(t, 1, 15*time.Second)
	node := nodes[0]

	pool.MarkFailure(node, responseWithStatus(http.StatusTooManyRequests), nil)
	pool.MarkSuccess(node)

	if got := node.cooldownUntil.Load(); got != 0 {
		t.Fatalf("MarkSuccess left cooldownUntil=%d, want 0", got)
	}
	if got := node.failures.Load(); got != 0 {
		t.Fatalf("MarkSuccess left failures=%d, want 0", got)
	}
}

// Cooldown grows exponentially and is capped at 8x the base (1<<min(failures-1,3)).
func TestAnonymousCooldownBacksOffExponentially(t *testing.T) {
	base := 10 * time.Second
	pool, nodes := newTestAnonymousPool(t, 1, base)
	node := nodes[0]

	for i := 0; i < 5; i++ {
		pool.MarkFailure(node, responseWithStatus(http.StatusTooManyRequests), nil)
	}

	// failures=5 -> multiplier 1<<3 = 8 -> 80s.
	remaining := time.Duration(node.cooldownUntil.Load() - time.Now().UnixNano())
	if remaining < 70*time.Second || remaining > 80*time.Second {
		t.Fatalf("cooldown remaining=%s, want ~80s (8x base)", remaining)
	}
}

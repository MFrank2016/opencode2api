package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

// responseStep is one canned upstream answer in an exit-rotation test.
type responseStep struct {
	status int
	body   string
	header http.Header
}

// sharedSequence serves a canned sequence of responses to every proxy that
// shares it, advancing once per call. This models exits that answer in the
// order the gateway walks them: first the exhausted-allowance 429, then a 200.
// A mutex keeps it safe if the gateway ever issues concurrent attempts.
type sharedSequence struct {
	mu    sync.Mutex
	steps []responseStep
	calls int
}

func (s *sharedSequence) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	idx := s.calls
	if idx >= len(s.steps) {
		idx = len(s.steps) - 1
	}
	s.calls++
	s.mu.Unlock()
	step := s.steps[idx]
	body := step.body
	if body == "" {
		body = "{}"
	}
	h := step.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{
		StatusCode: step.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

const freeLimitBody = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`

func newRotatingGateway(t *testing.T, rotation bool, steps []responseStep, proxyCount int) *Gateway {
	t.Helper()
	seq := &sharedSequence{steps: steps}
	transports := &proxyTransports{}
	for i := 0; i < proxyCount; i++ {
		proxy := &proxyTransport{
			index:  i,
			name:   "proxy-" + string(rune('a'+i)),
			client: &http.Client{Transport: seq},
			bun:    &http.Client{Transport: seq},
		}
		proxy.healthy.Store(true)
		transports.items = append(transports.items, proxy)
	}
	cfg := config.Config{}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	cfg.Performance.FreeLimitRotation = rotation
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
		anonymous:  anonymousPool(true, transports, 15*time.Second, anonymousBackoff{}),
		catalog:    models.Catalog(config.TierZen, nil),
	}
}

func rotatingBodies() map[config.Tier][]byte {
	return map[config.Tier][]byte{
		config.TierZen: []byte(`{"model":"test-free","messages":[{"role":"user","content":"hi"}]}`),
	}
}

func rotatingIDs() identity.RequestIDs {
	return identity.RequestIDs{Session: "ses_0123456789abcdefghijklmnop", Request: "req_rotate", Project: "proj"}
}

func rotatingRoute() models.Route {
	return models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true}
}

// With free_limit_rotation enabled, an exhausted-allowance 429 is scoped to
// that exit IP: another exit can still have allowance. The gateway must cool
// the first exit and keep walking until one succeeds.
func TestAnonymousLoopRotatesAcrossExitsOnFreeUsageLimit(t *testing.T) {
	steps := []responseStep{
		{http.StatusTooManyRequests, freeLimitBody, nil},
		{http.StatusOK, `{"ok":true}`, nil},
	}
	g := newRotatingGateway(t, true, steps, 2)
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, rotatingRoute(), rotatingBodies(), rotatingIDs(), 0)
	if err != nil {
		t.Fatalf("doAnonymousUpstream error = %v, want a successful second exit", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %v, want 200 from the second exit", resp)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (first exit 429, second exit 200)", attempts)
	}
}

// The exhausted exit must stay out of the cursor for the Retry-After window.
// A second request in the same session must go straight to the healthy exit.
func TestFreeLimitRotationCoolsExhaustedExitForRetryAfter(t *testing.T) {
	header := make(http.Header)
	header.Set("Retry-After", "120")
	steps := []responseStep{
		{http.StatusTooManyRequests, freeLimitBody, header},
		{http.StatusOK, `{"ok":true}`, nil},
	}
	g := newRotatingGateway(t, true, steps, 2)
	ctx := WithDiagnosticRequest(context.Background())

	resp, _, _ := g.doAnonymousUpstream(ctx, rotatingRoute(), rotatingBodies(), rotatingIDs(), 0)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %v, want 200", resp)
	}
	exhausted := g.transports.items[0]
	if !exhausted.freeLimitBlocked(time.Now()) {
		t.Fatal("exhausted exit was not cooled for the Retry-After window")
	}
	remaining := time.Until(time.Unix(0, exhausted.freeLimitUntil.Load()))
	if remaining < 110*time.Second || remaining > 120*time.Second {
		t.Fatalf("cool-down = %v, want about 120s", remaining)
	}
}

// When every exit answers with the exhausted-allowance 429, the rotation path
// still hands the last 429 (and its Retry-After) to the client after trying
// each exit once.
func TestFreeLimitRotationReturns429WhenAllExitsExhausted(t *testing.T) {
	header := make(http.Header)
	header.Set("Retry-After", "300")
	steps := []responseStep{
		{http.StatusTooManyRequests, freeLimitBody, header},
	}
	g := newRotatingGateway(t, true, steps, 3)
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, rotatingRoute(), rotatingBodies(), rotatingIDs(), 0)
	if !errors.Is(err, errFreeUsageLimit) {
		t.Fatalf("err = %v, want errFreeUsageLimit", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want the upstream 429", resp)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (one per exit)", attempts)
	}
}

// With the option off, the original fail-fast behavior is preserved: a single
// 429 ends the walk after one attempt.
func TestAnonymousLoopStillFailsFastWithoutFreeLimitRotation(t *testing.T) {
	steps := []responseStep{
		{http.StatusTooManyRequests, freeLimitBody, nil},
		{http.StatusOK, `{"ok":true}`, nil},
	}
	g := newRotatingGateway(t, false, steps, 3)
	ctx := WithDiagnosticRequest(context.Background())

	_, err, attempts := g.doAnonymousUpstream(ctx, rotatingRoute(), rotatingBodies(), rotatingIDs(), 0)
	if !errors.Is(err, errFreeUsageLimit) {
		t.Fatalf("err = %v, want errFreeUsageLimit", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (fail-fast when rotation is disabled)", attempts)
	}
}

// A per-exit 429 rotation must not count toward the anonymous lane circuit
// breaker: the lane is healthy, only that exit's allowance is gone.
func TestFreeLimitRotationDoesNotTripLaneBreaker(t *testing.T) {
	steps := []responseStep{
		{http.StatusTooManyRequests, freeLimitBody, nil},
	}
	g := newRotatingGateway(t, true, steps, 2)
	g.anonymous.backoff = anonymousBackoff{threshold: 2, base: time.Hour, max: time.Hour}
	ctx := WithDiagnosticRequest(context.Background())

	_, _, _ = g.doAnonymousUpstream(ctx, rotatingRoute(), rotatingBodies(), rotatingIDs(), 0)
	if g.anonymous.Open() {
		t.Fatal("lane circuit breaker opened after per-exit allowance 429s")
	}
	if g.anonymous.consecutive.Load() != 0 {
		t.Fatalf("consecutive failures = %d, want 0", g.anonymous.consecutive.Load())
	}
}

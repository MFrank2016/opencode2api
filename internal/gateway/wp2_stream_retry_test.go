package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

// sseStub serves a canned SSE body per call. An empty step models an upstream
// that accepts the request (200 + text/event-stream) and then closes before
// sending any event, which is the failure the empty-stream retry must absorb.
type sseStub struct {
	mu    sync.Mutex
	steps []string
	calls int
}

func (s *sseStub) RoundTrip(r *http.Request) (*http.Response, error) {
	// The real transport drives httptrace, which is how doInferenceAttempt
	// decides the failure phase. A stub that skips it would report "dial" and
	// hide the empty-stream classification under test.
	if trace := httptrace.ContextClientTrace(r.Context()); trace != nil {
		if trace.GotConn != nil {
			trace.GotConn(httptrace.GotConnInfo{})
		}
		if trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		if trace.GotFirstResponseByte != nil {
			trace.GotFirstResponseByte()
		}
	}
	s.mu.Lock()
	idx := s.calls
	if idx >= len(s.steps) {
		idx = len(s.steps) - 1
	}
	s.calls++
	s.mu.Unlock()
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(s.steps[idx])),
		Request:    r,
	}, nil
}

func (s *sseStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

const validSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"

func newStreamRetryGateway(t *testing.T, steps []string, proxyCount, maxRetries int) (*Gateway, *sseStub) {
	t.Helper()
	stub := &sseStub{steps: steps}
	transports := &proxyTransports{}
	for i := 0; i < proxyCount; i++ {
		proxy := &proxyTransport{
			index:  i,
			name:   "proxy-" + string(rune('a'+i)),
			client: &http.Client{Transport: stub},
			bun:    &http.Client{Transport: stub},
		}
		proxy.healthy.Store(true)
		transports.items = append(transports.items, proxy)
	}
	cfg := config.Config{}
	cfg.Upstream.Anonymous = "http://anonymous.test"
	cfg.Performance.FirstEventTimeoutSeconds = 1
	cfg.Performance.StreamEmptyRetryMax = maxRetries
	return &Gateway{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		transports: transports,
		anonymous:  anonymousPool(true, transports, 15*time.Second, anonymousBackoff{}),
		catalog:    models.Catalog(config.TierZen, nil),
	}, stub
}

func streamRetryBodies() map[config.Tier][]byte {
	return map[config.Tier][]byte{
		config.TierZen: []byte(`{"model":"test-free","messages":[{"role":"user","content":"hi"}]}`),
	}
}

func streamRetryIDs() identity.RequestIDs {
	return identity.RequestIDs{Session: "ses_0123456789abcdefghijklmnop", Request: "req_stream", Project: "proj"}
}

func streamRetryRoute() models.Route {
	return models.Route{ID: "test-free", Tier: config.TierZen, Protocol: wire.Chat, Anonymous: true}
}

// An upstream that accepts the request and then closes before the first event
// is a transient, per-attempt failure: the gateway must try the next exit
// instead of failing the client.
func TestEmptyStreamRetriesOnNextExit(t *testing.T) {
	g, stub := newStreamRetryGateway(t, []string{"", validSSE}, 2, 1)
	// A diagnostic context keeps the loop test free of proxy-health side
	// effects; the retry decision under test is independent of them.
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, streamRetryRoute(), streamRetryBodies(), streamRetryIDs(), 0)
	if err != nil {
		t.Fatalf("doAnonymousUpstream error = %v, want a successful retry", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %v, want 200 from the second attempt", resp)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (empty stream, then a valid one)", attempts)
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

// The retry budget bounds how many empty streams one request absorbs before it
// gives up, so a pathological upstream cannot walk the whole pool.
func TestEmptyStreamStopsAfterRetryBudget(t *testing.T) {
	g, stub := newStreamRetryGateway(t, []string{"", "", validSSE}, 4, 1)
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, streamRetryRoute(), streamRetryBodies(), streamRetryIDs(), 0)
	if err == nil {
		t.Fatalf("expected an error after the retry budget, got resp=%v", resp)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry after the first empty stream)", attempts)
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

// A normal stream is unaffected and costs exactly one attempt.
func TestNormalStreamNotRetried(t *testing.T) {
	g, stub := newStreamRetryGateway(t, []string{validSSE}, 3, 1)
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, streamRetryRoute(), streamRetryBodies(), streamRetryIDs(), 0)
	if err != nil {
		t.Fatalf("doAnonymousUpstream error = %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %v, want 200", resp)
	}
	if attempts != 1 || stub.count() != 1 {
		t.Fatalf("attempts = %d, upstream calls = %d, want 1/1", attempts, stub.count())
	}
}

// A larger budget absorbs more empty streams before giving up.
func TestEmptyStreamHonorsLargerBudget(t *testing.T) {
	g, _ := newStreamRetryGateway(t, []string{"", "", validSSE}, 4, 2)
	ctx := WithDiagnosticRequest(context.Background())

	resp, err, attempts := g.doAnonymousUpstream(ctx, streamRetryRoute(), streamRetryBodies(), streamRetryIDs(), 0)
	if err != nil {
		t.Fatalf("error = %v, want success with budget 2", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %v, want 200", resp)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (two empty streams, then success)", attempts)
	}
}

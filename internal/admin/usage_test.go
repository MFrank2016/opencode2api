package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
)

func TestHandleUsageReturnsPerKeyCounters(t *testing.T) {
	monitor := telemetry.Monitor()
	monitor.Record("/v1/chat/completions", 200, time.Millisecond, &telemetry.RequestMeta{
		Model: "m", Tier: "zen", ServerKeyID: "key-a", Usage: protocol.Usage{Input: 3, Output: 4, Total: 7}, UsageReported: true,
	})
	server := &Server{monitor: monitor}

	rec := httptest.NewRecorder()
	server.handleUsage(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Lifetime map[string]json.RawMessage `json:"lifetime"`
		LastHour map[string]json.RawMessage `json:"last_hour"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body.Lifetime["key-a"]; !ok {
		t.Fatalf("lifetime missing key-a: %s", rec.Body.String())
	}
	if body.LastHour == nil {
		t.Fatal("last_hour must be present")
	}
}

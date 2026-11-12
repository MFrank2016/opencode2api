package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"opencode2api/internal/protocol"
)

func recordOne(m *Metrics) {
	m.Record("/v1/chat/completions", 200, 5*time.Millisecond, &RequestMeta{
		Model: "test-free", Tier: "zen", ServerKeyID: "key-a", Session: "ses_1", Request: "req_1",
		Channel: "key", Usage: protocol.Usage{Input: 10, Output: 20, Total: 30}, UsageReported: true,
	})
}

func TestUsageSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	m := Monitor()
	recordOne(m)
	if err := m.SaveUsage(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	restored := Monitor()
	if err := restored.LoadUsage(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	snap := restored.Snapshot()
	if snap.Lifetime.Total != 1 || snap.Lifetime.Success != 1 {
		t.Fatalf("lifetime = %+v, want one success", snap.Lifetime)
	}
	if snap.Usage.Lifetime.Requests != 1 {
		t.Fatalf("usage requests = %d, want 1", snap.Usage.Lifetime.Requests)
	}
	if got := snap.Usage.Lifetime.Tokens.Input; got != 10 {
		t.Fatalf("input tokens = %d, want 10", got)
	}
	if usage, ok := snap.Usage.Lifetime.ServerKeys["key-a"]; !ok || usage.Requests != 1 {
		t.Fatalf("server key usage = %+v, want key-a/1", usage)
	}
}

func TestUsageSnapshotCorruptFileIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Monitor()
	if err := m.LoadUsage(path); err == nil {
		t.Fatal("expected an error for a corrupt snapshot")
	}
	if snap := m.Snapshot(); snap.Lifetime.Total != 0 {
		t.Fatalf("corrupt snapshot changed counters: %+v", snap.Lifetime)
	}
}

func TestUsageSnapshotMissingFileIsNotAnError(t *testing.T) {
	m := Monitor()
	if err := m.LoadUsage(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("missing file should be ignored, got %v", err)
	}
}

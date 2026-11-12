package admin

import (
	"encoding/json"
	"testing"

	"opencode2api/internal/config"
)

func TestMergeConfigBlockPreservesOmittedFields(t *testing.T) {
	current := config.PerformanceConfig{
		MaxIdleConns:        2048,
		FreeLimitRotation:   true,
		StreamEmptyRetryMax: 1,
		ToolCallFilter:      "manifest-only",
	}
	got := current
	if err := mergeConfigBlock(json.RawMessage(`{"max_idle_conns":100}`), &got); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got.MaxIdleConns != 100 {
		t.Fatalf("submitted field = %d, want 100", got.MaxIdleConns)
	}
	if !got.FreeLimitRotation || got.StreamEmptyRetryMax != 1 || got.ToolCallFilter != "manifest-only" {
		t.Fatalf("omitted fields were zeroed: %+v", got)
	}
}

func TestMergeConfigBlockNullKeepsCurrent(t *testing.T) {
	current := config.MetricsConfig{Enabled: true, Token: "t", Path: "/metrics"}
	got := current
	if err := mergeConfigBlock(json.RawMessage(`null`), &got); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got != current {
		t.Fatalf("null block changed the config: %+v", got)
	}
}

func TestMergeModelsBlockKeepsOmittedMaps(t *testing.T) {
	current := config.ModelsConfig{
		RefreshSeconds: 300,
		Protocols:      map[string]string{"a": "chat"},
		Aliases:        map[string]string{"x": "y"},
		Context:        map[string]int{"a": 1000},
	}
	got, err := mergeModelsBlock(json.RawMessage(`{"refresh_seconds":60}`), current)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got.RefreshSeconds != 60 {
		t.Fatalf("refresh = %d, want 60", got.RefreshSeconds)
	}
	if got.Aliases["x"] != "y" || got.Protocols["a"] != "chat" || got.Context["a"] != 1000 {
		t.Fatalf("omitted maps were changed: %+v", got)
	}
}

func TestMergeModelsBlockReplacesSubmittedMaps(t *testing.T) {
	current := config.ModelsConfig{
		Protocols: map[string]string{"a": "chat"},
		Aliases:   map[string]string{"x": "y"},
	}
	got, err := mergeModelsBlock(json.RawMessage(`{"protocols":{"b":"responses"},"aliases":null}`), current)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(got.Protocols) != 1 || got.Protocols["b"] != "responses" {
		t.Fatalf("protocols = %+v, want exactly b", got.Protocols)
	}
	if got.Aliases != nil {
		t.Fatalf("explicit null aliases should clear the map, got %+v", got.Aliases)
	}
}

package models

import (
	"testing"

	"opencode2api/internal/config"
	wire "opencode2api/internal/protocol"
)

func catalogWithModels(contextWindows map[string]int) *ModelCatalog {
	ids := make([]string, 0, len(contextWindows))
	protocols := map[string]wire.Protocol{}
	metadata := map[string]Metadata{}
	for model, window := range contextWindows {
		ids = append(ids, model)
		protocols[model] = wire.Chat
		metadata[model] = Metadata{ContextWindow: window}
	}
	c := Catalog(config.TierZen, nil)
	c.ReplaceWithCapabilities(
		ids,
		nil,
		map[config.Tier]map[string]wire.Protocol{config.TierZen: protocols},
		map[config.Tier]map[string]bool{config.TierZen: {}},
		map[config.Tier]map[string]Metadata{config.TierZen: metadata},
	)
	return c
}

// The capability catalog under-reports the free tier's context window, so an
// operator override must replace what /v1/models advertises.
func TestContextOverrideReplacesAdvertisedWindow(t *testing.T) {
	c := catalogWithModels(map[string]int{"glm-5.3-flash-free": 200000})
	c.SetContextOverrides(map[string]int{"glm-5.3-flash-free": 1048576})
	if got := c.MetadataForTier("glm-5.3-flash-free", config.TierZen).ContextWindow; got != 1048576 {
		t.Fatalf("ContextWindow = %d, want 1048576 (override)", got)
	}
}

// An override for one model must not touch any other model's window.
func TestContextOverrideLeavesOtherModelsUntouched(t *testing.T) {
	c := catalogWithModels(map[string]int{"glm-5.3-flash-free": 200000, "other-free": 200000})
	c.SetContextOverrides(map[string]int{"glm-5.3-flash-free": 1048576})
	if got := c.MetadataForTier("other-free", config.TierZen).ContextWindow; got != 200000 {
		t.Fatalf("other-free ContextWindow = %d, want 200000 (no override)", got)
	}
}

// Non-positive values and empty names are ignored rather than blanking a window.
func TestContextOverrideIgnoresInvalidEntries(t *testing.T) {
	c := catalogWithModels(map[string]int{"glm-5.3-flash-free": 200000})
	c.SetContextOverrides(map[string]int{"glm-5.3-flash-free": 0, "": 1048576})
	if got := c.MetadataForTier("glm-5.3-flash-free", config.TierZen).ContextWindow; got != 200000 {
		t.Fatalf("ContextWindow = %d, want 200000 (invalid override ignored)", got)
	}
}

// catalogWithMeta builds a catalog whose models carry the given Metadata.
func catalogWithMeta(entries map[string]Metadata) *ModelCatalog {
	ids := make([]string, 0, len(entries))
	protocols := map[string]wire.Protocol{}
	for model := range entries {
		ids = append(ids, model)
		protocols[model] = wire.Chat
	}
	c := Catalog(config.TierZen, nil)
	c.ReplaceWithCapabilities(
		ids,
		nil,
		map[config.Tier]map[string]wire.Protocol{config.TierZen: protocols},
		map[config.Tier]map[string]bool{config.TierZen: {}},
		map[config.Tier]map[string]Metadata{config.TierZen: entries},
	)
	return c
}

// A max-output override replaces what /v1/models advertises for max_output.
func TestMaxOutputOverrideReplacesAdvertisedValue(t *testing.T) {
	c := catalogWithMeta(map[string]Metadata{"longcat-2.5-preview-free": {ContextWindow: 1048576, MaxOutput: 131072}})
	c.SetMaxOutputOverrides(map[string]int{"longcat-2.5-preview-free": 262144})
	if got := c.MetadataForTier("longcat-2.5-preview-free", config.TierZen).MaxOutput; got != 262144 {
		t.Fatalf("MaxOutput = %d, want 262144 (override)", got)
	}
}

func TestMaxOutputOverrideLeavesOtherModelsUntouched(t *testing.T) {
	c := catalogWithMeta(map[string]Metadata{"longcat-2.5-preview-free": {ContextWindow: 1048576, MaxOutput: 131072}, "other-free": {ContextWindow: 1048576, MaxOutput: 131072}})
	c.SetMaxOutputOverrides(map[string]int{"longcat-2.5-preview-free": 262144})
	if got := c.MetadataForTier("other-free", config.TierZen).MaxOutput; got != 131072 {
		t.Fatalf("other-free MaxOutput = %d, want 131072 (no override)", got)
	}
}

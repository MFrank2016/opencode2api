package models

import (
	"testing"

	"opencode2api/internal/config"
	wire "opencode2api/internal/protocol"
)

// models.dev publishes limits per provider. The primary OpenCode provider
// must win for duplicated model IDs, and other OpenCode tiers must still
// contribute the models they alone describe.
func TestDecodeModelsDevKeepsLimitsAndMergesTiers(t *testing.T) {
	payload := []byte(`{
	  "opencode": {"id":"opencode","models":{
	    "shared":{"id":"shared","limit":{"context":1000,"output":100},"cost":{"input":1,"output":2}},
	    "zen-only":{"id":"zen-only","limit":{"context":2000,"input":1500,"output":200}}
	  }},
	  "opencode-go": {"id":"opencode-go","models":{
	    "shared":{"id":"shared","limit":{"context":9000,"output":900},"cost":{"input":1,"output":2}},
	    "go-only":{"id":"go-only","limit":{"context":4000,"output":400},"cost":{"input":0,"output":0}}
	  }},
	  "anthropic":{"id":"anthropic","models":{"claude":{"id":"claude","limit":{"context":7}}}}
	}`)

	models, err := decodeModelsDev(payload)
	if err != nil {
		t.Fatalf("decodeModelsDev: %v", err)
	}
	if _, exists := models["claude"]; exists {
		t.Fatal("non-OpenCode provider leaked into the metadata")
	}

	shared := models["shared"]
	if shared.ContextWindow != 1000 || shared.MaxOutput != 100 {
		t.Errorf("shared = ctx %d/%d, want the primary provider limits 1000/100", shared.ContextWindow, shared.MaxOutput)
	}
	if shared.Input == nil || *shared.Input != 1 {
		t.Errorf("shared input cost = %v, want 1", shared.Input)
	}

	zen := models["zen-only"]
	if zen.ContextWindow != 2000 || zen.MaxInput != 1500 || zen.MaxOutput != 200 {
		t.Errorf("zen-only = ctx %d, input %d, output %d; want 2000/1500/200", zen.ContextWindow, zen.MaxInput, zen.MaxOutput)
	}

	goOnly, exists := models["go-only"]
	if !exists {
		t.Fatal("go-only model missing; tiers must be merged instead of the first provider winning alone")
	}
	if goOnly.ContextWindow != 4000 || goOnly.MaxOutput != 400 {
		t.Errorf("go-only = ctx %d/%d, want 4000/400", goOnly.ContextWindow, goOnly.MaxOutput)
	}
	if models["go-only"].Output == nil || *models["go-only"].Output != 0 {
		t.Errorf("go-only output cost = %v, want zero output cost preserved", models["go-only"].Output)
	}
}

// OpenCode's capability catalog stays authoritative; models.dev only fills the
// limits it omitted so discovery clients still get per-model numbers.
func TestMetadataForTierFillsMissingLimitsFromModelsDev(t *testing.T) {
	catalog := NewCatalog(config.TierZen, nil)
	catalog.SetPricingStore(&PricingStore{models: map[string]Price{
		"alpha": {ID: "alpha", ContextWindow: 262144, MaxOutput: 32768},
		"beta":  {ID: "beta", ContextWindow: 500000, MaxInput: 400000, MaxOutput: 64000},
	}})
	catalog.ReplaceWithCapabilities(
		[]string{"alpha", "beta"},
		nil,
		map[config.Tier]map[string]wire.Protocol{config.TierZen: {}, config.TierGo: {}},
		nil,
		map[config.Tier]map[string]Metadata{config.TierZen: {
			"alpha": {ContextWindow: 1000000, MaxOutput: 128000},
			"beta":  {Reasoning: true},
		}},
	)

	alpha := catalog.MetadataForTier("alpha", config.TierZen)
	if alpha.ContextWindow != 1000000 || alpha.MaxOutput != 128000 {
		t.Errorf("alpha = ctx %d/%d, want the catalog values 1000000/128000", alpha.ContextWindow, alpha.MaxOutput)
	}

	beta := catalog.MetadataForTier("beta", config.TierZen)
	if beta.ContextWindow != 500000 || beta.MaxInput != 400000 || beta.MaxOutput != 64000 {
		t.Errorf("beta = ctx %d, input %d, output %d; want 500000/400000/64000", beta.ContextWindow, beta.MaxInput, beta.MaxOutput)
	}
	if !beta.Reasoning {
		t.Error("beta lost its catalog capability flags while limits were filled")
	}

	if unknown := catalog.MetadataForTier("gamma", config.TierZen); unknown.ContextWindow != 0 {
		t.Errorf("gamma = ctx %d, want zero for a model no source describes", unknown.ContextWindow)
	}
}

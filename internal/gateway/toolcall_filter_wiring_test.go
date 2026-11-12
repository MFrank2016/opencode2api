package gateway

import (
	"testing"

	"opencode2api/internal/config"
	wire "opencode2api/internal/protocol"
)

// The configured filter must reach the renames a shaped request carries;
// otherwise the reply-side drop never runs.
func TestApplyToolCallFilterFromConfig(t *testing.T) {
	g := &Gateway{cfg: config.Config{}}
	g.cfg.Performance.ToolCallFilter = "manifest-only"

	renames := wire.NewToolRenames()
	g.applyToolCallFilter(renames)
	renames.SetManifest([]string{"webfetch"})

	if !renames.HasFilter() {
		t.Fatal("filter was not applied")
	}
	if !renames.ShouldDrop("webfetch") {
		t.Fatal("manifest tool without a declared equivalent should be dropped")
	}
}

func TestApplyToolCallFilterDefaultsOff(t *testing.T) {
	g := &Gateway{cfg: config.Config{}}
	renames := wire.NewToolRenames()
	g.applyToolCallFilter(renames)
	renames.SetManifest([]string{"webfetch"})

	if renames.HasFilter() {
		t.Fatal("empty configuration must keep the filter off")
	}
	if renames.ShouldDrop("webfetch") {
		t.Fatal("filter off must keep every call")
	}
}

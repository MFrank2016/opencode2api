package models

import (
	"testing"

	"opencode2api/internal/config"
)

func TestModelAliasRoutesToTarget(t *testing.T) {
	c := catalogWithModels(map[string]int{"deepseek-v4.1-flash-free": 200000})
	c.SetAliases(map[string]string{"deepseek-v4-flash": "deepseek-v4.1-flash-free"})

	route, err := c.Route("deepseek-v4-flash", false, false, true)
	if err != nil {
		t.Fatalf("Route(alias) error = %v", err)
	}
	if route.ID != "deepseek-v4.1-flash-free" {
		t.Fatalf("route ID = %q, want the target model", route.ID)
	}
}

func TestModelAliasIgnoredWhenTargetMissing(t *testing.T) {
	c := catalogWithModels(map[string]int{"present-free": 200000})
	c.SetAliases(map[string]string{"ghost": "missing-free"})

	if _, err := c.Route("ghost", false, false, true); err == nil {
		t.Fatal("an alias to a missing model must not resolve")
	}
	if _, err := c.Route("present-free", false, false, true); err != nil {
		t.Fatalf("the real model must still resolve: %v", err)
	}
}

func TestModelAliasDoesNotChain(t *testing.T) {
	c := catalogWithModels(map[string]int{"final-free": 200000})
	c.SetAliases(map[string]string{"a": "b", "b": "final-free"})

	if _, err := c.Route("a", false, false, true); err == nil {
		t.Fatal("aliases must be single-level; a resolved to b, which is not a model")
	}
	if route, err := c.Route("b", false, false, true); err != nil || route.ID != "final-free" {
		t.Fatalf("b should resolve to final-free, got %+v err=%v", route, err)
	}
}

func TestModelAliasSelfAndEmptyIgnored(t *testing.T) {
	c := catalogWithModels(map[string]int{"m-free": 200000})
	c.SetAliases(map[string]string{"m-free": "m-free", "empty": ""})

	if route, err := c.Route("m-free", false, false, true); err != nil || route.ID != "m-free" {
		t.Fatalf("self alias should be ignored, got %+v err=%v", route, err)
	}
}

func TestModelAliasHonorsTierResolution(t *testing.T) {
	c := catalogWithModels(map[string]int{"real-free": 200000})
	c.SetAliases(map[string]string{"short": "real-free"})

	route, err := c.RouteForTier("short", config.TierZen, true, false)
	if err != nil {
		t.Fatalf("RouteForTier(alias) error = %v", err)
	}
	if route.ID != "real-free" {
		t.Fatalf("route ID = %q, want real-free", route.ID)
	}
}

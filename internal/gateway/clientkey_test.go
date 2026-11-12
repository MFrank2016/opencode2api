package gateway

import (
	"net/http/httptest"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
)

func TestUserAgentFamilyCollapsesVersions(t *testing.T) {
	a := userAgentFamily("opencode/2.0.32 (linux; x64)")
	b := userAgentFamily("opencode/2.1.4 (linux; x64)")
	if a != b {
		t.Fatalf("version change fragmented the family: %q vs %q", a, b)
	}
	if userAgentFamily("claude-cli/1.2.3") == a {
		t.Fatal("different agents collapsed to one family")
	}
	if got := userAgentFamily(""); got != "anon" {
		t.Fatalf("empty UA = %q, want anon", got)
	}
}

func TestClientIPHonorsTrustForwarded(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.RemoteAddr = "10.0.0.9:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	if got := clientIP(r, false); got != "10.0.0.9" {
		t.Fatalf("untrusted IP = %q, want the socket address", got)
	}
	if got := clientIP(r, true); got != "203.0.113.7" {
		t.Fatalf("trusted IP = %q, want the first forwarded entry", got)
	}
}

func poolGateway() *Gateway {
	g := &Gateway{cfg: config.Config{}}
	g.identityPool = identity.NewPool(identity.PoolOptions{MaxEntries: 8, TTL: time.Hour})
	return g
}

func TestDeriveRequestIDsUsesStableProjectPerClient(t *testing.T) {
	g := poolGateway()
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}

	request := func(ua, project string) string {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.RemoteAddr = "198.51.100.4:9000"
		r.Header.Set("User-Agent", ua)
		if project != "" {
			r.Header.Set("x-opencode-project", project)
		}
		return g.deriveRequestIDs(r, body, "scope").Project
	}

	first := request("opencode/2.0.32", "")
	if second := request("opencode/2.1.0", ""); second != first {
		t.Fatalf("same client bucket got two projects: %q vs %q", first, second)
	}
	if other := request("claude-cli/1.0.0", ""); other == first {
		t.Fatal("different user-agent families shared a project")
	}
	if named := request("opencode/2.0.32", "abc123"); named != "abc123" {
		t.Fatalf("caller-named project = %q, want abc123", named)
	}
}

func TestDeriveRequestIDsWithoutPoolKeepsRandomProject(t *testing.T) {
	g := &Gateway{cfg: config.Config{}}
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	request := func() string {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.RemoteAddr = "198.51.100.4:9000"
		r.Header.Set("User-Agent", "opencode/2.0.32")
		return g.deriveRequestIDs(r, body, "scope").Project
	}
	if request() == request() {
		t.Fatal("without a pool the project must stay per-request random")
	}
}

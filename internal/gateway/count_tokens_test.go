package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
)

func countTokensGateway() *Gateway {
	return &Gateway{
		cfg:    config.Config{ServerKeys: []string{"sk-test"}},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func postCountTokens(t *testing.T, g *Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.handleCountTokens(rec, req)
	return rec
}

func TestCountTokensReturnsPositiveEstimate(t *testing.T) {
	body := `{"model":"m","system":"You are a helpful assistant.","messages":[{"role":"user","content":"Hello there, how are you?"}]}`
	rec := postCountTokens(t, countTokensGateway(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", out.InputTokens)
	}
}

func TestCountTokensGrowsWithInput(t *testing.T) {
	short := `{"messages":[{"role":"user","content":"hi"}]}`
	long := `{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 400) + `"}]}`
	g := countTokensGateway()

	var a, b struct {
		InputTokens int `json:"input_tokens"`
	}
	json.Unmarshal(postCountTokens(t, g, short).Body.Bytes(), &a)
	json.Unmarshal(postCountTokens(t, g, long).Body.Bytes(), &b)
	if b.InputTokens <= a.InputTokens {
		t.Fatalf("longer input = %d, shorter = %d; estimate must grow", b.InputTokens, a.InputTokens)
	}
}

func TestCountTokensRejectsInvalidJSON(t *testing.T) {
	rec := postCountTokens(t, countTokensGateway(), `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCountTokensRouteRequiresServerKey(t *testing.T) {
	g := countTokensGateway()
	body := `{"messages":[{"role":"user","content":"hi"}]}`

	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, unauthorized)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key status = %d, want 401", rec.Code)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	authorized.Header.Set("Authorization", "Bearer sk-test")
	rec = httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, authorized)
	if rec.Code != http.StatusOK {
		t.Fatalf("with key status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

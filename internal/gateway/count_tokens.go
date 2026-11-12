package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	wire "opencode2api/internal/protocol"
)

// handleCountTokens answers Anthropic's token-count probe. There is no upstream
// tokenizer available, so the value is a local estimate: four characters per
// token plus the small per-message framing overhead. It is exact enough for the
// context budgeting Claude Code uses it for, and far better than a 404 for a
// client that probes it before every request.
func (g *Gateway) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		wire.WriteError(w, wire.Anthropic, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		wire.WriteError(w, wire.Anthropic, http.StatusBadRequest, "request body must be a JSON object", "invalid_request_error", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": estimateInputTokens(payload)})
}

// estimateInputTokens sums the text the request carries and converts it at the
// usual four-characters-per-token ratio. Tool definitions are counted by their
// JSON length, since they reach the upstream as schema text.
func estimateInputTokens(payload map[string]any) int {
	chars := 0
	var add func(any)
	add = func(value any) {
		switch typed := value.(type) {
		case string:
			chars += utf8.RuneCountInString(typed)
		case []any:
			for _, entry := range typed {
				add(entry)
			}
		case map[string]any:
			for _, key := range []string{"text", "content", "system"} {
				if nested, ok := typed[key]; ok {
					add(nested)
				}
			}
		}
	}
	if system, ok := payload["system"]; ok {
		add(system)
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, raw := range messages {
			if message, ok := raw.(map[string]any); ok {
				add(message["content"])
			}
			chars += 16 // role tag and message framing
		}
	}
	if tools, ok := payload["tools"]; ok {
		if encoded, err := json.Marshal(tools); err == nil {
			chars += len(encoded)
		}
	}
	tokens := chars / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

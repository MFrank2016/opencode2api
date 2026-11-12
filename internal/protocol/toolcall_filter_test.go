package protocol

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func filterRenames(mode ToolCallFilter, declared, manifest []string) *ToolRenames {
	renames := NewToolRenames()
	renames.Add("Read", ClientToolWireName("Read", map[string]bool{}))
	renames.SetDeclared(declared)
	renames.SetManifest(manifest)
	renames.SetFilter(mode)
	return renames
}

func clintRead() string {
	return ClientToolWireName("Read", map[string]bool{})
}

func chatBody(calls ...map[string]any) []byte {
	body := map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "tool_calls": anySlice(calls)},
			"finish_reason": "tool_calls",
		}},
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func anySlice(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func toolCall(name string) map[string]any {
	return map[string]any{
		"id":       "call_" + name,
		"type":     "function",
		"function": map[string]any{"name": name, "arguments": "{}"},
	}
}

func chatCallNames(t *testing.T, body []byte) ([]string, string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	var names []string
	if calls, ok := message["tool_calls"].([]any); ok {
		for _, raw := range calls {
			call := raw.(map[string]any)
			names = append(names, call["function"].(map[string]any)["name"].(string))
		}
	}
	finish, _ := choice["finish_reason"].(string)
	return names, finish
}

// A manifest tool the caller declared no equivalent for is withheld; a client
// tool is kept and restored to its declared name.
func TestFilterManifestOnlyDropsUnofferedOfficialCall(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch", "read"})
	body := chatBody(toolCall("webfetch"), toolCall(clintRead()))

	got, _ := chatCallNames(t, RenameResponseTool(body, Chat, renames))
	if len(got) != 1 || got[0] != "Read" {
		t.Fatalf("names = %v, want [Read]", got)
	}
}

// When every call is withheld, the choice must not claim it is waiting on tool
// calls the client will never receive.
func TestFilterDowngradesFinishReasonWhenAllCallsDropped(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, nil, []string{"webfetch"})
	body := chatBody(toolCall("webfetch"))

	names, finish := chatCallNames(t, RenameResponseTool(body, Chat, renames))
	if len(names) != 0 {
		t.Fatalf("names = %v, want none", names)
	}
	if finish != "stop" {
		t.Fatalf("finish_reason = %q, want stop", finish)
	}
}

// FilterOff preserves the historical behavior: every call is returned.
func TestFilterOffKeepsEveryCall(t *testing.T) {
	renames := filterRenames(FilterOff, []string{"Read"}, []string{"webfetch"})
	body := chatBody(toolCall("webfetch"), toolCall(clintRead()))

	names, _ := chatCallNames(t, RenameResponseTool(body, Chat, renames))
	if len(names) != 2 {
		t.Fatalf("names = %v, want two", names)
	}
}

// unoffered also drops a name the model invented, which manifest-only keeps.
func TestFilterUnofferedDropsInventedNames(t *testing.T) {
	manifestOnly := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch"})
	strict := filterRenames(FilterUnoffered, []string{"Read"}, []string{"webfetch"})
	body := chatBody(toolCall("client_nonce_lookup"))

	if names, _ := chatCallNames(t, RenameResponseTool(body, Chat, manifestOnly)); len(names) != 1 {
		t.Fatalf("manifest-only names = %v, want the invented call kept", names)
	}
	if names, _ := chatCallNames(t, RenameResponseTool(body, Chat, strict)); len(names) != 0 {
		t.Fatalf("unoffered names = %v, want the invented call dropped", names)
	}
}

func TestFilterResponsesDropsUnofferedOfficialCall(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch"})
	body, _ := json.Marshal(map[string]any{
		"output": []any{
			map[string]any{"type": "function_call", "name": "webfetch", "arguments": "{}"},
			map[string]any{"type": "function_call", "name": clintRead(), "arguments": "{}"},
		},
	})
	var payload map[string]any
	if err := json.Unmarshal(RenameResponseTool(body, Responses, renames), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	output := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output len = %d, want 1", len(output))
	}
	if name := output[0].(map[string]any)["name"]; name != "Read" {
		t.Fatalf("name = %v, want Read", name)
	}
}

func TestFilterAnthropicDropsUnofferedOfficialCall(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch"})
	body, _ := json.Marshal(map[string]any{
		"content": []any{
			map[string]any{"type": "tool_use", "name": "webfetch", "id": "t1"},
			map[string]any{"type": "tool_use", "name": clintRead(), "id": "t2"},
		},
	})
	var payload map[string]any
	if err := json.Unmarshal(RenameResponseTool(body, Anthropic, renames), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := payload["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	if name := content[0].(map[string]any)["name"]; name != "Read" {
		t.Fatalf("name = %v, want Read", name)
	}
}

func readStream(t *testing.T, input string, protocol Protocol, renames *ToolRenames) string {
	t.Helper()
	reader := RenameStreamReader(strings.NewReader(input), protocol, renames)
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return string(out)
}

func TestFilterChatStreamDropsUnofferedOfficialCall(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch"})
	drop := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"webfetch","arguments":"{}"}}]}}]}` + "\n\n"
	keep := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"` + clintRead() + `","arguments":"{}"}}]}}]}` + "\n\n"
	out := readStream(t, drop+keep+"data: [DONE]\n\n", Chat, renames)

	if strings.Contains(out, "webfetch") {
		t.Fatalf("dropped official call leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"Read"`) {
		t.Fatalf("kept client call was not restored: %s", out)
	}
}

func TestFilterAnthropicStreamDropsWholeBlock(t *testing.T) {
	renames := filterRenames(FilterManifestOnly, []string{"Read"}, []string{"webfetch"})
	input := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"webfetch"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t2","name":"` + clintRead() + `"}}`,
		`data: {"type":"content_block_stop","index":1}`,
	}, "\n\n") + "\n\n"
	out := readStream(t, input, Anthropic, renames)

	if strings.Contains(out, "webfetch") {
		t.Fatalf("dropped official block leaked: %s", out)
	}
	if strings.Contains(out, `"index":0`) {
		t.Fatalf("dropped block deltas leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"Read"`) {
		t.Fatalf("kept block was not restored: %s", out)
	}
}

package admin

import (
	"encoding/json"

	"opencode2api/internal/config"
)

// mergeConfigBlock applies a partial JSON object over a copy of the current
// block: keys the client omitted keep their current value. The configuration
// form does not render every field, and an older client (a cached page) must
// not zero the fields it does not know about. An absent or null block keeps the
// whole current value.
func mergeConfigBlock(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// mergeBlockInto is mergeConfigBlock with a typed current value.
func mergeBlockInto[T any](current T, raw json.RawMessage) (T, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return current, nil
	}
	out := current
	if err := json.Unmarshal(raw, &out); err != nil {
		return current, err
	}
	return out, nil
}

// mergeModelsBlock merges the models block field by field. Scalars keep the
// current value when omitted; maps and the nested backoff block are replaced
// when present, so deleting an entry in the editor still deletes it (an
// explicit null clears the map).
func mergeModelsBlock(raw json.RawMessage, current config.ModelsConfig) (config.ModelsConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return current, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return current, err
	}
	out := current
	for key, target := range map[string]any{
		"refresh_seconds": &out.RefreshSeconds,
		"free_only":       &out.FreeOnly,
		"active_probes":   &out.ActiveProbes,
	} {
		if value, ok := fields[key]; ok {
			if err := json.Unmarshal(value, target); err != nil {
				return current, err
			}
		}
	}
	replaceMap := func(key string, target any, clear func()) error {
		value, ok := fields[key]
		if !ok {
			return nil
		}
		clear()
		return json.Unmarshal(value, target)
	}
	if err := replaceMap("protocols", &out.Protocols, func() { out.Protocols = nil }); err != nil {
		return current, err
	}
	if err := replaceMap("context", &out.Context, func() { out.Context = nil }); err != nil {
		return current, err
	}
	if err := replaceMap("max_output", &out.MaxOutput, func() { out.MaxOutput = nil }); err != nil {
		return current, err
	}
	if err := replaceMap("aliases", &out.Aliases, func() { out.Aliases = nil }); err != nil {
		return current, err
	}
	if value, ok := fields["anonymous_backoff"]; ok {
		out.AnonymousBackoff = nil
		if string(value) != "null" {
			var backoff config.AnonymousBackoffConfig
			if err := json.Unmarshal(value, &backoff); err != nil {
				return current, err
			}
			out.AnonymousBackoff = &backoff
		}
	}
	return out, nil
}

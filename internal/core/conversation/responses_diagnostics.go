package conversation

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
)

// RequestDiagnostics contains hashes and sizes, not prompt text, credentials,
// encrypted reasoning or endpoint URLs. Input hashes preserve wire item order.
type RequestDiagnostics struct {
	Transport         string   `json:"transport"`
	Origin            string   `json:"origin"`
	RequestHash       string   `json:"request_hash"`
	SystemHash        string   `json:"system_hash"`
	ToolsHash         string   `json:"tools_hash"`
	SettingsHash      string   `json:"settings_hash"`
	InputHashes       []string `json:"input_hashes"`
	InputBytes        int      `json:"input_bytes"`
	RuntimeTailBytes  int      `json:"runtime_tail_bytes"`
	ReplayItems       int      `json:"replay_items"`
	CacheMode         string   `json:"cache_mode"`
	CacheTTL          string   `json:"cache_ttl,omitempty"`
	CacheFallback     bool     `json:"cache_fallback,omitempty"`
	ComparedWith      string   `json:"compared_with,omitempty"`
	CommonPrefixItems int      `json:"common_prefix_items"`
	FirstChangedItem  *int     `json:"first_changed_item,omitempty"`
}

func responsesRequestDiagnostics(params responses.ResponseNewParams, origin string, tailBytes, replayed int, explicit, fallback bool) *RequestDiagnostics {
	data, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	// NewStreaming adds stream:true outside ResponseNewParams.
	raw["stream"] = json.RawMessage(`true`)
	wire, _ := json.Marshal(raw)
	d := &RequestDiagnostics{
		Transport: "responses", Origin: responsesHash([]byte(origin)), RequestHash: responsesHash(wire),
		SystemHash: responsesHash(raw["instructions"]), ToolsHash: responsesHash(raw["tools"]),
		InputBytes: len(raw["input"]), RuntimeTailBytes: tailBytes, ReplayItems: replayed,
		CacheMode: "server_default", CacheFallback: fallback,
	}
	if explicit {
		d.CacheMode, d.CacheTTL = "explicit", "30m"
	}
	var items []json.RawMessage
	_ = json.Unmarshal(raw["input"], &items)
	for _, item := range items {
		d.InputHashes = append(d.InputHashes, responsesHash(item))
	}
	delete(raw, "input")
	delete(raw, "instructions")
	delete(raw, "tools")
	settings, _ := json.Marshal(raw)
	d.SettingsHash = responsesHash(settings)
	return d
}

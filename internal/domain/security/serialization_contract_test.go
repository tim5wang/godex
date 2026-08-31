package security

import (
	"encoding/json"
	"testing"
)

func TestSecurityPolicyJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"interactive_approval_enabled":true,"interactive_approval_mode":"ask","block_automation_mutations":true}`)
	var policy SecurityPolicy
	if err := json.Unmarshal(legacy, &policy); err != nil {
		t.Fatalf("decode legacy policy: %v", err)
	}
	if !policy.InteractiveApprovalEnabled || policy.InteractiveApprovalMode != "ask" || !policy.BlockAutomationMutations {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("encode policy: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode encoded policy: %v", err)
	}
	if _, ok := object["interactive_approval_enabled"]; !ok {
		t.Fatalf("stable approval key missing: %s", encoded)
	}
	if _, ok := object["block_automation_mutations"]; !ok {
		t.Fatalf("stable automation key missing: %s", encoded)
	}
}

func TestSecurityEventJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"id":"event-1","at":"2026-08-31T00:00:00Z","category":"tool","action":"deny"}`)
	var event SecurityEvent
	if err := json.Unmarshal(legacy, &event); err != nil {
		t.Fatalf("decode legacy event: %v", err)
	}
	if event.ID != "event-1" || event.Category != "tool" || event.Action != "deny" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

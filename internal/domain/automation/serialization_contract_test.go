package automation

import (
	"encoding/json"
	"testing"
)

func TestCronJobJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"id":"job-1","message":"run","schedule":{"type":"every","every_seconds":60},"enabled":true}`)
	var job CronJob
	if err := json.Unmarshal(legacy, &job); err != nil {
		t.Fatalf("decode legacy cron job: %v", err)
	}
	if job.ID != "job-1" || job.Schedule.EverySeconds != 60 || job.ModelProfileID != "" {
		t.Fatalf("unexpected legacy decode: %#v", job)
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("encode cron job: %v", err)
	}
	assertJSONKeys(t, encoded, "id", "message", "schedule", "enabled")
}

func TestHeartbeatRuleJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"id":"heartbeat","enabled":true,"interval_seconds":30}`)
	var rule HeartbeatRule
	if err := json.Unmarshal(legacy, &rule); err != nil {
		t.Fatalf("decode legacy heartbeat rule: %v", err)
	}
	if rule.ID != "heartbeat" || rule.IntervalSeconds != 30 || !rule.DeliveryTarget.IsZero() {
		t.Fatalf("unexpected legacy decode: %#v", rule)
	}
}

func assertJSONKeys(t *testing.T, encoded []byte, keys ...string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode encoded object: %v", err)
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Fatalf("missing stable JSON key %q in %s", key, encoded)
		}
	}
}

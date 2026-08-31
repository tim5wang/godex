package eval

import (
	"encoding/json"
	"testing"
)

func TestCaseJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"id":"case-1","prompt":"say hello","expected":{"required_substrings":["hello"]}}`)
	var testCase Case
	if err := json.Unmarshal(legacy, &testCase); err != nil {
		t.Fatalf("decode legacy case: %v", err)
	}
	if testCase.ID != "case-1" || len(testCase.Expected.RequiredSubstrings) != 1 {
		t.Fatalf("unexpected case: %#v", testCase)
	}
	encoded, err := json.Marshal(testCase)
	if err != nil {
		t.Fatalf("encode case: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode encoded case: %v", err)
	}
	for _, key := range []string{"id", "prompt", "expected"} {
		if _, ok := object[key]; !ok {
			t.Fatalf("missing stable key %q in %s", key, encoded)
		}
	}
}

func TestReportJSONCompatibility(t *testing.T) {
	legacy := []byte(`{"run_id":"run-1","suite_name":"smoke","passed":true,"total_cases":1,"passed_cases":1,"failed_cases":0,"results":[]}`)
	var report Report
	if err := json.Unmarshal(legacy, &report); err != nil {
		t.Fatalf("decode legacy report: %v", err)
	}
	if report.RunID != "run-1" || report.SuiteName != "smoke" || !report.Passed {
		t.Fatalf("unexpected report: %#v", report)
	}
}

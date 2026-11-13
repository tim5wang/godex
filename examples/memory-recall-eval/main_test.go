package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestRecallEvaluationKeepsQualityAndIsolation(t *testing.T) {
	baselineRaw, err := os.ReadFile("testdata/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline report
	if err = json.Unmarshal(baselineRaw, &baseline); err != nil {
		t.Fatal(err)
	}
	current, err := evaluate("regression", corpusJSON)
	if err != nil {
		t.Fatal(err)
	}
	if current.CorpusSHA != baseline.CorpusSHA {
		t.Fatal("corpus changed: freeze and regenerate a comparable baseline before changing evaluation labels")
	}
	for _, engine := range []string{"desktop", "personal"} {
		for _, split := range []string{"all", "holdout"} {
			m := current.Metrics[engine+"/"+split]
			if m.Precision < .7 || m.Recall < 1 || m.Forbidden != 0 {
				t.Errorf("%s/%s retrieval quality/isolation regressed: %+v", engine, split, m)
			}
		}
	}
}

func TestMultiTurnTaskRecallAndAbstention(t *testing.T) {
	raw, err := os.ReadFile("testdata/multiturn-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	after, err := evaluate("task-aware", raw)
	if err != nil {
		t.Fatal(err)
	}
	before, err := evaluateOptions("literal", raw, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"desktop", "personal"} {
		for _, split := range []string{"all", "holdout"} {
			m := after.Metrics[engine+"/"+split]
			if m.Precision != 1 || m.Recall != 1 || m.NoResultAccuracy != 1 || m.Forbidden != 0 {
				t.Errorf("%s/%s task recall regressed: %+v", engine, split, m)
			}
		}
		if before.Metrics[engine+"/all"].Recall >= after.Metrics[engine+"/all"].Recall {
			t.Fatal("task planner failed to improve continuation recall")
		}
	}
}

func TestIntentFacetAndDuplicateRecallPreservesDistinctEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/intent-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluate("intent", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"desktop", "personal"} {
		m := report.Metrics[engine+"/all"]
		if m.Precision < .9 || m.Recall != 1 || m.NoResultAccuracy != 1 || m.Forbidden != 0 {
			t.Fatal(engine, m)
		}
	}
}

func TestEquivalentLabelsCannotMaskScopeOrLifecycleLeaks(t *testing.T) {
	for _, status := range []string{"confirmed", "archived"} {
		raw, _ := json.Marshal(corpus{Records: []record{
			{ID: "a", Status: "confirmed", Desktop: "primary"},
			{ID: "b", Status: status, Desktop: "other", EquivalentTo: "a"},
		}})
		if _, err := evaluate("invalid", raw); err == nil {
			t.Fatal("equivalence masked eligibility")
		}
	}
}

func TestEntityCatalogEvaluationAndAblation(t *testing.T) {
	raw, err := os.ReadFile("testdata/entity-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := evaluateWithCatalog("ablation", raw, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := evaluateWithCatalog("catalog", raw, true)
	if err != nil {
		t.Fatal(err)
	}
	b, a := before.Metrics["desktop/all"], after.Metrics["desktop/all"]
	if a.Recall != 1 || a.Precision != 1 || a.NoResultAccuracy != 1 || a.Forbidden != 0 || a.Recall <= b.Recall || a.Precision < b.Precision {
		t.Fatalf("catalog quality/isolation regression: before=%+v after=%+v", b, a)
	}
	for _, row := range after.Results["desktop"] {
		if !reflect.DeepEqual(row.Retrieved, row.Expected) {
			t.Errorf("unexpected entity expansion: query=%q want=%v got=%v", row.Query, row.Expected, row.Retrieved)
		}
	}
}

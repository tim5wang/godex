package codexnative

import (
	"encoding/json"
	"errors"
	"testing"
)

type modelPage struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func testModelCatalog() []Model {
	return []Model{
		{ID: "catalog-id-a", Model: "model-a", DisplayName: "Model A", DefaultReasoningEffort: "high", SupportedReasoningEfforts: []ReasoningEffortOption{{ReasoningEffort: "low"}, {ReasoningEffort: "high"}}},
		{ID: "catalog-id-b", Model: "model-b", DisplayName: "Model B", DefaultReasoningEffort: "medium", SupportedReasoningEfforts: []ReasoningEffortOption{{ReasoningEffort: "medium"}, {ReasoningEffort: "ultra"}}},
	}
}

func TestModelsUsesScopedProviderAndEveryPageWithoutResume(t *testing.T) {
	f, s := newFakeCodex(t)
	f.notLoaded = true // History-only discovery must not acquire a writer.
	next := "next"
	f.modelPages = map[string]modelPage{"": {Data: testModelCatalog()[:1], NextCursor: &next}, next: {Data: append(testModelCatalog()[1:], Model{Model: "hidden", Hidden: true})}}
	models, err := s.Models(testContext(t), "thread")
	if err != nil || len(models) != 2 || models[1].Model != "model-b" {
		t.Fatalf("%+v %v", models, err)
	}
	if f.count("model/list") != 2 || f.count("thread/resume") != 0 || f.count("thread/settings/update") != 0 {
		t.Fatal("catalog did not preserve read-only native pagination")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "model/list" {
			var params map[string]any
			_ = json.Unmarshal(call.Params, &params)
			if params["modelProvider"] != "native-provider" || params["includeHidden"] != false {
				t.Fatal(string(call.Params))
			}
		}
	}
}

func TestModelSettingsOnlyChangeExplicitThreadModelAndEffort(t *testing.T) {
	f, s := newFakeCodex(t)
	f.active = true // Changes are for subsequent turns, even while native work runs.
	thread, err := s.UpdateModelSettings(testContext(t), "thread", ModelSettings{Model: "model-b", Effort: "ultra"})
	if err != nil || thread.Model != "model-b" || thread.ReasoningEffort != "ultra" {
		t.Fatalf("%+v %v", thread, err)
	}
	if f.count("thread/settings/update") != 1 {
		t.Fatal("explicit settings were not applied exactly once")
	}
	if f.count("thread/resume") != 0 || f.count("turn/start") != 0 || f.count("turn/interrupt") != 0 {
		t.Fatal("settings changed native execution")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "thread/settings/update" {
			var params map[string]string
			_ = json.Unmarshal(call.Params, &params)
			if len(params) != 3 || params["threadId"] != "thread" || params["model"] != "model-b" || params["effort"] != "ultra" {
				t.Fatal(string(call.Params))
			}
		}
	}
}

func TestModelWithoutReasoningDoesNotSendInventedEffort(t *testing.T) {
	f, s := newFakeCodex(t)
	f.modelPages = map[string]modelPage{"": {Data: []Model{{ID: "simple-id", Model: "simple-model"}}}}
	if _, err := s.UpdateModelSettings(testContext(t), "thread", ModelSettings{Model: "simple-model"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "thread/settings/update" {
			var params map[string]any
			_ = json.Unmarshal(call.Params, &params)
			if len(params) != 2 || params["model"] != "simple-model" {
				t.Fatal(string(call.Params))
			}
		}
	}
}

func TestModelSettingsRejectUnsupportedSelections(t *testing.T) {
	for _, settings := range []ModelSettings{{}, {Model: "arbitrary", Effort: "high"}, {Model: "model-a", Effort: "ultra"}, {Model: "model-a"}} {
		f, s := newFakeCodex(t)
		if _, err := s.UpdateModelSettings(testContext(t), "thread", settings); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", settings, err)
		}
		if f.count("thread/settings/update") != 0 {
			t.Fatal("invalid settings reached native runtime")
		}
	}
}

func TestModelSettingsRespectScopeAndLoadedInputCapability(t *testing.T) {
	for _, state := range []string{"outside", "unloaded", "read-only"} {
		t.Run(state, func(t *testing.T) {
			f, s := newFakeCodex(t)
			id := "thread"
			want := ErrConflict
			switch state {
			case "outside":
				id = "outside"
				want = ErrForbidden
			case "unloaded":
				f.notLoaded = true
			case "read-only":
				f.noDirectInput = true
			}
			if _, err := s.UpdateModelSettings(testContext(t), id, ModelSettings{Model: "model-a", Effort: "low"}); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if f.count("model/list") != 0 || f.count("thread/settings/update") != 0 || f.count("thread/resume") != 0 {
				t.Fatal("denied update accessed catalog or mutated runtime")
			}
			if state == "outside" {
				if _, err := s.Models(testContext(t), id); !errors.Is(err, ErrForbidden) || f.count("model/list") != 0 {
					t.Fatalf("outside catalog: %v", err)
				}
			}
		})
	}
}

func TestModelCatalogAndUnsupportedSettingsFailuresDoNotFallBack(t *testing.T) {
	for _, stage := range []string{"catalog", "settings", "repeated-cursor"} {
		t.Run(stage, func(t *testing.T) {
			f, s := newFakeCodex(t)
			switch stage {
			case "catalog":
				f.modelError = &RPCError{Code: -32601, Message: "unsupported model/list"}
			case "settings":
				f.settingsError = &RPCError{Code: -32601, Message: "unsupported thread/settings/update"}
			case "repeated-cursor":
				next := "again"
				page := modelPage{Data: testModelCatalog(), NextCursor: &next}
				f.modelPages = map[string]modelPage{"": page, next: page}
			}
			if _, err := s.UpdateModelSettings(testContext(t), "thread", ModelSettings{Model: "model-a", Effort: "low"}); err == nil {
				t.Fatal("runtime failure hidden")
			}
			if f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
				t.Fatal("unsafe settings fallback")
			}
			if stage != "settings" && f.count("thread/settings/update") != 0 {
				t.Fatal("failed validation still applied settings")
			}
		})
	}
}

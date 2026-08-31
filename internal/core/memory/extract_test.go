package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
)

func TestExtractorCapturesChinesePreferenceAndDedupes(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())

	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "好的，我之后会使用中文回复。"),
	}

	added, err := extractor.Capture(messages)
	if err != nil {
		t.Fatalf("capture candidates: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 new candidate, got %+v", added)
	}
	if added[0].Type != TypeUser {
		t.Fatalf("expected user memory candidate, got %+v", added[0])
	}

	again, err := extractor.Capture(messages)
	if err != nil {
		t.Fatalf("capture candidates again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected duplicate capture to be ignored, got %+v", again)
	}

	stored, err := LoadCandidates(extractor.SuggestionsPath())
	if err != nil {
		t.Fatalf("load stored candidates: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored candidate, got %+v", stored)
	}
}

func TestExtractorSkipsSuggestionsAlreadyPersistedAsMemory(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	if _, err := manager.Remember(SaveInput{
		Title:   "Workflow: Validate Go changes",
		Summary: "Run go test ./... after Go changes.",
		Content: "Persisted already.",
		Type:    TypeWorkflow,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	extractor := NewExtractor(manager, t.TempDir())
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "修一下 runtime。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "Run go test ./... after Go changes."),
	}

	added, err := extractor.Capture(messages)
	if err != nil {
		t.Fatalf("capture candidates: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected persisted memory title to suppress new candidate, got %+v", added)
	}
}

func TestExtractorMigratesLegacyTempCandidatesIntoInbox(t *testing.T) {
	base := t.TempDir()
	manager := NewManager(filepath.Join(base, "memory"))
	extractor := NewExtractor(manager, filepath.Join(base, "tmp"))
	if err := os.MkdirAll(filepath.Join(base, "tmp"), 0755); err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}

	legacy := []Candidate{newCandidate(
		"User Preference: Reply in Chinese",
		"Prefer Chinese responses in future sessions.",
		"以后请用中文回复。",
		TypeUser,
		"turn-end-extractor",
	)}
	if err := os.WriteFile(filepath.Join(base, "tmp", legacyCandidatesFileName), []byte(`[
  {
    "fingerprint": "`+legacy[0].Fingerprint+`",
    "title": "`+legacy[0].Title+`",
    "summary": "`+legacy[0].Summary+`",
    "content": "`+legacy[0].Content+`",
    "memory_type": "`+string(legacy[0].Type)+`",
    "source": "`+legacy[0].Source+`"
  }
]
`), 0644); err != nil {
		t.Fatalf("write legacy candidate file: %v", err)
	}

	added, err := extractor.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "好的，我之后会使用中文回复。"),
	})
	if err != nil {
		t.Fatalf("capture candidates: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected migrated legacy candidate to dedupe new capture, got %+v", added)
	}

	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list inbox candidates: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one migrated inbox candidate, got %+v", stored)
	}
	if _, err := os.Stat(filepath.Join(base, "tmp", legacyCandidatesFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected legacy temp candidates to be removed, got %v", err)
	}
}

func TestAcceptAndDismissCandidate(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, "")
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "好的，我之后会使用中文回复。"),
	}
	added, err := extractor.Capture(messages)
	if err != nil {
		t.Fatalf("capture candidates: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected one new candidate, got %+v", added)
	}

	entry, err := manager.AcceptCandidate(added[0].Fingerprint)
	if err != nil {
		t.Fatalf("accept candidate: %v", err)
	}
	if entry.Title != added[0].Title {
		t.Fatalf("expected accepted entry title %q, got %+v", added[0].Title, entry)
	}
	remaining, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates after accept: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected inbox to be empty after accept, got %+v", remaining)
	}

	added, err = extractor.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "修一下 runtime。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "Run go test ./... after Go changes."),
	})
	if err != nil {
		t.Fatalf("capture second candidates: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected second candidate, got %+v", added)
	}
	dismissed, err := manager.DismissCandidate(added[0].Fingerprint)
	if err != nil {
		t.Fatalf("dismiss candidate: %v", err)
	}
	if dismissed.Title != added[0].Title {
		t.Fatalf("expected dismissed candidate %+v, got %+v", added[0], dismissed)
	}
	remaining, err = manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates after dismiss: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected inbox to stay empty after dismiss, got %+v", remaining)
	}
}

func TestAcceptCandidateWithAlwaysInclude(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, "")
	added, err := extractor.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "发布前记得先跑回归。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "Run go test ./... after Go changes."),
	})
	if err != nil {
		t.Fatalf("capture candidates: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected one candidate, got %+v", added)
	}

	entry, err := manager.AcceptCandidateWithOptions(AcceptCandidateInput{
		Fingerprint:   added[0].Fingerprint,
		AlwaysInclude: true,
	})
	if err != nil {
		t.Fatalf("accept candidate with always include: %v", err)
	}
	if !hasTag(entry.Tags, "core") {
		t.Fatalf("expected accepted candidate to gain core tag, got %+v", entry.Tags)
	}
}

func TestDismissCandidateSuppressesImmediateRecapture(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())

	first, err := extractor.CaptureInsightsReport(&insights.Report{
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
		},
	})
	if err != nil {
		t.Fatalf("capture initial report: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected one initial candidate, got %+v", first)
	}

	if _, err := manager.DismissCandidate(first[0].Fingerprint); err != nil {
		t.Fatalf("dismiss candidate: %v", err)
	}

	again, err := extractor.CaptureInsightsReport(&insights.Report{
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
		},
	})
	if err != nil {
		t.Fatalf("capture repeated report: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected dismissed candidate to stay suppressed, got %+v", again)
	}

	remaining, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates after suppression: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected inbox to remain empty after repeated report, got %+v", remaining)
	}
}

func TestExtractorDedupesSemanticallyEquivalentCandidatesAgainstDurableMemory(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	if _, err := manager.Remember(SaveInput{
		Title:   "Runtime Timeout Policy",
		Summary: "Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
		Content: "Treat model/API timeouts as a first-class runtime friction.",
		Type:    TypeWarning,
		Source:  "manual",
	}); err != nil {
		t.Fatalf("seed durable memory: %v", err)
	}

	extractor := NewExtractor(manager, t.TempDir())
	added, err := extractor.CaptureInsightsReport(&insights.Report{
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
		},
	})
	if err != nil {
		t.Fatalf("capture insights report: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected semantic duplicate to be suppressed, got %+v", added)
	}
}

func TestExtractorCapturesInsightsReportCandidates(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())

	added, err := extractor.CaptureInsightsReport(&insights.Report{
		AgentMDAdditions: []string{
			"Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: Prefer concise Chinese responses.",
			"Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: Run go test ./... after runtime changes.",
		},
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
		},
	})
	if err != nil {
		t.Fatalf("capture insights report: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("expected three candidates from insights report, got %+v", added)
	}
	if added[0].Type != TypeUser || added[1].Type != TypeWorkflow || added[2].Type != TypeWarning {
		t.Fatalf("unexpected candidate types from insights report: %+v", added)
	}
}

func TestExtractorCapsLargeInsightsReportBatches(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())

	added, err := extractor.CaptureInsightsReport(&insights.Report{
		AgentMDAdditions: []string{
			"Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: Prefer concise Chinese responses.",
			"Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: Run go test ./... after runtime changes.",
			"Consider promoting this durable project note into `AGENT.md`: Keep channel attachment handling explicit.",
		},
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
			"Path resolution and file existence checks remain a recurring source of errors.",
			"Progressive tool loading is discoverable but still produces inactive-tool friction in practice.",
		},
	})
	if err != nil {
		t.Fatalf("capture insights report: %v", err)
	}
	if len(added) != maxInsightBridgeAdds {
		t.Fatalf("expected insights bridge to cap candidates at %d, got %+v", maxInsightBridgeAdds, added)
	}
}

func TestExtractorCapturesRecurringTimelineFrictions(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	extractor := NewExtractor(manager, t.TempDir())

	added, err := extractor.CaptureTimeline([]events.Event{
		{Type: events.EventWarningRaised, Payload: events.NoticePayload{Message: "context deadline exceeded while awaiting headers"}},
		{Type: events.EventErrorRaised, Payload: events.NoticePayload{Message: "context deadline exceeded on upstream model call"}},
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "read_file", Error: "no such file or directory"}},
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "bash", Error: "no such file or directory"}},
	})
	if err != nil {
		t.Fatalf("capture timeline: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("expected two recurring timeline friction candidates, got %+v", added)
	}

	titles := map[string]struct{}{}
	for _, candidate := range added {
		titles[candidate.Title] = struct{}{}
	}
	foundTimeout := false
	foundPath := false
	for title := range titles {
		if strings.Contains(title, "Model/API timeouts") {
			foundTimeout = true
		}
		if strings.Contains(title, "Path resolution and file existence checks") {
			foundPath = true
		}
	}
	if !foundTimeout {
		t.Fatalf("expected timeout warning candidate, got %+v", added)
	}
	if !foundPath {
		t.Fatalf("expected path warning candidate, got %+v", added)
	}
}

func TestExtractorCaptureProjectDocs(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	manager := NewManager(filepath.Join(base, "memory"))
	extractor := NewExtractor(manager, filepath.Join(base, "tmp"))
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(`# GoDex

GoDex is a shared backend workspace for Web, TUI, and IM channels.

It centralizes session management and shared tooling.
`), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(`# Delivery Workflow

Always run go test ./... before wrapping up runtime changes.

Keep channel and runtime regressions visible.
`), 0644); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "runtime.md"), []byte(`# Runtime Notes

The runtime coordinates background tasks, approvals, and event delivery.

Use the snapshot timeline to inspect state transitions.
`), 0644); err != nil {
		t.Fatalf("write docs note: %v", err)
	}

	added, err := extractor.CaptureProjectDocs(workspace)
	if err != nil {
		t.Fatalf("capture project docs: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("expected three mined candidates, got %+v", added)
	}

	bySource := make(map[string]Candidate, len(added))
	for _, candidate := range added {
		bySource[candidate.Source] = candidate
	}
	if bySource["project-miner:readme"].Type != TypeProject {
		t.Fatalf("expected readme candidate to be project memory, got %+v", bySource["project-miner:readme"])
	}
	if bySource["project-miner:agents"].Type != TypeWorkflow {
		t.Fatalf("expected AGENTS candidate to be workflow memory, got %+v", bySource["project-miner:agents"])
	}
	if bySource["project-miner:docs"].Type != TypeProject {
		t.Fatalf("expected docs candidate to be project memory, got %+v", bySource["project-miner:docs"])
	}
	if !strings.Contains(bySource["project-miner:readme"].Content, "README.md") {
		t.Fatalf("expected mined content to include source path, got %+v", bySource["project-miner:readme"])
	}
}

func TestExtractorProjectDocsRespectsDismissSuppression(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	manager := NewManager(filepath.Join(base, "memory"))
	extractor := NewExtractor(manager, filepath.Join(base, "tmp"))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(`# GoDex

GoDex is a shared backend workspace for Web, TUI, and IM channels.

It centralizes session management and shared tooling.
`), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	first, err := extractor.CaptureProjectDocs(workspace)
	if err != nil {
		t.Fatalf("first capture project docs: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected one mined candidate, got %+v", first)
	}
	if _, err := manager.DismissCandidate(first[0].Fingerprint); err != nil {
		t.Fatalf("dismiss mined candidate: %v", err)
	}

	again, err := extractor.CaptureProjectDocs(workspace)
	if err != nil {
		t.Fatalf("recapture project docs: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected dismissed mined candidate to stay suppressed, got %+v", again)
	}
}

// TestAcceptWorkMethodAndWorkFactCandidates is a regression test for the
// work_method/work_fact save gap: project-docs mining produces candidates of
// these types, so accepting them must succeed (validateSaveInput must not
// reject the types the rest of the system advertises).
func TestAcceptWorkMethodAndWorkFactCandidates(t *testing.T) {
	for _, typ := range []Type{TypeWorkMethod, TypeWorkFact} {
		manager := NewManager(filepath.Join(t.TempDir(), "memory"))
		extractor := NewExtractor(manager, t.TempDir())

		added, err := extractor.captureCandidates([]Candidate{
			newCandidate(
				"Deploy Recipe "+string(typ),
				"How to deploy the service.",
				"1. build 2. upload 3. restart",
				typ,
				"project-docs",
			),
		})
		if err != nil {
			t.Fatalf("capture %s candidate: %v", typ, err)
		}
		if len(added) != 1 {
			t.Fatalf("expected one %s candidate, got %+v", typ, added)
		}

		entry, err := manager.AcceptCandidate(added[0].Fingerprint)
		if err != nil {
			t.Fatalf("accept %s candidate: %v", typ, err)
		}
		if entry.Type != typ {
			t.Fatalf("expected accepted type %s, got %q", typ, entry.Type)
		}

		// Update must also accept the type.
		if _, err := manager.Update(UpdateInput{
			Match:   ForgetInput{Title: entry.Title},
			Title:   entry.Title,
			Summary: "Updated summary",
			Content: "Updated content",
			Type:    typ,
		}); err != nil {
			t.Fatalf("update %s memory: %v", typ, err)
		}
	}
}

package workerruntime

import (
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

func TestJobRequestCloneIsDeepCopy(t *testing.T) {
	req := JobRequest{
		JobID:        " job-1 ",
		WorkerID:     " worker:godex:local ",
		SessionID:    " session-1 ",
		ParentTurnID: " turn-1 ",
		AgentType:    " general-purpose ",
		Prompt:       " inspect repo ",
		BasePrompt:   " base ",
		RuntimeContext: automation.SessionContext{
			SessionID: "session-1",
			Source:    "web",
			Metadata:  map[string]string{"channel": "test"},
		},
		Capabilities: CapabilitySet{
			ToolNames:       []string{"bash", "read_file"},
			RequiredBundles: []string{"web"},
			RequiredTools:   []string{"web_search"},
			DefaultBundles:  []string{"core_code"},
			ToolPolicy:      []string{"shell:allow=go test"},
			WriteScope:      []string{"internal/agent"},
			SandboxID:       " sandbox:local:abc ",
		},
		PreviewJobIDs: []string{"job-preview"},
		Display:       map[string]string{"icon": "test"},
		MaxTurns:      12,
		JobTimeoutMS:  5000,
	}

	cloned := req.Clone()
	cloned.Capabilities.ToolNames[0] = "changed"
	cloned.Capabilities.RequiredBundles[0] = "changed"
	cloned.Capabilities.RequiredTools[0] = "changed"
	cloned.Capabilities.DefaultBundles[0] = "changed"
	cloned.Capabilities.ToolPolicy[0] = "changed"
	cloned.Capabilities.WriteScope[0] = "changed"
	cloned.PreviewJobIDs[0] = "changed"
	cloned.RuntimeContext.Metadata["channel"] = "changed"
	cloned.Display["icon"] = "changed"

	if cloned.JobID != "job-1" || cloned.WorkerID != "worker:godex:local" || cloned.SessionID != "session-1" {
		t.Fatalf("string fields were not trimmed: %+v", cloned)
	}
	if cloned.Capabilities.SandboxID != "sandbox:local:abc" {
		t.Fatalf("sandbox id was not trimmed: %q", cloned.Capabilities.SandboxID)
	}
	if req.Capabilities.ToolNames[0] != "bash" {
		t.Fatalf("tool names mutated: %#v", req.Capabilities.ToolNames)
	}
	if req.Capabilities.RequiredBundles[0] != "web" {
		t.Fatalf("required bundles mutated: %#v", req.Capabilities.RequiredBundles)
	}
	if req.Capabilities.RequiredTools[0] != "web_search" {
		t.Fatalf("required tools mutated: %#v", req.Capabilities.RequiredTools)
	}
	if req.Capabilities.DefaultBundles[0] != "core_code" {
		t.Fatalf("default bundles mutated: %#v", req.Capabilities.DefaultBundles)
	}
	if req.Capabilities.ToolPolicy[0] != "shell:allow=go test" {
		t.Fatalf("tool policy mutated: %#v", req.Capabilities.ToolPolicy)
	}
	if req.Capabilities.WriteScope[0] != "internal/agent" {
		t.Fatalf("write scope mutated: %#v", req.Capabilities.WriteScope)
	}
	if req.PreviewJobIDs[0] != "job-preview" {
		t.Fatalf("preview jobs mutated: %#v", req.PreviewJobIDs)
	}
	if req.RuntimeContext.Metadata["channel"] != "test" {
		t.Fatalf("runtime context mutated: %#v", req.RuntimeContext.Metadata)
	}
	if req.Display["icon"] != "test" {
		t.Fatalf("display mutated: %#v", req.Display)
	}
}

func TestStatusTerminal(t *testing.T) {
	for _, status := range []Status{StatusCompleted, StatusCanceled, StatusInterrupted, StatusTimeout, StatusError} {
		if !status.Terminal() {
			t.Fatalf("expected %s to be terminal", status)
		}
	}
	for _, status := range []Status{StatusPending, StatusPendingApproval, StatusRunning} {
		if status.Terminal() {
			t.Fatalf("expected %s to be non-terminal", status)
		}
	}
}

func TestArtifactRefNormalizeTrimsFields(t *testing.T) {
	ref := ArtifactRef{
		ID:        " artifact-1 ",
		Path:      " /tmp/out.txt ",
		Kind:      " file ",
		MIMEType:  " text/plain ",
		Producer:  " job-1 ",
		WorkerID:  " worker:godex:local ",
		JobID:     " job-1 ",
		SandboxID: " sandbox:local:abc ",
		CreatedAt: time.Now(),
	}

	normalized := ref.Normalize()
	if normalized.ID != "artifact-1" || normalized.Path != "/tmp/out.txt" || normalized.Kind != "file" || normalized.MIMEType != "text/plain" || normalized.Producer != "job-1" || normalized.WorkerID != "worker:godex:local" || normalized.JobID != "job-1" || normalized.SandboxID != "sandbox:local:abc" {
		t.Fatalf("unexpected normalized artifact: %+v", normalized)
	}
}

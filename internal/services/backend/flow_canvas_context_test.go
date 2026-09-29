package backend

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/message"
)

func TestTakeFlowCanvasSnapshotForDesigner(t *testing.T) {
	session := &sessionState{
		locator: SessionLocator{
			Metadata: map[string]string{"template": flowDesignerTemplateID},
		},
	}
	envelope := message.Envelope{
		Metadata: map[string]string{
			flowCanvasSnapshotMetadataKey: `{"flow_id":"fl_demo","nodes":[],"edges":[]}`,
			"client_message_id":           "m-1",
		},
	}

	got, feedback, err := takeFlowCanvasSnapshot(session, envelope)
	if err != nil {
		t.Fatalf("take snapshot: %v", err)
	}
	if got.Metadata[flowCanvasSnapshotMetadataKey] != "" {
		t.Fatalf("snapshot metadata must be removed from the user envelope: %+v", got.Metadata)
	}
	if got.Metadata["client_message_id"] != "m-1" {
		t.Fatalf("unrelated metadata must be preserved: %+v", got.Metadata)
	}
	if !strings.HasPrefix(feedback, flowCanvasSnapshotFeedbackPrefix) ||
		!strings.Contains(feedback, `"flow_id":"fl_demo"`) {
		t.Fatalf("expected model feedback to include the supplied canvas, got %q", feedback)
	}
}

func TestTakeFlowCanvasSnapshotStripsButDoesNotInjectOutsideDesigner(t *testing.T) {
	envelope := message.Envelope{
		Metadata: map[string]string{flowCanvasSnapshotMetadataKey: `{"flow_id":"fl_demo"}`},
	}
	got, feedback, err := takeFlowCanvasSnapshot(&sessionState{}, envelope)
	if err != nil {
		t.Fatalf("take snapshot: %v", err)
	}
	if len(got.Metadata) != 0 {
		t.Fatalf("UI-only snapshot metadata should be stripped: %+v", got.Metadata)
	}
	if feedback != "" {
		t.Fatalf("non-designer sessions must not receive canvas context, got %q", feedback)
	}
}

func TestTakeFlowCanvasSnapshotStripsEmptyMetadata(t *testing.T) {
	got, feedback, err := takeFlowCanvasSnapshot(nil, message.Envelope{
		Metadata: map[string]string{flowCanvasSnapshotMetadataKey: "  "},
	})
	if err != nil {
		t.Fatalf("take empty snapshot: %v", err)
	}
	if len(got.Metadata) != 0 || feedback != "" {
		t.Fatalf("empty UI-only metadata should be removed without feedback: metadata=%+v feedback=%q", got.Metadata, feedback)
	}
}

func TestTakeFlowCanvasSnapshotRejectsInvalidAndOversizedInput(t *testing.T) {
	session := &sessionState{
		locator: SessionLocator{
			Metadata: map[string]string{"template": flowDesignerTemplateID},
		},
	}
	for name, raw := range map[string]string{
		"invalid JSON": `{`,
		"oversized":    `{"data":"` + strings.Repeat("x", maxFlowCanvasSnapshotBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := takeFlowCanvasSnapshot(session, message.Envelope{
				Metadata: map[string]string{flowCanvasSnapshotMetadataKey: raw},
			})
			if err == nil {
				t.Fatal("expected invalid snapshot to be rejected")
			}
		})
	}
}

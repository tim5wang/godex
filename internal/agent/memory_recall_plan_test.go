package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/memory"
)

func TestTaskRecallUsesOnlyOriginalUserTopic(t *testing.T) {
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "修复 gopls 引用"),
		protocol.NewTextMessage(protocol.RoleAssistant, "建议改用 PostgreSQL VACUUM"),
		protocol.NewEphemeralTextMessage(protocol.KindMemory, "桌面 Goal: PostgreSQL VACUUM"),
		protocol.NewTextMessage(protocol.RoleUser, "好，继续"),
	}
	plan := memoryRecallPlan(history)
	if plan.Mode != "continuation" || !strings.Contains(plan.Query, "gopls") || strings.Contains(plan.Query, "PostgreSQL") {
		t.Fatal("non-user context contaminated the task query", plan)
	}
	if got := memoryRecallPlan(history[:3], "好，继续"); got != plan {
		t.Fatal("host and runner derived different queries", got, plan)
	}
}

func TestTaskRecallContinuationAndSwitchInActualContext(t *testing.T) {
	a := newTestAgent(t, 100000)
	for _, content := range []string{"gopls references repair", "Vision OCR workflow"} {
		if _, err := a.memoryMgr.Remember(memory.SaveInput{Title: content, Summary: content, Content: content, Type: memory.TypeWorkFact}); err != nil {
			t.Fatal(err)
		}
	}
	a.AddMessage("修复 gopls references")
	a.AddMessage("好，继续下一步")
	first, err := a.buildContext(context.Background())
	if err != nil || !strings.Contains(first.RuntimeTail, "gopls references repair") || strings.Contains(first.RuntimeTail, "Vision OCR workflow") {
		t.Fatal("continuation lost its topic", first, err)
	}
	first.AckRuntime()
	a.AddMessage("谢谢")
	ack, err := a.buildContext(context.Background())
	if err != nil || strings.Contains(ack.RuntimeTail, "gopls references repair") {
		t.Fatal("acknowledgment injected related knowledge", ack, err)
	}
	ack.AckRuntime()
	a.AddMessage("换个话题：Vision OCR")
	switched, err := a.buildContext(context.Background())
	if err != nil || !strings.Contains(switched.RuntimeTail, "Vision OCR workflow") || strings.Contains(switched.RuntimeTail, "gopls references repair") {
		t.Fatal("topic switch retained stale knowledge", switched, err)
	}
}

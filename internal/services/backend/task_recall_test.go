package backend

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestTaskRecallDesktopPromptAndSessionIsolation(t *testing.T) {
	caller := &stubCaller{}
	for i := 0; i < 12; i++ {
		caller.responses = append(caller.responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("处理完成")}})
	}
	s := newTestService(newTestConfig(t), caller)
	c := createDesktopTest(t, s, "shared desktop with unrelated goal")
	f := confirmedDesktopFact(t, s, c.ID)
	a := linkDesktopTest(t, s, c.ID, "first")
	b := linkDesktopTest(t, s, c.ID, "second")
	for i, tt := range []struct {
		session *sessionState
		text    string
		want    bool
	}{
		{a, "检查 Vision OCR", true}, {a, "好，继续下一步", true},
		{a, "量子纠缠贝尔不等式", false}, {a, "谢谢", false}, {b, "继续下一步", false},
	} {
		caller.mu.Lock()
		before := len(caller.requests)
		caller.mu.Unlock()
		result, err := s.Submit(desktopOwnerContext(), tt.session.id, message.NewTextEnvelope(message.SourceWeb, tt.session.id, "user", tt.text, time.Now()))
		if err != nil || result.Status != "completed" {
			t.Fatal(result, err)
		}
		caller.mu.Lock()
		var raw []byte
		for _, req := range caller.requests[before:] {
			if strings.Contains(apiMessagesText(req.Messages), tt.text) {
				raw, err = json.Marshal(req)
				break
			}
		}
		caller.mu.Unlock()
		if err != nil || len(raw) == 0 || strings.Contains(string(raw), f.ID) != tt.want {
			t.Fatalf("turn %d (%s) unexpected desktop knowledge: want=%v error=%v plan=%+v", i, tt.text, tt.want, err, tt.session.agent.MemoryRecallPlan())
		}
	}
}

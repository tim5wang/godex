package tools

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestDecodeEvalJSONStringPreservesEscapedQuotes(t *testing.T) {
	raw := `{"title":"Demo","url":"http://localhost:8088","text":"Button says \"New chat\"","elements":[]}`
	result := &proto.RuntimeRemoteObject{Value: gson.New(raw)}

	var snapshot BrowserSnapshot
	if err := decodeEvalJSONString(result, &snapshot); err != nil {
		t.Fatalf("decode eval json string: %v", err)
	}
	if !strings.Contains(snapshot.Text, `"New chat"`) {
		t.Fatalf("expected quoted text to survive decode, got %q", snapshot.Text)
	}
}

package auth

import (
	"encoding/base64"
	"testing"
)

func TestIdentifyKeyClassifiesOpenAIKeys(t *testing.T) {
	if got := IdentifyKey("sk-test"); got != KeyKindAPIKey {
		t.Fatalf("expected api key, got %q", got)
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{}}`))
	token := "header." + payload + ".signature"
	if got := IdentifyKey(token); got != KeyKindCodexOAuth {
		t.Fatalf("expected codex oauth, got %q", got)
	}
	if got := IdentifyKey("opaque-token"); got != KeyKindUnknown {
		t.Fatalf("expected unknown, got %q", got)
	}
}

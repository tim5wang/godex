package conversation

import "testing"

func TestPromptLayersBuild(t *testing.T) {
	prompt := PromptLayers{
		Base:   "base",
		Skills: []string{"skill-a", "skill-b"},
		Extra:  []string{"extra"},
	}

	got := prompt.Build()
	want := "base\n\nskill-a\n\nskill-b\n\nextra"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

package memory

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/compress"
)

func TestNormalizeMemoryLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"- Use Go for everything", "use go for everything"},
		{"*  Use Go for everything", "use go for everything"},
		{"(2026-08-11) Use Go for everything", "use go for everything"},
		{"- (2026-08-11) Use Go for everything", "use go for everything"},
		{"  Use   Go   for everything  ", "use go for everything"},
		{"USE GO FOR EVERYTHING", "use go for everything"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := normalizeMemoryLine(tc.in); got != tc.want {
			t.Errorf("normalizeMemoryLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFoldCaptureAppendsNewAndSkipsDuplicates(t *testing.T) {
	existing := "- (2026-08-10) Use Go for runtime code\n- Prefer concise prose"
	incoming := "- Use Go for runtime code\n- Add integration tests"
	body, added := foldCapture(existing, incoming)
	if added != 1 {
		t.Fatalf("expected 1 added line, got %d (body=%q)", added, body)
	}
	if !strings.Contains(body, "Use Go for runtime code") {
		t.Errorf("expected existing first line preserved, got %q", body)
	}
	if !strings.Contains(body, "Prefer concise prose") {
		t.Errorf("expected existing second line preserved, got %q", body)
	}
	if !strings.Contains(body, "Add integration tests") {
		t.Errorf("expected new line appended, got %q", body)
	}
	if strings.Count(body, "Use Go for runtime code") != 1 {
		t.Errorf("expected duplicate line folded once, got %q", body)
	}
}

func TestFoldCaptureAllDuplicatesAddsNothing(t *testing.T) {
	existing := "- (2026-08-11) Run go test before commit"
	body, added := foldCapture(existing, "Run go test before commit")
	if added != 0 {
		t.Fatalf("expected 0 added for full duplicate, got %d", added)
	}
	if body != existing {
		t.Fatalf("expected body unchanged on full duplicate, got %q", body)
	}
}

func TestFoldCaptureEmptyIncomingKeepsExisting(t *testing.T) {
	existing := "- keep me"
	body, added := foldCapture(existing, "")
	if added != 0 || body != existing {
		t.Fatalf("expected no-op on empty incoming, got added=%d body=%q", added, body)
	}
}

func TestFoldCaptureEmptyExistingStartsWithNew(t *testing.T) {
	body, added := foldCapture("", "- (2026-08-11) First fact")
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}
	if body != "- (2026-08-11) First fact" {
		t.Fatalf("expected body to start with new line, got %q", body)
	}
}

func TestTruncateTextTailToTokenBudgetKeepsNewestTail(t *testing.T) {
	head := "OLD-HEAD-CONTENT "
	tail := "NEW-TAIL-CONTENT"
	text := head + tail
	// Budget that fits the tail but not head+tail together.
	budget := compress.CountTokens(tail) + 1
	trimmed := truncateTextTailToTokenBudget(text, budget)
	if trimmed == "" {
		t.Fatal("expected non-empty trimmed tail")
	}
	if strings.Contains(trimmed, "OLD-HEAD") {
		t.Errorf("expected oldest head dropped under capTail, got %q", trimmed)
	}
	if !strings.Contains(trimmed, "NEW-TAIL") {
		t.Errorf("expected newest tail preserved, got %q", trimmed)
	}
	if compress.CountTokens(trimmed) > budget {
		t.Errorf("expected trimmed token count <= %d, got %q (%d tokens)", budget, trimmed, compress.CountTokens(trimmed))
	}
}

func TestTruncateTextTailToTokenBudgetShortTextUntouched(t *testing.T) {
	text := "short"
	if got := truncateTextTailToTokenBudget(text, 100); got != text {
		t.Fatalf("expected short text untouched, got %q", got)
	}
}

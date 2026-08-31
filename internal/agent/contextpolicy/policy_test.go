package contextpolicy

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/compress"
)

func TestTruncateTextRespectsBudget(t *testing.T) {
	got := TruncateText(strings.Repeat("alpha beta gamma delta ", 500), 400)
	if compress.CountTokens(got) > 400 || !strings.Contains(got, "[truncated]") {
		t.Fatalf("invalid truncation: tokens=%d", compress.CountTokens(got))
	}
}

func TestAssembleHandoffsRespectsUTF8Ceiling(t *testing.T) {
	got := AssembleHandoffs([]string{strings.Repeat("中文", 200)}, 200, 1000)
	if len(got) > 200 || !utf8.ValidString(got) || !strings.Contains(got, "truncated") {
		t.Fatalf("invalid handoff: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestRoleBudget(t *testing.T) {
	if got := RoleBudget("researcher", ""); got != BudgetResearcher {
		t.Fatalf("researcher budget = %d", got)
	}
	if got := RoleBudget("", "general-purpose"); got != BudgetDefault {
		t.Fatalf("default budget = %d", got)
	}
}

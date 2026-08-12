package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/scope"
)

// TestScopedManagerIsolatesSessions verifies that two session-scoped managers
// created from the same base directory never see each other's memory, while an
// org-scoped manager stays on the shared base layer (roadmap 6.2 M2).
func TestScopedManagerIsolatesSessions(t *testing.T) {
	base := t.TempDir()

	sessionA := NewScopedManager(base, scope.Session("session-a"))
	sessionB := NewScopedManager(base, scope.Session("session-b"))

	if sessionA.Scope() != scope.Session("session-a") {
		t.Fatalf("Scope() = %q, want session:session-a", sessionA.Scope())
	}
	if got := sessionA.Dir(); got == sessionB.Dir() {
		t.Fatalf("session managers must use distinct dirs, both %q", got)
	}
	dirA := sessionA.Dir()
	if dirA == base {
		// session scope must be isolated under a subdirectory
		t.Fatalf("session manager dir %q must not equal base %q", dirA, base)
	}
	rel, err := filepath.Rel(base, dirA)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		t.Fatalf("session dir %q must live under base %q (rel=%q err=%v)", dirA, base, rel, err)
	}

	mustRemember(t, sessionA, "Session A fact", "alpha zebra fact")
	mustRemember(t, sessionB, "Session B fact", "beta yak fact")

	found, err := sessionA.FindRelevant("alpha zebra", 5)
	if err != nil {
		t.Fatalf("FindRelevant A: %v", err)
	}
	if len(found) == 0 || found[0].Title != "Session A fact" {
		t.Fatalf("session A should recall its own memory, got %+v", found)
	}

	// "yak" only exists in session B; session A must not see it.
	leak, err := sessionA.FindRelevant("yak", 5)
	if err != nil {
		t.Fatalf("FindRelevant A leak check: %v", err)
	}
	if len(leak) > 0 {
		t.Fatalf("session A leaked session B memory: %+v", leak)
	}
}

// TestScopedManagerOrgStaysShared verifies an org-scoped manager roots at the
// base directory (the shared org/legacy layer) and is not pushed into a
// session subdirectory.
func TestScopedManagerOrgStaysShared(t *testing.T) {
	base := t.TempDir()
	orgMgr := NewScopedManager(base, scope.Org("godex"))
	if orgMgr.Dir() != base {
		t.Fatalf("org manager dir = %q, want base %q", orgMgr.Dir(), base)
	}
	if orgMgr.Scope() != scope.Org("godex") {
		t.Fatalf("Scope() = %q, want org:godex", orgMgr.Scope())
	}

	// A plain manager (legacy default) shares the same root as org scope.
	plain := NewManager(base)
	if plain.Dir() != orgMgr.Dir() {
		t.Fatalf("plain and org managers must share root, %q vs %q", plain.Dir(), orgMgr.Dir())
	}
}

// TestScopedManagerEmptyScopeUsesBase verifies the unspecified scope stays at
// the base directory for backward compatibility.
func TestScopedManagerEmptyScopeUsesBase(t *testing.T) {
	base := t.TempDir()
	mgr := NewScopedManager(base, "")
	if mgr.Dir() != base {
		t.Fatalf("empty-scope manager dir = %q, want base %q", mgr.Dir(), base)
	}
}

// TestScopedManagerPersonalIsolated verifies a personal scope is isolated too,
// but shared across that user's sessions is represented by the same scope ref.
func TestScopedManagerPersonalIsolated(t *testing.T) {
	base := t.TempDir()
	p1 := NewScopedManager(base, scope.Personal("user-1"))
	p1b := NewScopedManager(base, scope.Personal("user-1"))
	p2 := NewScopedManager(base, scope.Personal("user-2"))

	if p1.Dir() != p1b.Dir() {
		t.Fatalf("same personal scope must share a dir: %q vs %q", p1.Dir(), p1b.Dir())
	}
	if p1.Dir() == p2.Dir() {
		t.Fatalf("different personal scopes must not share a dir: %q", p1.Dir())
	}
}

func mustRemember(t *testing.T, mgr *Manager, title, content string) {
	t.Helper()
	if _, err := mgr.Remember(SaveInput{
		Title:   title,
		Summary: content,
		Content: content,
		Type:    TypeProject,
	}); err != nil {
		t.Fatalf("remember %q: %v", title, err)
	}
}

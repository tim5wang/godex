package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/scope"
)

func TestResolveWritePathAllowsInside(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	cases := []struct {
		requested string
		want      string
	}{
		{"docs/plan.md", filepath.Join(root, "docs", "plan.md")},
		{"./a.txt", filepath.Join(root, "a.txt")},
		{filepath.Join(root, "sub", "f.go"), filepath.Join(root, "sub", "f.go")},
		{"a/b/../c.txt", filepath.Join(root, "a", "c.txt")}, // internal .. is fine
	}
	for _, tc := range cases {
		got, err := ResolveWritePath(scope.Session("s1"), root, tc.requested)
		if err != nil {
			t.Errorf("ResolveWritePath(%q) unexpected error: %v", tc.requested, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveWritePath(%q) = %q, want %q", tc.requested, got, tc.want)
		}
	}
}

func TestResolveWritePathRejectsEscape(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	cases := []string{
		"../escape.txt",
		"../../etc/passwd",
		filepath.Join("a", "..", "..", "out.txt"),
		filepath.Join(root, "..", "outside.txt"),
	}
	for _, tc := range cases {
		_, err := ResolveWritePath(scope.Session("s1"), root, tc)
		if err == nil {
			t.Errorf("ResolveWritePath(%q) should reject escape", tc)
			continue
		}
		if !strings.Contains(err.Error(), "escapes workspace root") {
			t.Errorf("ResolveWritePath(%q) error %q should mention escape", tc, err)
		}
	}
}

func TestResolveWritePathRejectsAbsoluteOutside(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	_, err := ResolveWritePath(scope.Session("s1"), root, "/etc/hosts")
	if err == nil {
		t.Fatal("absolute path outside root should be rejected")
	}
}

func TestResolveWritePathEmptyAndErrors(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if _, err := ResolveWritePath(scope.Session("s1"), "", "x"); err == nil {
		t.Fatal("empty root should error")
	}
	if _, err := ResolveWritePath(scope.Session("s1"), root, "  "); err == nil {
		t.Fatal("empty requested should error")
	}
}

func TestResolveWritePathOrgScopeSameBehavior(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	got, err := ResolveWritePath(scope.Org("godex"), root, "notes/a.md")
	if err != nil {
		t.Fatalf("org scope: %v", err)
	}
	if want := filepath.Join(root, "notes", "a.md"); got != want {
		t.Fatalf("org scope got %q want %q", got, want)
	}
}

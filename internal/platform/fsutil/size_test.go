package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeBestEffort(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "one"), []byte("123"), 0o600); err != nil {
		t.Fatalf("write first file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "two"), []byte("4567"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	if got := DirSizeBestEffort(root); got != 7 {
		t.Fatalf("expected 7 bytes, got %d", got)
	}
	if got := DirSizeBestEffort(filepath.Join(root, "missing")); got != 0 {
		t.Fatalf("expected missing directory to report 0, got %d", got)
	}
}

package skill

import (
	"context"
	"testing"
)

func TestPrepareInstallSourceHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader := NewLoader(t.TempDir())
	_, cleanup, err := loader.prepareInstallSource(ctx, "owner/repo")
	cleanup()
	if err == nil {
		t.Fatal("expected canceled skill source preparation to fail")
	}
}

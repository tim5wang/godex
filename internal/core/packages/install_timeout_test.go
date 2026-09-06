package packages

import (
	"context"
	"testing"
)

func TestPrepareSourceHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := prepareSource(ctx, "owner/repo")
	cleanup()
	if err == nil {
		t.Fatal("expected canceled package source preparation to fail")
	}
}

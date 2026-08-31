package idgen

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	id := New("sess_", 12)
	if !strings.HasPrefix(id, "sess_") || len(id) != len("sess_")+24 {
		t.Fatalf("unexpected id %q", id)
	}
}

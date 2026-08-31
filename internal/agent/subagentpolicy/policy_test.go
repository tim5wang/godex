package subagentpolicy

import (
	"reflect"
	"testing"
)

func TestNarrowWriteTools(t *testing.T) {
	got := NarrowWriteTools([]string{"bash", "read_file", "write_file"}, nil)
	if !reflect.DeepEqual(got, []string{"read_file"}) {
		t.Fatalf("read-only tools = %v", got)
	}
	got = NarrowWriteTools([]string{"bash", "read_file", "write_file"}, []string{"internal"})
	if !reflect.DeepEqual(got, []string{"bash", "read_file", "write_file"}) {
		t.Fatalf("write tools = %v", got)
	}
}

func TestPathAllowed(t *testing.T) {
	if !PathAllowed("internal/agent/file.go", []string{"internal/agent"}) {
		t.Fatal("expected nested path to be allowed")
	}
	if PathAllowed("../outside", []string{"internal"}) {
		t.Fatal("expected traversal to be denied")
	}
}

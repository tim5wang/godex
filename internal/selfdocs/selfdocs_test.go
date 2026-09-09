package selfdocs

import (
	"strings"
	"testing"
)

func TestAllSortedAndUnique(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("expected at least one generated feature (run go generate ./internal/selfdocs/... if empty)")
	}
	seen := map[string]bool{}
	prev := ""
	for _, f := range all {
		if f.ID == "" {
			t.Errorf("feature with empty id: %+v", f)
		}
		if seen[f.ID] {
			t.Errorf("duplicate feature id %q", f.ID)
		}
		seen[f.ID] = true
		if prev != "" && f.ID < prev {
			t.Errorf("features not sorted: %q after %q", f.ID, prev)
		}
		prev = f.ID
		if f.Location == "" {
			t.Errorf("feature %q missing source location", f.ID)
		}
		if strings.Contains(f.Location, "/Users/") || strings.HasPrefix(f.Location, "/") {
			t.Errorf("feature %q location should be repo-relative, got %q", f.ID, f.Location)
		}
	}
}

func TestGet(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Skip("no generated features")
	}
	want := all[0]
	got, ok := Get(want.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", want.ID)
	}
	if got.ID != want.ID {
		t.Errorf("Get(%q).ID = %q", want.ID, got.ID)
	}
	if _, ok := Get("definitely-not-a-feature-id"); ok {
		t.Error("Get on unknown id should return false")
	}
}

func TestSearch(t *testing.T) {
	if len(All()) == 0 {
		t.Skip("no generated features")
	}
	// Searching for the first feature's id must find it.
	first := All()[0]
	if got := Search(first.ID); len(got) == 0 {
		t.Errorf("Search(%q) found nothing", first.ID)
	}
	// Description terms must be searchable (e.g. the webui entry mentions
	// "Web 工作台"; longtask mentions "LongTask").
	if got := Search("LongTask"); len(got) == 0 {
		t.Error("Search(\"LongTask\") found nothing")
	}
	// Case-insensitive.
	if got := Search("longtask"); len(got) == 0 {
		t.Error("Search(\"longtask\") found nothing (case-insensitive expected)")
	}
	// Empty query returns nil.
	if got := Search(""); got != nil {
		t.Errorf("Search(\"\") = %v, want nil", got)
	}
	// Nonsense query returns empty.
	if got := Search("zzz-no-such-term-xyz"); len(got) != 0 {
		t.Errorf("Search(nonsense) = %v, want empty", got)
	}
}

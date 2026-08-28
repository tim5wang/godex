package templates

import "testing"

func TestNormalizeMemoryMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", MemoryShared},
		{"shared", MemoryShared},
		{"SHARED", MemoryShared},
		{"none", MemoryNone},
		{"scoped", MemoryScoped},
		{"Scoped", MemoryScoped},
		{"bogus", MemoryShared},
		{"  none  ", MemoryNone},
	}
	for _, c := range cases {
		if got := NormalizeMemoryMode(c.in); got != c.want {
			t.Errorf("NormalizeMemoryMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

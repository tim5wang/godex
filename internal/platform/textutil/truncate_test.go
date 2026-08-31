package textutil

import "testing"

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "non-positive unchanged", value: "hello", want: "hello"},
		{name: "within limit", value: "你好", limit: 2, want: "你好"},
		{name: "unicode truncated", value: "你好世界", limit: 2, want: "你好..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateRunes(tt.value, tt.limit); got != tt.want {
				t.Fatalf("TruncateRunes(%q, %d) = %q, want %q", tt.value, tt.limit, got, tt.want)
			}
		})
	}
}

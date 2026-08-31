package toolfilter

import "testing"

func TestAllows(t *testing.T) {
	tests := []struct {
		name string
		list []string
		item string
		want bool
	}{
		{name: "empty", item: "crm"},
		{name: "exact", list: []string{"crm"}, item: "crm", want: true},
		{name: "scoped", list: []string{"crm/*"}, item: "crm", want: true},
		{name: "wildcard", list: []string{"*"}, item: "crm", want: true},
		{name: "unmatched", list: []string{"kb"}, item: "crm"},
		{name: "exact exclusion wins", list: []string{"crm", "!crm"}, item: "crm"},
		{name: "scoped exclusion wins", list: []string{"*", "!crm/*"}, item: "crm"},
		{name: "global exclusion wins", list: []string{"crm", "!*"}, item: "crm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allows(tt.list, tt.item); got != tt.want {
				t.Fatalf("Allows(%v, %q) = %v, want %v", tt.list, tt.item, got, tt.want)
			}
		})
	}
}

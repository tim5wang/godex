package valueutil

import (
	"encoding/json"
	"testing"
)

func TestInt(t *testing.T) {
	for _, value := range []interface{}{3, int64(4), float64(5), json.Number("6")} {
		if _, ok := Int(value); !ok {
			t.Fatalf("expected %T to convert", value)
		}
	}
	if _, ok := Int("7"); ok {
		t.Fatal("plain strings are not protocol numbers")
	}
}

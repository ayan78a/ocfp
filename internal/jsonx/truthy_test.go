package jsonx

// Pins JS truthiness for decoded JSON values — the `||` operand gate, e.g.
// utils/error.js:80 `json.error?.message || json.message || json.error ||
// bodyText`. Only null, false, 0 and "" are falsy; everything else —
// including empty objects/arrays and the strings "0"/"false" — is truthy.

import (
	"encoding/json"
	"testing"
)

func TestTruthy(t *testing.T) {
	decode := func(raw string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v
	}
	cases := []struct {
		name string
		v    any
		want bool
	}{
		{"nil is falsy", nil, false},
		{"JSON null is falsy", decode(`null`), false},
		{"false is falsy", decode(`false`), false},
		{"true is truthy", decode(`true`), true},
		{"zero is falsy", decode(`0`), false},
		{"negative zero is falsy", decode(`-0`), false},
		{"nonzero number is truthy", decode(`5`), true},
		{"negative number is truthy", decode(`-1.5`), true},
		{"tiny fraction is truthy", decode(`0.0001`), true},
		{"empty string is falsy", decode(`""`), false},
		{"zero string is truthy", decode(`"0"`), true},
		{"false string is truthy", decode(`"false"`), true},
		{"nonempty string is truthy", decode(`"x"`), true},
		{"empty object is truthy", decode(`{}`), true},
		{"empty array is truthy", decode(`[]`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truthy(tc.v); got != tc.want {
				t.Fatalf("Truthy(%#v) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

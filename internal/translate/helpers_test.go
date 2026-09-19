package translate

// Shared helpers for the translate package tests. Every expectation in these
// tests is derived by hand from the authoritative JS sources in
// ~/9router/9router-src/open-sse/translator — not from the Go implementation.

import (
	"encoding/json"
	"strings"
	"testing"

	"opencode-free-proxy/internal/jsonx"
)

// jb parses a JSON object literal — bodies and SSE payloads as the wire
// delivers them (encoding/json, so numbers come back as float64 like JS).
func jb(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("jb(%s): %v", s, err)
	}
	return v
}

// ja parses a JSON array literal.
func ja(t *testing.T, s string) []any {
	t.Helper()
	var v []any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("ja(%s): %v", s, err)
	}
	return v
}

// js renders v as it would travel on the wire. map keys marshal sorted, so the
// result is a stable comparison key.
func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(b)
}

// eq fails the test when got and want serialize differently.
func eq(t *testing.T, label string, got, want any) {
	t.Helper()
	if g, w := js(got), js(want); g != w {
		t.Fatalf("%s:\n got: %s\nwant: %s", label, g, w)
	}
}

// dig walks v through object keys (string) and array indexes (int).
func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	cur := v
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("dig %v: not an object at key %q: %s", path, k, js(cur))
			}
			next, ok := m[k]
			if !ok {
				t.Fatalf("dig %v: missing key %q in %s", path, k, js(m))
			}
			cur = next
		case int:
			a, ok := cur.([]any)
			if !ok {
				t.Fatalf("dig %v: not an array at index %d: %s", path, k, js(cur))
			}
			if k < 0 || k >= len(a) {
				t.Fatalf("dig %v: index %d out of range (len %d)", path, k, len(a))
			}
			cur = a[k]
		default:
			t.Fatalf("dig: unsupported path element %v (%T)", p, p)
		}
	}
	return cur
}

// dgs is dig + string assertion.
func dgs(t *testing.T, v any, path ...any) string {
	t.Helper()
	got := dig(t, v, path...)
	s, is := got.(string)
	if !is {
		t.Fatalf("dig %v: want string, got %s", path, js(got))
	}
	return s
}

// msgs returns body.messages as a slice, failing when it is missing.
func msgs(t *testing.T, body map[string]any) []any {
	t.Helper()
	m := jsonx.AsArr(body["messages"])
	if m == nil {
		t.Fatalf("body has no messages array: %s", js(body))
	}
	return m
}

// msgAt returns messages[i] as an object.
func msgAt(t *testing.T, body map[string]any, i int) map[string]any {
	t.Helper()
	m := jsonx.AsObj(dig(t, body, "messages", i))
	if m == nil {
		t.Fatalf("messages[%d] is not an object: %s", i, js(dig(t, body, "messages", i)))
	}
	return m
}

// key reports whether m holds key.
func key(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// setOf asserts two slices hold the same elements regardless of order —
// needed wherever the Go port builds an array out of a Go map (iteration order
// is randomized) while the JS source preserves insertion order.
func setOf(t *testing.T, label string, got []any, want ...string) {
	t.Helper()
	seen := map[string]int{}
	for _, v := range got {
		seen[jsonx.AsStr(v)]++
	}
	if len(got) != len(want) {
		t.Fatalf("%s: want %d items (%v), got %d (%s)", label, len(want), want, len(got), js(got))
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Fatalf("%s: want exactly one %q in %s", label, w, js(got))
		}
	}
}

// eqNames compares an event/chunk name sequence positionally.
func eqNames(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s:\n got: [%s]\nwant: [%s]", label, strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

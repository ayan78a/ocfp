package relay

// Tests for the stream.js / streamHelpers.js relay plumbing
// (internal/relay/common.go) plus the shared helpers used by the other relay
// test files.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"opencode-free-proxy/internal/cloak"
)

// --- shared relay test helpers ---

// decodeData decodes the JSON payload of one canonical `data: ` line.
func decodeData(t *testing.T, line string) map[string]any {
	t.Helper()
	const prefix = "data: "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("line %q is not a canonical data line", line)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line[len(prefix):]), &m); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return m
}

// payloadOf decodes one emitted frame ("data: {...}\n").
func payloadOf(t *testing.T, frame string) map[string]any {
	t.Helper()
	line := strings.TrimSuffix(frame, "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("frame %q spans multiple lines", frame)
	}
	return decodeData(t, line)
}

// dataFrames decodes every `data: ` line of an emitted buffer, in order.
func dataFrames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, decodeData(t, line))
		}
	}
	return frames
}

// lastDataLine returns the last `data: ` line of an emitted buffer.
func lastDataLine(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "data: ") {
			return lines[i]
		}
	}
	t.Fatalf("no data line in %q", out)
	return ""
}

// sseEvent is one decoded framed event block (event: X / data: {...}).
type sseEvent struct {
	name string
	data map[string]any
}

// events splits an emitted buffer into framed events (blocks separated by a
// blank line).
func events(t *testing.T, out string) []sseEvent {
	t.Helper()
	var evs []sseEvent
	for _, block := range strings.Split(out, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.data); err != nil {
					t.Fatalf("decode event data in %q: %v", block, err)
				}
			}
		}
		if ev.name == "" || ev.data == nil {
			t.Fatalf("malformed event block %q", block)
		}
		evs = append(evs, ev)
	}
	return evs
}

// getNum reads a numeric field (encoding/json decodes numbers as float64).
func getNum(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("key %q = %#v, want number", key, m[key])
	}
	return v
}

// --- common.go ---

func TestFixInvalidID(t *testing.T) {
	t.Run("generic id uses extend_fields.requestId", func(t *testing.T) {
		parsed := map[string]any{
			"id":            "chat",
			"extend_fields": map[string]any{"requestId": "req-9", "traceId": "tr-1"},
		}
		if !fixInvalidID(parsed) {
			t.Fatal("fixInvalidID must report the mutation")
		}
		if parsed["id"] != "chatcmpl-req-9" {
			t.Fatalf("id = %v, want chatcmpl-req-9", parsed["id"])
		}
	})
	t.Run("traceId is the second fallback", func(t *testing.T) {
		parsed := map[string]any{
			"id":            "completion",
			"extend_fields": map[string]any{"requestId": "", "traceId": "tr-1"},
		}
		fixInvalidID(parsed)
		if parsed["id"] != "chatcmpl-tr-1" {
			t.Fatalf("id = %v, want chatcmpl-tr-1 (empty requestId skipped)", parsed["id"])
		}
	})
	t.Run("short id falls back to base36 milliseconds", func(t *testing.T) {
		parsed := map[string]any{"id": "abc"}
		fixInvalidID(parsed)
		id, _ := parsed["id"].(string)
		if !regexp.MustCompile(`^chatcmpl-[0-9a-z]+$`).MatchString(id) {
			t.Fatalf("id = %q, want chatcmpl-<base36 Date.now()>", id)
		}
	})
	t.Run("valid id untouched", func(t *testing.T) {
		parsed := map[string]any{"id": "chatcmpl-abc12345"}
		if fixInvalidID(parsed) {
			t.Fatal("valid id must report false")
		}
		if parsed["id"] != "chatcmpl-abc12345" {
			t.Fatalf("id mutated: %v", parsed["id"])
		}
	})
	t.Run("eight characters are long enough", func(t *testing.T) {
		parsed := map[string]any{"id": "12345678"}
		if fixInvalidID(parsed) {
			t.Fatalf("len(id) == 8 is not < 8; got %v", parsed["id"])
		}
	})
	t.Run("missing id reports false", func(t *testing.T) {
		parsed := map[string]any{"model": "m"}
		if fixInvalidID(parsed) {
			t.Fatal("missing id must report false")
		}
	})
}

func TestFormatInt36(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{9, "9"},
		{35, "z"},
		{36, "10"},
		{12345, "9ix"}, // JS (12345).toString(36)
		{-12345, "-9ix"},
		{2176782336, "1000000"}, // 36^6
	}
	for _, c := range cases {
		if got := formatInt36(c.in); got != c.want {
			t.Fatalf("formatInt36(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasValuableContent(t *testing.T) {
	chatChunk := func(delta map[string]any, finish any, usage any) map[string]any {
		choice := map[string]any{"index": 0.0, "delta": delta, "finish_reason": finish}
		chunk := map[string]any{"choices": []any{choice}}
		if usage != nil {
			chunk["usage"] = usage
		}
		return chunk
	}
	cases := []struct {
		name  string
		chunk map[string]any
		want  bool
	}{
		{"text content", chatChunk(map[string]any{"content": "Hi"}, nil, nil), true},
		{"reasoning content", chatChunk(map[string]any{"reasoning_content": "hmm"}, nil, nil), true},
		{"tool calls", chatChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0.0}}}, nil, nil), true},
		{"role only", chatChunk(map[string]any{"role": "assistant"}, nil, nil), true},
		{"finish reason", chatChunk(map[string]any{}, "stop", nil), true},
		{"valid usage on empty delta", chatChunk(map[string]any{}, nil, map[string]any{"prompt_tokens": 3.0}), true},
		{"empty delta, no usage", chatChunk(map[string]any{}, nil, nil), false},
		{"zero-only usage", chatChunk(map[string]any{}, nil, map[string]any{"prompt_tokens": 0.0}), false},
		{"null usage", chatChunk(map[string]any{}, nil, nil), false},
		{"empty role string is falsy", chatChunk(map[string]any{"role": ""}, nil, nil), false},
		{"no choices at all (usage-only chunk)", map[string]any{"usage": map[string]any{"prompt_tokens": 3.0}}, true},
		{"empty choices array", map[string]any{"choices": []any{}}, true},
		// JS gate: the filter only runs when choices[0].delta is present — a
		// delta-less choice keeps the chunk whatever else it carries
		// (streamHelpers.js:40 falls through to `return true`).
		{"delta-less choice, nothing else", map[string]any{"choices": []any{map[string]any{"index": 0.0, "finish_reason": nil}}}, true},
		{"delta-less choice with finish reason", map[string]any{"choices": []any{map[string]any{"index": 0.0, "finish_reason": "stop"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasValuableContent(c.chunk); got != c.want {
				t.Fatalf("hasValuableContent(%v) = %v, want %v", c.chunk, got, c.want)
			}
		})
	}
}

func TestResolveSynthesis(t *testing.T) {
	intent := func(mode string) *cloak.ThinkingCfg { return &cloak.ThinkingCfg{Mode: mode} }
	cases := []struct {
		name    string
		body    map[string]any
		model   string
		intent  *cloak.ThinkingCfg
		enabled bool
	}{
		{"nothing requested", map[string]any{}, "m", nil, false},
		{"request body reasoning_effort", map[string]any{"reasoning_effort": "high"}, "m", nil, true},
		{"explicit none in body disables", map[string]any{"reasoning_effort": "none"}, "m", nil, false},
		{"intent enables", map[string]any{}, "m", intent("auto"), true},
		{"intent none beats the body", map[string]any{"reasoning_effort": "high"}, "m", intent("none"), false},
		{"model suffix none wins over intent", map[string]any{}, "m(none)", intent("auto"), false},
		{"model suffix wins over none intent", map[string]any{}, "m(high)", intent("none"), true},
		{"unknown suffix falls through to intent", map[string]any{}, "m(bogus)", intent("auto"), true},
		{"off suffix disables", map[string]any{}, "m(off)", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveSynthesis(c.body, c.model, c.intent)
			if got.Enabled != c.enabled {
				t.Fatalf("ResolveSynthesis(%v, %q, %v) = %+v, want enabled=%v", c.body, c.model, c.intent, got, c.enabled)
			}
			if got.Ratio != 0.75 { // usage.DefaultRatio (HIDDEN_THINKING_RATIO)
				t.Fatalf("ratio = %v, want the fixed default 0.75", got.Ratio)
			}
		})
	}
}

// jsTruthy is the shared JS-truthiness predicate for the falsy-check ports
// (stream.js:279-280 missing fields, stream.js:288 truthy-choices guard,
// sseToJsonHandler.js:125 error chunks). Empty arrays and objects are TRUTHY
// in JS — JS has no length truthiness; the length checks that matter are
// spelled out separately (stream.js:304, streamHelpers.js:47).
func TestJsTruthy(t *testing.T) {
	truthTable := []struct {
		value any
		want  bool
	}{
		{nil, false},
		{false, false},
		{true, true},
		{0.0, false},
		{1.0, true},
		{-1.0, true},
		{"", false},
		{"0", true},
		{"x", true},
		{[]any{}, true},
		{[]any{1.0}, true},
		{map[string]any{}, true},
	}
	for _, c := range truthTable {
		if got := jsTruthy(c.value); got != c.want {
			t.Fatalf("jsTruthy(%#v) = %v, want %v", c.value, got, c.want)
		}
	}
}

package relay

// Tests for the SSE framing helpers (streamHelpers.js formatSSE /
// cleanUsagePayload / parseSSELine and responsesStreamHelpers.js terminal
// detection) ported in internal/relay/sse.go.

import (
	"strings"
	"testing"
)

func TestFormatDataDoneSentinel(t *testing.T) {
	if got := FormatData(map[string]any{"done": true}); got != "data: [DONE]\n\n" {
		t.Fatalf("FormatData(done) = %q, want the [DONE] sentinel", got)
	}
	// JS `if (data && data.done)` is truthy-based; a false bool falls through
	// and renders as a plain object in both engines.
	if got := FormatData(map[string]any{"done": false}); !strings.HasPrefix(got, "data: {") {
		t.Fatalf("FormatData(done:false) = %q, want a plain data line", got)
	}
}

func TestFormatDataFramedEvent(t *testing.T) {
	got := FormatData(map[string]any{
		"event": "response.created",
		"data":  map[string]any{"type": "response.created"},
	})
	if got != "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" {
		t.Fatalf("FormatData framed = %q", got)
	}
}

func TestFormatDataPlainObject(t *testing.T) {
	got := FormatData(map[string]any{"id": "1"})
	if got != "data: {\"id\":\"1\"}\n\n" {
		t.Fatalf("FormatData plain = %q", got)
	}
}

func TestFormatDataCleanUsagePayload(t *testing.T) {
	t.Run("null usage dropped", func(t *testing.T) {
		got := FormatData(map[string]any{"id": "1", "usage": nil})
		if strings.Contains(got, "usage") {
			t.Fatalf("null usage must be dropped: %q", got)
		}
	})
	t.Run("usage.perf_metrics null dropped", func(t *testing.T) {
		got := FormatData(map[string]any{"usage": map[string]any{
			"prompt_tokens": 3.0, "perf_metrics": nil}})
		u := decodeData(t, strings.TrimSuffix(got, "\n\n"))["usage"].(map[string]any)
		if _, has := u["perf_metrics"]; has {
			t.Fatalf("perf_metrics must be dropped: %v", u)
		}
		if u["prompt_tokens"] != 3.0 {
			t.Fatalf("real usage fields must survive: %v", u)
		}
	})
	t.Run("recurses into response", func(t *testing.T) {
		got := FormatData(map[string]any{"response": map[string]any{"usage": nil}})
		resp := decodeData(t, strings.TrimSuffix(got, "\n\n"))["response"].(map[string]any)
		if _, has := resp["usage"]; has {
			t.Fatalf("nested null usage must be dropped: %v", resp)
		}
	})
}

func TestFormatDataLineAndEvent(t *testing.T) {
	if got := FormatDataLine(map[string]any{"id": "1"}); got != "data: {\"id\":\"1\"}\n" {
		t.Fatalf("FormatDataLine must end with a single newline: %q", got)
	}
	if got := FormatEvent("response.done", map[string]any{"a": 1.0}); got != "event: response.done\ndata: {\"a\":1}\n\n" {
		t.Fatalf("FormatEvent = %q", got)
	}
}

func TestClassifyDataLine(t *testing.T) {
	// classifyDataLine's signals mirror the passthrough gate (stream.js:270)
	// plus its catch (364): data-line detection, [DONE] detection, JSON-parse
	// success and objectness are independent. (data: {} is valid, an object
	// with no fields.)
	cases := []struct {
		name      string
		line      string
		object    map[string]any // non-nil: expect a parsed object
		isData    bool
		isDone    bool
		nonObject bool // valid JSON that is not an object (stream.js:272)
	}{
		{"data with space", `data: {"a":1}`, map[string]any{"a": 1.0}, true, false, false},
		{"data without space", `data:{"a":1}`, map[string]any{"a": 1.0}, true, false, false},
		{"empty object", `data: {}`, map[string]any{}, true, false, false},
		{"done sentinel", `data: [DONE]`, nil, true, true, false},
		{"done sentinel without space", `data:[DONE]`, nil, true, true, false},
		{"event line", "event: response.created", nil, false, false, false},
		{"comment", ": ping", nil, false, false, false},
		{"blank", "", nil, false, false, false},
		{"malformed json", `data: not-json`, nil, true, false, false},
		{"empty payload", "data: ", nil, true, false, false},
		// JSON.parse accepts scalars (stream.js:272) — forwarded verbatim by
		// the passthrough tail (stream.js:372-378).
		{"number payload", `data: 42`, nil, true, false, true},
		{"string payload", `data: "x"`, nil, true, false, true},
		{"array payload", `data: [1,2]`, nil, true, false, true},
		{"bool payload", `data: true`, nil, true, false, true},
		// A JSON null payload throws in JS (fixInvalidId reads parsed.id on it,
		// streamHelpers.js:71) and lands in the catch — dropped, not forwarded.
		{"null payload", `data: null`, nil, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, isData, isDone, nonObject := classifyDataLine(c.line)
			if isData != c.isData || isDone != c.isDone || nonObject != c.nonObject {
				t.Fatalf("classifyDataLine(%q) = (_, %v, %v, %v), want data=%v done=%v nonObject=%v",
					c.line, isData, isDone, nonObject, c.isData, c.isDone, c.nonObject)
			}
			if c.object == nil {
				if parsed != nil {
					t.Fatalf("classifyDataLine(%q) parsed %v, want nil", c.line, parsed)
				}
				return
			}
			if parsed == nil {
				t.Fatalf("classifyDataLine(%q) parsed nothing", c.line)
			}
			if c.object["a"] != nil && parsed["a"] != c.object["a"] {
				t.Fatalf("classifyDataLine(%q) = %v", c.line, parsed)
			}
		})
	}
}

func TestParseDataLine(t *testing.T) {
	// parseDataLine is translate mode's two-value view: the parsed object plus
	// "was a data line" — true for both a parsed object and the [DONE]
	// sentinel, false for non-data and unparsable data lines.
	cases := []struct {
		name   string
		line   string
		isLine bool // expected bool
		parses bool // expect a non-nil object
	}{
		{"data with space", `data: {"a":1}`, true, true},
		{"done sentinel", `data: [DONE]`, true, false},
		{"event line", "event: response.created", false, false},
		{"malformed json", `data: not-json`, false, false},
		{"empty payload", "data: ", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, isLine := parseDataLine(c.line)
			if isLine != c.isLine {
				t.Fatalf("parseDataLine(%q) bool = %v, want %v", c.line, isLine, c.isLine)
			}
			if c.parses && parsed == nil {
				t.Fatalf("parseDataLine(%q) parsed nothing", c.line)
			}
			if !c.parses && parsed != nil {
				t.Fatalf("parseDataLine(%q) = %v, want nil", c.line, parsed)
			}
		})
	}
}

func TestTerminalResponsesEvent(t *testing.T) {
	cases := []struct {
		name  string
		event string
		chunk map[string]any
		want  bool
	}{
		{"response.completed", "response.completed", map[string]any{}, true},
		{"response.done", "response.done", map[string]any{}, true},
		{"response.failed", "response.failed", map[string]any{}, true},
		{"error", "error", map[string]any{}, true},
		{"non-terminal", "response.created", map[string]any{}, false},
		{"status completed", "", map[string]any{"response": map[string]any{"status": "completed"}}, true},
		{"status failed", "", map[string]any{"response": map[string]any{"status": "failed"}}, true},
		{"status in progress", "", map[string]any{"response": map[string]any{"status": "in_progress"}}, false},
		// getOpenAIResponsesEventName's chunk.type fallback
		// (responsesStreamHelpers.js:13-17): with no `event:` line the chunk's
		// own type decides.
		{"type fallback terminal", "", map[string]any{"type": "response.completed"}, true},
		{"type fallback error", "", map[string]any{"type": "error"}, true},
		{"type fallback non-terminal", "", map[string]any{"type": "response.output_text.delta"}, false},
		{"event line wins over type", "response.created", map[string]any{"type": "response.completed"}, false},
		// A non-terminal name still falls through to the status check
		// (responsesStreamHelpers.js:22-23).
		{"non-terminal name falls through to status", "", map[string]any{"type": "response.created", "response": map[string]any{"status": "completed"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TerminalResponsesEvent(c.event, c.chunk); got != c.want {
				t.Fatalf("TerminalResponsesEvent(%q, %v) = %v, want %v", c.event, c.chunk, got, c.want)
			}
		})
	}
}

func TestEventName(t *testing.T) {
	if got := EventName("response.created", nil); got != "response.created" {
		t.Fatalf("captured event name must win, got %q", got)
	}
	if got := EventName("", map[string]any{"type": "response.done"}); got != "response.done" {
		t.Fatalf("chunk.type is the fallback, got %q", got)
	}
	if got := EventName("", map[string]any{}); got != "" {
		t.Fatalf("no name anywhere, got %q", got)
	}
}

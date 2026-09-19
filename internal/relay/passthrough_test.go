package relay

// Tests for PassthroughRelay — stream.js STREAM_MODE.PASSTHROUGH: per-chunk
// normalization, the usage seam, and the guaranteed [DONE] sentinel.

import (
	"bytes"
	"strings"
	"testing"

	"opencode-free-proxy/internal/cloak"
)

// estBody mirrors the JS estimate arithmetic: {"model":"m"} encodes to 13
// bytes -> ceil(13/4) = 4 input tokens before the +2000 buffer.
var estBody = map[string]any{"model": "m"}

// NOTE: JSON fixtures below live in raw (backtick) string literals — no quote
// escaping, exactly the bytes an upstream SSE line carries.

const contentChunk = `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"content":"hello world!"},"finish_reason":null}]}`

const finishChunk = `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func newPassthrough(t *testing.T, body map[string]any, format Format, intent *cloak.ThinkingCfg) (*PassthroughRelay, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewPassthroughRelay(&buf, body, "m", format, intent), &buf
}

func processAll(t *testing.T, r *PassthroughRelay, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if err := r.ProcessLine(line); err != nil {
			t.Fatalf("ProcessLine(%q): %v", line, err)
		}
	}
}

// Former parity bug (fixed): parsable chunks used to be forwarded verbatim,
// bypassing normalization, the empty-chunk filter and the usage seam (the
// parseDataLine bool was read as the [DONE] sentinel). This test keeps the
// seam exercised end to end: an unparsable-id, empty-delta chunk goes through
// fixInvalidId + field injection and is THEN dropped by hasValuableContent —
// nothing may reach the wire (stream.js:274-313).
func TestPassthroughEmptyDeltaWithInvalidIdDropped(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, `data: {"id":"chat","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	if buf.Len() != 0 {
		t.Fatalf("an empty-delta chunk must be dropped even when its id was repaired, got %q", buf.String())
	}
	if r.FinalUsage != nil {
		t.Fatalf("dropped chunk must leave no tracked usage, got %v", r.FinalUsage)
	}
}

func TestPassthroughPlainChunkVerbatim(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	line := contentChunk + "\n"
	processAll(t, r, line)
	if buf.String() != line {
		t.Fatalf("untouched valuable chunk must pass verbatim:\n got %q\nwant %q", buf.String(), line)
	}
}

func TestPassthroughReprefixesDataWithoutSpace(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	raw := `{"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`
	processAll(t, r, "data:"+raw)
	if buf.String() != "data: "+raw+"\n" {
		t.Fatalf("got %q, want the canonical data: prefix re-added", buf.String())
	}
}

func TestPassthroughInjectsMissingChunkFields(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, `data: {"id":"chatcmpl-12345678","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`)
	frame := buf.String()
	if !strings.HasSuffix(frame, "}\n") || strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("re-serialized line must end with exactly one newline: %q", frame)
	}
	got := payloadOf(t, frame)
	if got["object"] != "chat.completion.chunk" {
		t.Fatalf("object must be injected: %v", got["object"])
	}
	if created := getNum(t, got, "created"); created < 1600000000 {
		t.Fatalf("created must be injected (seconds): %v", created)
	}
}

func TestPassthroughStripsAzureFieldsAndEmptyToolCalls(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, `data: {"id":"chatcmpl-12345678","prompt_filter_results":[],"choices":[{"index":0,"delta":{"content":"x","tool_calls":[]},"content_filter_results":{"hate":{}}}]}`)
	got := payloadOf(t, buf.String())
	if _, has := got["prompt_filter_results"]; has {
		t.Fatal("prompt_filter_results must be stripped")
	}
	choice := got["choices"].([]any)[0].(map[string]any)
	if _, has := choice["content_filter_results"]; has {
		t.Fatal("choice.content_filter_results must be stripped")
	}
	delta := choice["delta"].(map[string]any)
	if _, has := delta["tool_calls"]; has {
		t.Fatal("empty delta.tool_calls must be stripped")
	}
	if delta["content"] != "x" {
		t.Fatalf("real delta fields must survive: %v", delta)
	}
}

func TestPassthroughFixesInvalidIdOnly(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, `data: {"id":"chat","extend_fields":{"requestId":"req-9"},"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`)
	if got := payloadOf(t, buf.String()); got["id"] != "chatcmpl-req-9" {
		t.Fatalf("id = %v, want chatcmpl-req-9", got["id"])
	}
	buf.Reset()
	good := contentChunk + "\n"
	processAll(t, r, good)
	if buf.String() != good {
		t.Fatalf("valid id must pass through verbatim, got %q", buf.String())
	}
}

func TestPassthroughDropsNonValuableAndGarbage(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	empty := `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":null}]}`
	processAll(t, r, empty, "data: <html>502 Bad Gateway</html>", "event: ping")
	if buf.String() != "event: ping\n" {
		t.Fatalf("empty deltas and unparsable data lines must produce no output, got %q", buf.String())
	}
}

func TestPassthroughFinishChunkGetsEstimatedUsage(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, contentChunk, finishChunk)
	frames := dataFrames(t, buf.String())
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d: %q", len(frames), buf.String())
	}
	usage, has := frames[1]["usage"].(map[string]any)
	if !has {
		t.Fatalf("finish chunk must carry the injected estimate: %v", frames[1])
	}
	if usage["prompt_tokens"] != 2004.0 || usage["completion_tokens"] != 3.0 ||
		usage["total_tokens"] != 2007.0 || usage["estimated"] != true {
		t.Fatalf("estimate = %v, want prompt 2004 / completion 3 / total 2007 / estimated", usage)
	}
	// stream.js re-serializes an injected chunk with a single trailing newline.
	if !strings.HasSuffix(buf.String(), "}\n") || strings.HasSuffix(buf.String(), "\n\n") {
		t.Fatalf("injected line framing broken: %q", buf.String())
	}
}

func TestPassthroughCarriesUsageKeepsNumbersAndSynthesizes(t *testing.T) {
	var buf bytes.Buffer
	body := map[string]any{"reasoning_effort": "high"}
	r := NewPassthroughRelay(&buf, body, "m", FormatChat, nil)
	if !r.synthesis.Enabled {
		t.Fatal("reasoning_effort must enable synthesis")
	}
	line := `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110}}`
	processAll(t, r, line)
	usage := payloadOf(t, buf.String())["usage"].(map[string]any)
	if usage["prompt_tokens"] != 10.0 || usage["completion_tokens"] != 100.0 || usage["total_tokens"] != 110.0 {
		t.Fatalf("real numbers must be kept untouched: %v", usage)
	}
	details, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok || details["reasoning_tokens"] != 75.0 {
		t.Fatalf("synthesis must add floor(100*0.75)=75 reasoning tokens: %v", usage)
	}
}

func TestPassthroughFinishWithTrackedUsageBuffersIt(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	usageChunk := `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	processAll(t, r, contentChunk, usageChunk, finishChunk)
	frames := dataFrames(t, buf.String())
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d: %q", len(frames), buf.String())
	}
	usage, has := frames[2]["usage"].(map[string]any)
	if !has {
		t.Fatalf("finish chunk must carry the buffered tracked usage: %v", frames[2])
	}
	// AddBuffer: +2000 on prompt_tokens, total recomputed from the buffered
	// prompt plus the real completion (2010, 5, 2015).
	if usage["prompt_tokens"] != 2010.0 || usage["completion_tokens"] != 5.0 || usage["total_tokens"] != 2015.0 {
		t.Fatalf("buffered usage = %v, want 2010/5/2015", usage)
	}
	if _, has := usage["estimated"]; has {
		t.Fatalf("real usage must not carry the estimated marker: %v", usage)
	}
}

func TestPassthroughDoneLineForwarded(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	for _, c := range []struct{ in, want string }{
		{"data: [DONE]", "data: [DONE]\n"},
		{"data:[DONE]", "data: [DONE]\n"}, // re-prefixed
	} {
		before := buf.Len()
		processAll(t, r, c.in)
		if got := buf.String()[before:]; got != c.want {
			t.Fatalf("ProcessLine(%q) wrote %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPassthroughFlushAlwaysEmitsDoneAndFinalizes(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "data: [DONE]\n\n" {
		t.Fatalf("flush must always emit the sentinel, got %q", buf.String())
	}
	if r.FinalUsage != nil {
		t.Fatalf("nothing seen means no final usage, got %v", r.FinalUsage)
	}
	if r.FinalContent != "" || r.FinalThinking != "" {
		t.Fatalf("final content/thinking must be empty, got %q/%q", r.FinalContent, r.FinalThinking)
	}
}

func TestPassthroughFinalContentThinkingAndEstimate(t *testing.T) {
	r, _ := newPassthrough(t, estBody, FormatChat, nil)
	thinking := `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"thought"}}]}`
	processAll(t, r, contentChunk, thinking)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if r.FinalContent != "hello world!" {
		t.Fatalf("FinalContent = %q", r.FinalContent)
	}
	if r.FinalThinking != "thought" {
		t.Fatalf("FinalThinking = %q", r.FinalThinking)
	}
	// No real usage ever arrived, so the stats-side final usage is the
	// estimate over BOTH accumulated shapes: 12 content chars + 7 reasoning
	// chars = 19 → floor(19/4) = 4 output tokens (stream.js:318-325,
	// estimateOutputTokens).
	if r.FinalUsage["estimated"] != true || r.FinalUsage["completion_tokens"] != 4.0 {
		t.Fatalf("FinalUsage = %v, want the estimate over content + thinking", r.FinalUsage)
	}
}

func TestPassthroughResponsesFormatForwardsEventsAndTracksUsage(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatResponses, nil)
	created := `{"type":"response.created","response":{"id":"resp_x","created_at":1700000000}}`
	delta := `{"type":"response.output_text.delta","delta":"hello world!"}`
	completed := `{"type":"response.completed","response":{"id":"resp_x","status":"completed","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3}}}}`
	processAll(t, r,
		"event: response.created",
		"data: "+created,
		"data: "+delta,
		"data: "+completed,
		"")
	want := "event: response.created\n" +
		"data: " + created + "\n" +
		"data: " + delta + "\n" +
		"data: " + completed + "\n" +
		"\n"
	if buf.String() != want {
		t.Fatalf("responses events must flow through untouched:\n got %q\nwant %q", buf.String(), want)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "data: [DONE]\n\n") {
		t.Fatalf("flush must append the sentinel, got %q", buf.String())
	}
	// Tracked usage from the terminal event; the stats side keeps canonical names.
	if r.FinalUsage["prompt_tokens"] != 10.0 || r.FinalUsage["completion_tokens"] != 4.0 {
		t.Fatalf("FinalUsage = %v, want input/output 10/4", r.FinalUsage)
	}
	if r.FinalUsage["cached_tokens"] != 3.0 {
		t.Fatalf("cached_tokens must be tracked: %v", r.FinalUsage)
	}
}

func TestPassthroughResponsesEstimateRenamedToResponsesShape(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatResponses, nil)
	processAll(t, r, contentChunk, finishChunk)
	usage, has := payloadOf(t, lastDataLine(t, buf.String()))["usage"].(map[string]any)
	if !has {
		t.Fatal("finish chunk must carry the injected estimate")
	}
	if usage["input_tokens"] != 2004.0 || usage["output_tokens"] != 3.0 || usage["estimated"] != true {
		t.Fatalf("responses estimate = %v, want input 2004 / output 3 / estimated", usage)
	}
	for _, banned := range []string{"prompt_tokens", "total_tokens", "completion_tokens"} {
		if _, has := usage[banned]; has {
			t.Fatalf("convertUsageForFormat must rename away %q: %v", banned, usage)
		}
	}
}

// Parity: stream.js:332 evaluates terminality through
// getOpenAIResponsesEventName, whose chunk.type fallback makes a bare
// `data: {"type":"response.completed",...}` line terminal even with no
// preceding `event:` line — finalizeStream() runs and the final usage is
// captured immediately.
func TestPassthroughTerminalByTypeOnlyFinalizes(t *testing.T) {
	r, _ := newPassthrough(t, estBody, FormatResponses, nil)
	line := `data: {"type":"response.completed","response":{"id":"resp_x","status":"completed","usage":{"input_tokens":10,"output_tokens":4}}}`
	processAll(t, r, line)
	if r.FinalUsage == nil {
		t.Fatalf("JS finalizes on the type-only terminal event (FinalUsage captured); got nil")
	}
}

// Parity: stream.js:341 injects the estimate whenever the finish chunk carries
// no usage and nothing was tracked — with NO accumulated-content gate.
// Tool-call-only streams never add to totalContentLength (only
// content/reasoning_content do), yet still get the estimate.
func TestPassthroughToolCallFinishStillGetsEstimate(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	line := `data: {"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	processAll(t, r, line)
	fin := payloadOf(t, lastDataLine(t, buf.String()))
	if _, has := fin["usage"]; !has {
		t.Fatalf("JS injects estimated usage into a content-less finish chunk; got %v", fin)
	}
}

// Parity (fixed): stream.js:279-280 gate the missing-field injection on JS
// FALSY checks — `"object": null / "" / 0` and `"created": 0 / null / ""` are
// all rewritten — while the gate itself is a PRESENCE check
// (parsed.choices !== undefined, stream.js:278).
func TestPassthroughFalsyMissingFieldsRewritten(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
	}{
		{"null object", `{"id":"chatcmpl-12345678","object":null,"created":1700000000,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"empty object string", `{"id":"chatcmpl-12345678","object":"","created":1700000000,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"zero object number", `{"id":"chatcmpl-12345678","object":0,"created":1700000000,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"false object bool", `{"id":"chatcmpl-12345678","object":false,"created":1700000000,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"zero created", `{"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":0,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"null created", `{"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":null,"choices":[{"index":0,"delta":{"content":"x"}}]}`},
		{"empty created string", `{"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":"","choices":[{"index":0,"delta":{"content":"x"}}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, buf := newPassthrough(t, estBody, FormatChat, nil)
			processAll(t, r, "data: "+c.chunk)
			got := payloadOf(t, buf.String())
			if got["object"] != "chat.completion.chunk" {
				t.Fatalf("object = %v, want the injected chat.completion.chunk", got["object"])
			}
			if created := getNum(t, got, "created"); created < 1600000000 {
				t.Fatalf("created = %v, want now-in-seconds", created)
			}
			if delta := got["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any); delta["content"] != "x" {
				t.Fatalf("chunk payload must survive: %v", got)
			}
		})
	}
}

// Truthy object/created are never overwritten — the chunk stays byte-identical
// (no mutation, no re-serialization; stream.js:279-280) — and `"choices": null`
// still enters the injection because the gate is a presence check
// (stream.js:278).
func TestPassthroughTruthyFieldsAndNullChoicesGate(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	line := `data: {"id":"chatcmpl-12345678","object":"data.chunk","created":1700000000,"choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n"
	processAll(t, r, line)
	if buf.String() != line {
		t.Fatalf("truthy object/created must stay verbatim, got %q", buf.String())
	}

	r2, buf2 := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r2, `data: {"id":"chatcmpl-12345678","choices":null}`)
	got := payloadOf(t, buf2.String())
	if _, has := got["choices"]; !has {
		t.Fatalf("choices:null must survive (presence gate, not a strip): %v", got)
	}
	if got["object"] != "chat.completion.chunk" {
		t.Fatalf("choices:null must still get the injected object field: %v", got)
	}
	if created := getNum(t, got, "created"); created < 1600000000 {
		t.Fatalf("created = %v, want injected", created)
	}
}

// Parity (fixed): stream.js:272 JSON.parse accepts scalars — a data line
// carrying a number, string, array or bool parses, skips every mutation guard
// (hasValuableContent falls through to true, streamHelpers.js:66) and is
// re-emitted verbatim through the !injectedUsage tail (stream.js:372-378).
// A JSON `null` payload is the exception: parsed.id throws inside fixInvalidId
// (streamHelpers.js:71) and the catch drops the line (stream.js:364-369).
func TestPassthroughNonObjectJSONForwarded(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"number", "data: 42", "data: 42\n"},
		{"number without space", "data:42", "data: 42\n"},
		{"string", `data: "x"`, "data: \"x\"\n"},
		{"array", "data: [1,2]", "data: [1,2]\n"},
		{"bool", "data: false", "data: false\n"},
		{"empty array", "data: []", "data: []\n"},
		{"empty object stays verbatim too", `data: {}`, "data: {}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, buf := newPassthrough(t, estBody, FormatChat, nil)
			processAll(t, r, c.in)
			if buf.String() != c.want {
				t.Fatalf("ProcessLine(%q) wrote %q, want %q", c.in, buf.String(), c.want)
			}
			if r.FinalUsage != nil {
				t.Fatalf("a non-object chunk must leave no tracked usage, got %v", r.FinalUsage)
			}
		})
	}
	t.Run("null payload dropped", func(t *testing.T) {
		r, buf := newPassthrough(t, estBody, FormatChat, nil)
		processAll(t, r, "data: null")
		if buf.Len() != 0 {
			t.Fatalf("data: null must be dropped (JS throws on parsed.id), wrote %q", buf.String())
		}
	})
}

// Parity (fixed): stream.js:288-309 iterate parsed.choices with for..of — a
// truthy choices that is neither an array nor a string throws TypeError,
// caught at stream.js:364 → continue, so the WHOLE chunk is dropped (no seam,
// no emit). Strings iterate harmlessly per character; falsy choices never
// reach the loops (stream.js:288 parsed?.choices).
func TestPassthroughNonIterableChoicesDropped(t *testing.T) {
	full := `"id":"chatcmpl-12345678","object":"chat.completion.chunk","created":1700000000,"model":"m"`
	cases := []struct {
		name    string
		line    string
		dropped bool
		want    string // used when not dropped (verbatim forward)
	}{
		{"object choices dropped", `data: {` + full + `,"choices":{}}`, true, ""},
		{"number choices dropped", `data: {` + full + `,"choices":5}`, true, ""},
		{"bool choices dropped", `data: {` + full + `,"choices":true}`, true, ""},
		{"string choices forwarded", `data: {` + full + `,"choices":"abc"}`, false, `data: {` + full + `,"choices":"abc"}` + "\n"},
		{"falsy number choices forwarded", `data: {` + full + `,"choices":0}`, false, `data: {` + full + `,"choices":0}` + "\n"},
		{"null choices forwarded", `data: {` + full + `,"choices":null}`, false, `data: {` + full + `,"choices":null}` + "\n"},
		{"array of scalars forwarded", `data: {` + full + `,"choices":[1,2]}`, false, `data: {` + full + `,"choices":[1,2]}` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, buf := newPassthrough(t, estBody, FormatChat, nil)
			processAll(t, r, c.line)
			if c.dropped {
				if buf.Len() != 0 {
					t.Fatalf("non-iterable choices must drop the whole chunk, wrote %q", buf.String())
				}
				if r.FinalUsage != nil {
					t.Fatalf("a dropped chunk must leave no tracked usage, got %v", r.FinalUsage)
				}
				return
			}
			if buf.String() != c.want {
				t.Fatalf("got %q, want %q", buf.String(), c.want)
			}
		})
	}
}

// Parity (fixed): the passthrough re-emit is bare `data: ${JSON.stringify(parsed)}\n`
// (stream.js:346/351/358/361) — cleanUsagePayload is formatSSE-only
// (streamHelpers.js:119) — so an Azure-style `"usage": null` and
// `usage.perf_metrics: null` survive a re-serialized chunk.
func TestPassthroughReemitKeepsNullUsageFields(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	// A repaired id forces the re-serialization path (stream.js:360-362).
	processAll(t, r, `data: {"id":"chat","usage":null,"choices":[{"index":0,"delta":{"content":"x"}}]}`)
	frame := buf.String()
	if !strings.Contains(frame, `"usage":null`) {
		t.Fatalf("bare re-emit must keep usage:null (stream.js:361), got %q", frame)
	}
	// A repaired id (chatcmpl-… base36 fallback) and single-\n framing.
	if !strings.Contains(frame, `"id":"chatcmpl-`) || !strings.HasSuffix(frame, "}\n") || strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("bare re-emit framing broken: %q", frame)
	}

	r2, buf2 := newPassthrough(t, estBody, FormatChat, nil)
	// A valid usage object takes the carriesUsage re-emit (stream.js:349-352).
	processAll(t, r2, `data: {"id":"chat","usage":{"prompt_tokens":5,"perf_metrics":null},"choices":[{"index":0,"delta":{"content":"x"}}]}`)
	frame2 := buf2.String()
	if !strings.Contains(frame2, `"perf_metrics":null`) {
		t.Fatalf("bare re-emit must keep usage.perf_metrics:null, got %q", frame2)
	}
	if !strings.Contains(frame2, `"prompt_tokens":5`) {
		t.Fatalf("real usage numbers must survive: %q", frame2)
	}
}

// Parity (fixed): on the responses→responses route (JS runs translate mode
// with keepsOpenAIResponsesFormat there) a [DONE] sentinel arriving without a
// prior terminal event makes the done-branch synthesize response.failed
// immediately BEFORE the sentinel (stream.js:406-424) and mark the stream
// done, so Flush adds nothing more (stream.js:618-624).
func TestPassthroughResponsesDoneSynthesizesFailureBeforeSentinel(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatResponses, nil)
	processAll(t, r, `data: {"type":"response.output_text.delta","delta":"partial"}`, "data: [DONE]")
	out := buf.String()
	failedAt, doneAt := strings.Index(out, "event: response.failed"), strings.Index(out, "data: [DONE]")
	if failedAt < 0 || doneAt < 0 || failedAt > doneAt {
		t.Fatalf("the failed frame must precede the sentinel, got %q", out)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	if n := strings.Count(out, "data: [DONE]"); n != 1 {
		t.Fatalf("the sentinel must be emitted exactly once, got %d in %q", n, out)
	}
	if n := strings.Count(out, "event: response.failed"); n != 1 {
		t.Fatalf("the failed frame must be emitted exactly once, got %d in %q", n, out)
	}
}

// Parity: a responses→responses stream that ends without a terminal event and
// without a [DONE] (clean EOF) gets the failed frame + sentinel from flush
// (stream.js:609-624).
func TestPassthroughResponsesCleanEOFSynthesizesFailure(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatResponses, nil)
	processAll(t, r, `data: {"type":"response.output_text.delta","delta":"partial"}`)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	failedAt, doneAt := strings.Index(out, "event: response.failed"), strings.Index(out, "data: [DONE]")
	if failedAt < 0 || doneAt < 0 || failedAt > doneAt {
		t.Fatalf("flush must emit the failed frame before the sentinel, got %q", out)
	}
	if strings.Count(out, "event: response.failed") != 1 || strings.Count(out, "data: [DONE]") != 1 {
		t.Fatalf("exactly one failed frame + one sentinel expected, got %q", out)
	}
}

// A terminal event suppresses the failed synthesis — the stream just ends
// with the sentinel (stream.js:408/611 gate on openAIResponsesTerminalSeen).
func TestPassthroughResponsesTerminalSeenNoFailure(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatResponses, nil)
	processAll(t, r,
		`data: {"type":"response.completed","response":{"id":"resp_x","status":"completed","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}`,
		"data: [DONE]")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "response.failed") {
		t.Fatalf("a terminal event must suppress the failed synthesis, got %q", buf.String())
	}
	if n := strings.Count(buf.String(), "data: [DONE]"); n != 1 {
		t.Fatalf("exactly one sentinel expected, got %d in %q", n, buf.String())
	}
}

// Chat passthrough keeps the historical double [DONE]: a raw-forwarded
// sentinel never sets streamDoneSent (stream.js:372-378 — passthrough mode
// has no done-branch), so flush emits the canonical one again
// (stream.js:539-543) and no failed frame is ever synthesized.
func TestPassthroughChatDoubleDoneUnchanged(t *testing.T) {
	r, buf := newPassthrough(t, estBody, FormatChat, nil)
	processAll(t, r, contentChunk, "data: [DONE]")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), contentChunk+"\ndata: [DONE]\ndata: [DONE]\n\n") {
		t.Fatalf("want forwarded chunk + forwarded sentinel + flush sentinel, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "response.failed") {
		t.Fatalf("chat passthrough must never synthesize the failed frame, got %q", buf.String())
	}
}

// Parity (fixed): the passthrough tail forwards the residual buffer RAW with
// only the "data:"-prefix fix — NO trailing newline, no parse, no seam
// (stream.js:523-531).
func TestPassthroughProcessTailRaw(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"truncated chunk", `data: {"id":"chatcmpl-12345678","object":"chat.completion.`, `data: {"id":"chatcmpl-12345678","object":"chat.completion.`},
		{"prefix fix, no newline", `data:{"partial":1`, `data: {"partial":1`},
		{"canonical prefix untouched", `data: {"partial":1`, `data: {"partial":1`},
		{"non-data line", `event: response.created`, `event: response.created`},
		{"sentinel tail forwards raw (chat)", `data: [DONE]`, `data: [DONE]`},
		{"empty buffer writes nothing", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, buf := newPassthrough(t, estBody, FormatChat, nil)
			if err := r.ProcessTail(c.in); err != nil {
				t.Fatal(err)
			}
			if buf.String() != c.want {
				t.Fatalf("ProcessTail(%q) wrote %q, want %q", c.in, buf.String(), c.want)
			}
			if err := r.Flush(); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(buf.String(), "data: [DONE]\n\n") {
				t.Fatalf("flush must still terminate the stream, got %q", buf.String())
			}
		})
	}
}

// The responses→responses exception: JS's translate flush parses the residual
// buffer with parseSSELine (stream.js:553), so a tail that parses as the
// [DONE] sentinel triggers the failed synthesis (stream.js:610-624); any
// other tail forwards raw — a documented adaptation boundary (JS would
// re-frame it through the translator).
func TestPassthroughResponsesProcessTail(t *testing.T) {
	t.Run("sentinel tail synthesizes failure before the sentinel", func(t *testing.T) {
		r, buf := newPassthrough(t, estBody, FormatResponses, nil)
		if err := r.ProcessTail("data: [DONE]"); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		failedAt, doneAt := strings.Index(out, "event: response.failed"), strings.Index(out, "data: [DONE]")
		if failedAt < 0 || doneAt < 0 || failedAt > doneAt {
			t.Fatalf("tail sentinel must synthesize the failed frame first, got %q", out)
		}
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}
		if strings.Count(buf.String(), "data: [DONE]") != 1 || strings.Count(buf.String(), "event: response.failed") != 1 {
			t.Fatalf("flush must add nothing after the tail sentinel, got %q", buf.String())
		}
	})
	t.Run("sentinel tail after a terminal emits no failure", func(t *testing.T) {
		r, buf := newPassthrough(t, estBody, FormatResponses, nil)
		processAll(t, r, `data: {"type":"response.completed","response":{"id":"resp_x","status":"completed"}}`)
		before := buf.Len()
		if err := r.ProcessTail("data:[DONE]"); err != nil {
			t.Fatal(err)
		}
		// stream.js:617-619 — the flush tail emits the canonical sentinel.
		if got := buf.String()[before:]; got != "data: [DONE]\n\n" {
			t.Fatalf("tail sentinel must emit the canonical sentinel after a terminal, got %q", got)
		}
		if strings.Contains(buf.String(), "response.failed") {
			t.Fatalf("no failed frame expected, got %q", buf.String())
		}
	})
	t.Run("non-sentinel tail forwards raw", func(t *testing.T) {
		r, buf := newPassthrough(t, estBody, FormatResponses, nil)
		tail := `{"type":"response.output_text.delta","delta":"trun`
		if err := r.ProcessTail(tail); err != nil {
			t.Fatal(err)
		}
		if buf.String() != tail {
			t.Fatalf("non-sentinel tail must forward raw without a newline, got %q", buf.String())
		}
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if n := strings.Count(out, "event: response.failed"); n != 1 {
			t.Fatalf("flush must close the stream with the failed frame, got %d in %q", n, out)
		}
		if !strings.HasSuffix(out, "data: [DONE]\n\n") {
			t.Fatalf("flush must close the stream with the sentinel, got %q", out)
		}
	})
}

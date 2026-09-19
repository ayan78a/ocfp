package relay

// Tests for TranslateRelay — stream.js STREAM_MODE.TRANSLATE (upstream format
// ≠ client format) plus FormatIncompleteResponsesFailure
// (responsesStreamHelpers.js formatIncompleteOpenAIResponsesStreamFailure).
//
// The underlying state machines have their own parity suites in
// internal/translate; these tests cover the relay wrapper: line parsing,
// accumulation, the usage seam, framing, and flush behaviour.

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func newTranslate(t *testing.T, source Format, responseID string) (*TranslateRelay, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewTranslateRelay(&buf, estBody, "m", source, responseID, nil), &buf
}

func processTranslate(t *testing.T, r *TranslateRelay, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if err := r.ProcessLine(line); err != nil {
			t.Fatalf("ProcessLine(%q): %v", line, err)
		}
	}
}

func eventNames(evs []sseEvent) []string {
	names := make([]string, len(evs))
	for i, ev := range evs {
		names[i] = ev.name
	}
	return names
}

// Responses upstream → chat client (case 2): deltas become chat chunks and the
// terminal response.completed becomes the finish chunk carrying usage.
func TestTranslateResponsesToChat(t *testing.T) {
	r, buf := newTranslate(t, FormatChat, "")
	processTranslate(t, r,
		`data: {"type":"response.created","response":{"id":"resp_x"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		`data: {"type":"response.completed","response":{"id":"resp_x","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":3}}}}`,
	)
	frames := dataFrames(t, buf.String())
	if len(frames) != 3 {
		t.Fatalf("want 3 chat chunks (2 deltas + final), got %d:\n%s", len(frames), buf.String())
	}
	for i, want := range []string{"Hello", " world"} {
		choice := frames[i]["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if delta["content"] != want {
			t.Fatalf("chunk %d content = %v, want %q", i, delta["content"], want)
		}
		if choice["finish_reason"] != nil {
			t.Fatalf("chunk %d must not finish yet: %v", i, choice["finish_reason"])
		}
	}
	fin := frames[2]["choices"].([]any)[0].(map[string]any)
	if fin["finish_reason"] != "stop" {
		t.Fatalf("final finish_reason = %v, want stop", fin["finish_reason"])
	}
	usage := frames[2]["usage"].(map[string]any)
	// buildUsage carries the cache split: input_tokens_details.cached_tokens →
	// prompt_tokens_details.cached_tokens.
	if usage["prompt_tokens"] != 10.0 || usage["completion_tokens"] != 5.0 || usage["total_tokens"] != 15.0 {
		t.Fatalf("final usage = %v, want 10/5/15", usage)
	}
	if details := usage["prompt_tokens_details"].(map[string]any); details["cached_tokens"] != 3.0 {
		t.Fatalf("cached split = %v, want 3", usage["prompt_tokens_details"])
	}
	// One chat identity across the stream.
	if id, _ := frames[0]["id"].(string); id == "" || id != frames[2]["id"] {
		t.Fatalf("chunks must share the chat id: %v vs %v", frames[0]["id"], frames[2]["id"])
	}
	if frames[0]["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v", frames[0]["object"])
	}

	// Translate mode never sends [DONE] to a chat client — flush stays silent
	// once the terminal event already finalized the stream.
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "[DONE]") {
		t.Fatal("translate mode must not emit data: [DONE]")
	}
	if len(dataFrames(t, buf.String())) != 3 {
		t.Fatal("flush after a completed event must add no chunk")
	}
	// Stats side keeps the canonical field names from the terminal usage.
	if r.FinalUsage["prompt_tokens"] != 10.0 || r.FinalUsage["completion_tokens"] != 5.0 || r.FinalUsage["cached_tokens"] != 3.0 {
		t.Fatalf("FinalUsage = %v, want prompt 10 / completion 5 / cached 3", r.FinalUsage)
	}
	// stream.js parity quirk, faithfully ported: the translate accumulator only
	// reads Claude (delta.text), OpenAI-chat (choices[0].delta) and Gemini
	// shapes — a responses upstream's string-`delta` events never feed it, so
	// the stats side loses the content of a responses→chat relay
	// (stream.js:423-455).
	if r.FinalContent != "" {
		t.Fatalf("FinalContent = %q, want empty (responses upstreams never accumulate)", r.FinalContent)
	}
}

// A stream that dies before response.completed still ends with the flush-time
// final chunk — but WITHOUT usage: the estimateUsage seam is gated on
// totalContentLength > 0, and the translate accumulator only reads
// Claude/OpenAI-chat/Gemini shapes, never a responses upstream's string-delta
// events (stream.js:187, 423-455). Faithfully ported, so both engines leave
// the final chunk bare here.
func TestTranslateResponsesToChatFlushFinalChunkCarriesNoUsage(t *testing.T) {
	r, buf := newTranslate(t, FormatChat, "")
	processTranslate(t, r,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_text.delta","delta":" world!"}`,
	)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	frames := dataFrames(t, buf.String())
	if len(frames) != 3 {
		t.Fatalf("want 2 deltas + the flush final chunk, got %d:\n%s", len(frames), buf.String())
	}
	fin := frames[2]["choices"].([]any)[0].(map[string]any)
	if fin["finish_reason"] != "stop" {
		t.Fatalf("flush must finalize with stop, got %v", fin["finish_reason"])
	}
	if _, has := frames[2]["usage"]; has {
		t.Fatalf("flush final chunk must stay usage-less (totalLen stays 0 for responses upstreams): %v", frames[2])
	}
	if r.FinalUsage != nil || r.FinalContent != "" {
		t.Fatalf("final state = %v / %q, want nothing accumulated", r.FinalUsage, r.FinalContent)
	}
	if strings.Contains(buf.String(), "[DONE]") {
		t.Fatal("translate mode must not emit data: [DONE]")
	}
}

// Chat upstream → responses client (case 3b): each chunk becomes framed
// `event:` + `data:` blocks with running sequence numbers, opening with the
// JS first-chunk scaffold (response.created + response.in_progress) and
// closing on the finish chunk with response.completed.
func TestTranslateChatToResponses(t *testing.T) {
	r, buf := newTranslate(t, FormatResponses, "chatcmpl-x")
	processTranslate(t, r,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","created":1700000000,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	evs := events(t, buf.String())
	wantNames := []string{
		"response.created", "response.in_progress", // JS first-chunk scaffold (bug #4 fixed)
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if len(evs) != len(wantNames) {
		t.Fatalf("want %d events, got %d: %v", len(wantNames), len(evs), eventNames(evs))
	}
	for i, ev := range evs {
		if ev.name != wantNames[i] {
			t.Fatalf("event %d = %s, want %s", i, ev.name, wantNames[i])
		}
		if got := getNum(t, ev.data, "sequence_number"); got != float64(i+1) {
			t.Fatalf("event %d sequence_number = %v, want %d", i, got, i+1)
		}
		if ev.data["type"] != wantNames[i] {
			t.Fatalf("event %d data.type = %v", i, ev.data["type"])
		}
	}
	// The message scaffold derives its ids from resp_<upstream chat id>.
	// Indexes follow wantNames: the first-chunk scaffold occupies events 0-1,
	// so the data-bearing events start at 2.
	item0 := evs[2].data["item"].(map[string]any)
	if item0["id"] != "msg_resp_chatcmpl-x_0" || item0["type"] != "message" || item0["role"] != "assistant" {
		t.Fatalf("output_item.added item = %v", item0)
	}
	if d := evs[4].data["delta"]; d != "Hello" {
		t.Fatalf("first delta = %v", d)
	}
	if d := evs[5].data["delta"]; d != " world" {
		t.Fatalf("second delta = %v", d)
	}
	if txt := evs[6].data["text"]; txt != "Hello world" {
		t.Fatalf("output_text.done text = %v", txt)
	}
	doneItem := evs[8].data["item"].(map[string]any)
	content := doneItem["content"].([]any)[0].(map[string]any)
	if content["type"] != "output_text" || content["text"] != "Hello world" {
		t.Fatalf("output_item.done content = %v", content)
	}
	completed := evs[9].data["response"].(map[string]any)
	if completed["id"] != "resp_chatcmpl-x" || completed["status"] != "completed" {
		t.Fatalf("response.completed = %v", completed)
	}
	if strings.Contains(buf.String(), "[DONE]") {
		t.Fatal("responses clients of translate mode get no [DONE] either")
	}

	// The state machine already completed — flush emits nothing more.
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if after := events(t, buf.String()); len(after) != len(wantNames) {
		t.Fatalf("flush after completed emitted %d extra events", len(after)-len(wantNames))
	}
}

// A stream cut before any finish_reason still closes cleanly at flush: open
// items close and the state machine synthesizes response.completed.
func TestTranslateChatToResponsesSynthesizesCompletedOnFlush(t *testing.T) {
	r, buf := newTranslate(t, FormatResponses, "chatcmpl-x")
	processTranslate(t, r,
		`data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"partial"}}]}`)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	evs := events(t, buf.String())
	wantNames := []string{
		"response.created", "response.in_progress", // JS first-chunk scaffold
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if len(evs) != len(wantNames) {
		t.Fatalf("want %d events, got %d: %v", len(wantNames), len(evs), eventNames(evs))
	}
	for i, ev := range evs {
		if ev.name != wantNames[i] {
			t.Fatalf("event %d = %s, want %s", i, ev.name, wantNames[i])
		}
	}
	completed := evs[len(evs)-1].data["response"].(map[string]any)
	if completed["status"] != "completed" {
		t.Fatalf("flush must complete the response, got %v", completed)
	}
	if r.FinalContent != "partial" {
		t.Fatalf("FinalContent = %q", r.FinalContent)
	}
}

// Real usage flows to the stats side only: framed responses events never carry
// a usage object (the seam keys off usage/choices/response at the item level,
// which framed {event,data} wrappers do not expose — stream.js parity).
func TestTranslateChatToResponsesTrackedUsageStaysStatsSide(t *testing.T) {
	r, buf := newTranslate(t, FormatResponses, "chatcmpl-x")
	// Usage-only chunk: tracked for stats, emits nothing (no choices to
	// translate).
	processTranslate(t, r,
		`data: {"id":"chatcmpl-x","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	if buf.Len() != 0 {
		t.Fatalf("usage-only chunk must emit nothing, wrote %q", buf.String())
	}
	processTranslate(t, r,
		`data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		`data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	evs := events(t, buf.String())
	if len(evs) == 0 {
		t.Fatal("expected framed events")
	}
	for i, ev := range evs {
		if _, has := ev.data["usage"]; has {
			t.Fatalf("event %d (%s) must not carry usage", i, ev.name)
		}
		if resp, is := ev.data["response"].(map[string]any); is {
			if _, has := resp["usage"]; has {
				t.Fatalf("event %d (%s) must not embed response usage", i, ev.name)
			}
		}
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if r.FinalUsage["prompt_tokens"] != 10.0 || r.FinalUsage["completion_tokens"] != 5.0 {
		t.Fatalf("FinalUsage = %v, want the tracked 10/5", r.FinalUsage)
	}
}

// An upstream error event becomes a single error chat chunk and ends the
// translation (no further chunks, no [DONE]).
func TestTranslateResponsesErrorBecomesErrorChunk(t *testing.T) {
	r, buf := newTranslate(t, FormatChat, "")
	processTranslate(t, r, `data: {"type":"error","error":{"message":"boom"}}`)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	frames := dataFrames(t, buf.String())
	if len(frames) != 1 {
		t.Fatalf("want exactly the error chunk, got %d:\n%s", len(frames), buf.String())
	}
	choice := frames[0]["choices"].([]any)[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["content"] != "[Error] boom" || choice["finish_reason"] != "stop" {
		t.Fatalf("error chunk = %v", choice)
	}
}

func TestTranslateSkipsDoneSentinel(t *testing.T) {
	for _, source := range []Format{FormatChat, FormatResponses} {
		r, buf := newTranslate(t, source, "chatcmpl-x")
		processTranslate(t, r, "data: [DONE]", "event: upstream.only", "", ": comment")
		if buf.Len() != 0 {
			t.Fatalf("source %v: sentinel and non-data lines must be swallowed, wrote %q", source, buf.String())
		}
	}
}

// FormatIncompleteResponsesFailure is the synthesized failure frame for
// streams that closed without a terminal event.
func TestFormatIncompleteResponsesFailure(t *testing.T) {
	got := FormatIncompleteResponsesFailure()
	if !strings.HasPrefix(got, "event: response.failed\ndata: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("framing = %q", got)
	}
	dataLine := strings.Split(strings.TrimSuffix(got, "\n\n"), "\n")[1]
	payload := decodeData(t, dataLine)
	if payload["type"] != "response.failed" {
		t.Fatalf("type = %v", payload["type"])
	}
	resp := payload["response"].(map[string]any)
	if resp["status"] != "failed" {
		t.Fatalf("status = %v", resp["status"])
	}
	id, _ := resp["id"].(string)
	// JS builds `resp_${Date.now()}` — decimal millis, exactly what the port
	// emits (translate.go FormatIncompleteResponsesFailure).
	if !regexp.MustCompile(`^resp_[0-9a-z]+$`).MatchString(id) {
		t.Fatalf("id = %q, want resp_<millis>", id)
	}
	errObj := resp["error"].(map[string]any)
	if errObj["type"] != "stream_error" || errObj["code"] != "stream_disconnected" ||
		errObj["message"] != "stream closed before response.completed" {
		t.Fatalf("error = %v", errObj)
	}
}

// Parity (fixed): the translate tail runs the SAME parse + transform as the
// loop — stream.js:549-586 parses the residual buffer with parseSSELine and
// pushes it through translateResponse + the usage seam — so ProcessTail is
// ProcessLine. The [DONE] sentinel stays swallowed in a tail too, and the
// closing items follow in Flush exactly like stream.js:588-626.
func TestTranslateProcessTailFollowsProcessLine(t *testing.T) {
	t.Run("responses upstream tail delta is translated", func(t *testing.T) {
		r, buf := newTranslate(t, FormatChat, "")
		if err := r.ProcessTail(`data: {"type":"response.output_text.delta","delta":"tail"}`); err != nil {
			t.Fatal(err)
		}
		frames := dataFrames(t, buf.String())
		if len(frames) != 1 {
			t.Fatalf("want the tail translated like a line, got %q", buf.String())
		}
		delta := frames[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if delta["content"] != "tail" {
			t.Fatalf("tail content = %v, want tail", delta["content"])
		}
	})
	t.Run("chat upstream tail chunk is translated", func(t *testing.T) {
		r, buf := newTranslate(t, FormatResponses, "chatcmpl-x")
		if err := r.ProcessTail(`data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"tail"}}]}`); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `"response.output_text.delta"`) {
			t.Fatalf("tail chunk must be translated, got %q", buf.String())
		}
	})
	t.Run("sentinel tail stays swallowed for both sources", func(t *testing.T) {
		for _, source := range []Format{FormatChat, FormatResponses} {
			r, buf := newTranslate(t, source, "chatcmpl-x")
			if err := r.ProcessTail("data: [DONE]"); err != nil {
				t.Fatal(err)
			}
			if err := r.Flush(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), "[DONE]") {
				t.Fatalf("source %v: translate mode must never emit [DONE]", source)
			}
		}
	})
	t.Run("unparsable tail is dropped like a line", func(t *testing.T) {
		r, buf := newTranslate(t, FormatChat, "")
		if err := r.ProcessTail(`data: {"type":"response.output_text.delta","delta":"trun`); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Fatalf("an unparsable tail must produce no output, wrote %q", buf.String())
		}
	})
}

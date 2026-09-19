package translate

// Tests for stream_resp2chat.go (RespToChatState) — the Responses SSE → Chat
// chunks state machine ported from
// 9router open-sse/translator/response/openai-responses.js
// openaiResponsesToOpenAIResponse.

import (
	"regexp"
	"testing"
	"time"

	"opencode-free-proxy/internal/jsonx"
)

// newRespState seeds a state with a frozen clock so ids/created are
// deterministic: chatcmpl-1700000000123 / created 1700000000.
func newRespState(model string) *RespToChatState {
	st := NewRespToChatState(model)
	st.now = func() time.Time { return time.UnixMilli(1700000000123) }
	return st
}

// feedResp runs raw SSE payloads (data frames, already JSON) through Convert
// and returns the non-nil chat chunks.
func feedResp(t *testing.T, st *RespToChatState, raw ...string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range raw {
		if c := st.Convert(jb(t, r)); c != nil {
			out = append(out, c)
		}
	}
	return out
}

func TestRespToChatFirstEventInitializesStateWithoutEmitting(t *testing.T) {
	// JS: response.created is not translated into a chat chunk — but it does
	// initialize chatId/created (openai-responses.js:443-456).
	st := newRespState("muse-spark-1.2")
	if c := st.Convert(jb(t, `{"type":"response.created","response":{"id":"resp_1"}}`)); c != nil {
		t.Fatalf("response.created must not emit a chunk: %s", js(c))
	}
	if !st.Started {
		t.Fatal("state must be started")
	}
	eq(t, "chat id", st.ChatID, "chatcmpl-1700000000123")
	eq(t, "created", st.Created, int64(1700000000))
}

func TestRespToChatTextDeltas(t *testing.T) {
	st := newRespState("muse-spark-1.2")
	chunks := feedResp(t, st,
		`{"type":"response.created","response":{}}`,
		`{"type":"response.output_text.delta","data":{"delta":"Hel"}}`,
		`{"type":"response.output_text.delta","data":{"delta":"lo"}}`,
		`{"type":"response.output_text.done","data":{"text":"Hello"}}`,
		`{"type":"response.output_text.delta","data":{"delta":""}}`,
	)
	if len(chunks) != 2 {
		t.Fatalf("want 2 content chunks, got %s", js(chunks))
	}
	want := `{"id":"chatcmpl-1700000000123","object":"chat.completion.chunk","created":1700000000,
		"model":"muse-spark-1.2","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`
	eq(t, "first chunk", chunks[0], jb(t, want))
	eq(t, "second delta", dig(t, chunks[1], "choices", 0, "delta", "content"), "lo")
}

func TestRespToChatFramedAndBareEventForms(t *testing.T) {
	// JS reads `chunk.type || chunk.event` and `chunk.data || chunk`, so both
	// the framed SSE form and a bare payload translate identically.
	st := newRespState("")
	chunks := feedResp(t, st,
		`{"event":"response.output_text.delta","delta":"bare"}`,
		`{"type":"response.output_text.delta","data":{"delta":"framed"}}`,
	)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	eq(t, "bare form", dig(t, chunks[0], "choices", 0, "delta", "content"), "bare")
	eq(t, "framed form", dig(t, chunks[1], "choices", 0, "delta", "content"), "framed")
	eq(t, "model fallback", dig(t, chunks[0], "model"), "unknown")
}

func TestRespToChatFunctionCallAddedAndArgs(t *testing.T) {
	t.Run("output_item.added announces the call at its assigned index", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{
			"type":"response.output_item.added",
			"data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":""}}
		}`)
		if len(chunks) != 1 {
			t.Fatalf("want 1 chunk, got %d", len(chunks))
		}
		eq(t, "tool_call", dig(t, chunks[0], "choices", 0, "delta", "tool_calls"), ja(t, `[{
			"index":0,"id":"call_abc","type":"function",
			"function":{"name":"get_weather","arguments":""}
		}]`))
	})

	t.Run("argument deltas ride the same index without an id", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st,
			`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
			`{"type":"response.function_call_arguments.delta","data":{"item_id":"fc_1","delta":"{\"city\":"}}`,
		)
		eq(t, "args delta", dig(t, chunks[1], "choices", 0, "delta", "tool_calls"), ja(t, `[
			{"index":0,"function":{"arguments":"{\"city\":"}}
		]`))
	})

	t.Run("done without prior deltas emits the full arguments once", func(t *testing.T) {
		st := newRespState("m")
		item := `{"type":"response.output_item.done","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f","arguments":"{\"city\":\"SF\"}"}}}`
		chunks := feedResp(t, st, item, item)
		if len(chunks) != 1 {
			t.Fatalf("done must emit exactly once, got %d chunks", len(chunks))
		}
		eq(t, "full args", dig(t, chunks[0], "choices", 0, "delta", "tool_calls", 0, "function", "arguments"), `{"city":"SF"}`)
	})

	t.Run("done after deltas is ignored", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st,
			`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
			`{"type":"response.function_call_arguments.delta","data":{"item_id":"fc_1","delta":"{}"}}`,
			`{"type":"response.output_item.done","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f","arguments":"{}"}}}`,
		)
		if len(chunks) != 2 {
			t.Fatalf("done must not re-emit args, got %d chunks", len(chunks))
		}
	})

	t.Run("custom_tool_call items take the same path", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st,
			`{"type":"response.output_item.added","data":{"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_c","name":"edit"}}}`,
			`{"type":"response.custom_tool_call_input.delta","data":{"item_id":"ctc_1","delta":"raw input"}}`,
		)
		eq(t, "added", dig(t, chunks[0], "choices", 0, "delta", "tool_calls", 0, "function", "name"), "edit")
		eq(t, "input delta", dig(t, chunks[1], "choices", 0, "delta", "tool_calls", 0, "function", "arguments"), "raw input")
	})

	t.Run("missing call_id falls back to call_<millis>", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{
			"type":"response.output_item.added",
			"data":{"item":{"id":"fc_9","type":"function_call","name":"f"}}
		}`)
		id := jsonx.AsStr(dig(t, chunks[0], "choices", 0, "delta", "tool_calls", 0, "id"))
		if !regexp.MustCompile(`^call_[0-9]+$`).MatchString(id) {
			t.Fatalf("fallback id = %q, want call_<millis>", id)
		}
	})
}

func TestRespToChatParallelCallsRouteByItemID(t *testing.T) {
	// openai-responses.js:456-502 — index is assigned at added-time keyed by
	// the server item id, so all-addeds-before-dones stays separate.
	st := newRespState("m")
	chunks := feedResp(t, st,
		`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
		`{"type":"response.output_item.added","data":{"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"g"}}}`,
		`{"type":"response.function_call_arguments.delta","data":{"item_id":"fc_2","delta":"B1"}}`,
		`{"type":"response.function_call_arguments.delta","data":{"item_id":"fc_1","delta":"A1"}}`,
	)
	if len(chunks) != 4 {
		t.Fatalf("want 4 chunks, got %d", len(chunks))
	}
	eq(t, "first added index", dig(t, chunks[0], "choices", 0, "delta", "tool_calls", 0, "index"), float64(0))
	eq(t, "second added index", dig(t, chunks[1], "choices", 0, "delta", "tool_calls", 0, "index"), float64(1))
	eq(t, "fc_2 delta index", dig(t, chunks[2], "choices", 0, "delta", "tool_calls", 0, "index"), float64(1))
	eq(t, "fc_2 delta args", dig(t, chunks[2], "choices", 0, "delta", "tool_calls", 0, "function", "arguments"), "B1")
	eq(t, "fc_1 delta index", dig(t, chunks[3], "choices", 0, "delta", "tool_calls", 0, "index"), float64(0))
}

func TestRespToChatDuplicateAddedReusesIndex(t *testing.T) {
	// A repeated added (upstream retry) must not burn a second chat index.
	st := newRespState("m")
	chunks := feedResp(t, st,
		`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
		`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
	)
	eq(t, "reused index", dig(t, chunks[1], "choices", 0, "delta", "tool_calls", 0, "index"), float64(0))
}

func TestRespToChatReasoningSummaryDelta(t *testing.T) {
	// response.reasoning_summary_text.delta → reasoning_content delta
	// (concerns/reasoning.js reasoningDelta).
	st := newRespState("m")
	chunks := feedResp(t, st, `{"type":"response.reasoning_summary_text.delta","data":{"delta":"pondering"}}`)
	eq(t, "chunk", chunks[0], jb(t, `{
		"id":"chatcmpl-1700000000123","object":"chat.completion.chunk","created":1700000000,
		"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"pondering"},"finish_reason":null}]
	}`))

	// Empty delta is dropped (JS `if (!delta) return null`).
	if got := feedResp(t, st, `{"type":"response.reasoning_summary_text.delta","data":{"delta":""}}`); len(got) != 0 {
		t.Fatalf("empty reasoning delta must be ignored: %s", js(got))
	}
}

func TestRespToChatCompleted(t *testing.T) {
	t.Run("usage maps input/output tokens and cached details, finish stop", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{
			"type":"response.completed",
			"data":{"response":{"usage":{"input_tokens":120,"output_tokens":30,
				"input_tokens_details":{"cached_tokens":80}}}}
		}`)
		if len(chunks) != 1 {
			t.Fatalf("want exactly one final chunk, got %d", len(chunks))
		}
		eq(t, "final chunk", chunks[0], jb(t, `{
			"id":"chatcmpl-1700000000123","object":"chat.completion.chunk","created":1700000000,
			"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,
				"prompt_tokens_details":{"cached_tokens":80}}
		}`))
	})

	t.Run("prompt/completion token names are the fallback", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{
			"type":"response.done",
			"data":{"response":{"usage":{"prompt_tokens":10,"completion_tokens":5}}}
		}`)
		eq(t, "usage", dig(t, chunks[0], "usage"), jb(t, `{
			"prompt_tokens":10,"completion_tokens":5,"total_tokens":15
		}`))
	})

	t.Run("an announced tool call finishes as tool_calls", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st,
			`{"type":"response.output_item.added","data":{"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"f"}}}`,
			`{"type":"response.completed","data":{"response":{}}}`,
		)
		eq(t, "finish reason", dig(t, chunks[1], "choices", 0, "finish_reason"), "tool_calls")
	})

	t.Run("exactly one final chunk even after completed", func(t *testing.T) {
		st := newRespState("m")
		feedResp(t, st, `{"type":"response.completed","data":{"response":{}}}`)
		if got := st.Convert(nil); got != nil {
			t.Fatalf("flush after completed must be nil: %s", js(got))
		}
		if got := st.Convert(jb(t, `{"type":"response.completed","data":{"response":{}}} `)); got != nil {
			t.Fatalf("second completed must be nil: %s", js(got))
		}
	})

	t.Run("flush without a terminal event emits the final chunk once", func(t *testing.T) {
		st := newRespState("m")
		feedResp(t, st, `{"type":"response.output_text.delta","data":{"delta":"hi"}}`)
		final := st.Convert(nil)
		if final == nil {
			t.Fatal("flush must emit the final chunk")
		}
		eq(t, "finish reason", dig(t, final, "choices", 0, "finish_reason"), "stop")
		if again := st.Convert(nil); again != nil {
			t.Fatalf("second flush must be nil: %s", js(again))
		}
	})

	t.Run("flush before anything started is nil", func(t *testing.T) {
		st := newRespState("m")
		if got := st.Convert(nil); got != nil {
			t.Fatalf("unstarted flush must be nil: %s", js(got))
		}
	})
}

func TestRespToChatErrorEvents(t *testing.T) {
	t.Run("error event surfaces as an [Error] content chunk with finish stop", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{"type":"error","data":{"error":{"message":"boom","code":"server_error"}}}`)
		if len(chunks) != 1 {
			t.Fatalf("want 1 chunk, got %d", len(chunks))
		}
		eq(t, "error chunk", chunks[0], jb(t, `{
			"id":"chatcmpl-1700000000123","object":"chat.completion.chunk","created":1700000000,
			"model":"m","choices":[{"index":0,"delta":{"content":"[Error] boom"},"finish_reason":"stop"}]
		}`))
		// finishReasonSent suppresses the flush chunk and duplicate errors.
		if got := st.Convert(nil); got != nil {
			t.Fatalf("flush after error must be nil: %s", js(got))
		}
		if got := feedResp(t, st, `{"type":"response.failed","data":{"response":{"error":{"message":"again"}}}}`); len(got) != 0 {
			t.Fatalf("duplicate error must be ignored: %s", js(got))
		}
	})

	t.Run("response.failed reads response.error", func(t *testing.T) {
		st := newRespState("m")
		chunks := feedResp(t, st, `{"type":"response.failed","data":{"response":{"error":{"message":"nope"}}}}`)
		eq(t, "content", dig(t, chunks[0], "choices", 0, "delta", "content"), "[Error] nope")
	})

	t.Run("error event without an error object is ignored", func(t *testing.T) {
		st := newRespState("m")
		if got := feedResp(t, st, `{"type":"error","data":{}}`); len(got) != 0 {
			t.Fatalf("must be ignored: %s", js(got))
		}
	})
}

func TestRespToChatUnknownEventsIgnored(t *testing.T) {
	st := newRespState("m")
	for _, raw := range []string{
		`{"type":"response.in_progress","data":{}}`,
		`{"type":"response.output_item.added","data":{"item":{"type":"message","role":"assistant"}}}`,
		`{"type":"response.output_item.done","data":{"item":{"type":"message","role":"assistant"}}}`,
		`{"type":"response.content_part.added","data":{}}`,
		`{"type":"response.reasoning_summary_text.done","data":{}}`,
		`{"type":"response.created","data":{}}`,
	} {
		if got := st.Convert(jb(t, raw)); got != nil {
			t.Fatalf("%s must not emit a chunk: %s", raw, js(got))
		}
	}
}

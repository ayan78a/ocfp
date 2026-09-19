//go:build parityfails

package translate

// Deliberate FAILING repros for the parity bugs found while writing the
// translate test suite. They are excluded from the default run via this build
// tag so `go test ./internal/translate/...` stays green:
//
//	go test -tags parityfails ./internal/translate/...
//
// Each test asserts the AUTHORITATIVE JS behaviour
// (~/9router/9router-src/open-sse/...) and fails against the current Go port.
// Do not "fix" these tests to match Go — fix the Go code, then drop the tag.

import (
	"testing"
)

// Bug #1 — internal/translate/prenorm.go:240 (FixMissingToolResponses).
// JS concerns/toolCall.js fixMissingToolResponses guards with
// `nextMsg && !hasToolResults(nextMsg, toolCallIds)`, so an assistant
// tool_calls turn at the very END of the conversation inserts nothing. Go
// treats a missing next message as "unanswered" and fabricates an empty tool
// result, changing what the upstream model sees.
func TestXFailFixMissingToolResponsesDanglingAtEnd(t *testing.T) {
	body := jb(t, `{
		"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}
			]}
		]
	}`)
	FixMissingToolResponses(body)
	if got, want := len(msgs(t, body)), 2; got != want {
		t.Fatalf("JS inserts nothing for a dangling end-of-conversation tool call: got %d messages, want %d\nmessages: %s",
			got, want, js(body["messages"]))
	}
}

// Bug #2 — internal/translate/req_resp2chat.go:287 (ResponsesToChatRequest).
// JS openai-responses.js:246-249 maps `reasoning.effort` to `reasoning_effort`
// before deleting the Responses `reasoning` object; Go deletes it without the
// mapping, so a Codex-style body loses its effort setting.
func TestXFailResponsesToChatMapsReasoningEffort(t *testing.T) {
	got := ResponsesToChatRequest(jb(t, `{
		"input":"hi",
		"reasoning":{"effort":"high","summary":"auto"}
	}`))
	if s := jsonxStr(got["reasoning_effort"]); s != "high" {
		t.Fatalf("JS sets reasoning_effort from reasoning.effort; got %q (%s)", s, js(got))
	}
}

// Bug #3 — internal/translate/req_resp2chat.go:159-173 (ResponsesToChatRequest).
// JS openai-responses.js:117-127 wraps a custom tool's freeform input as
// `{"input": <string-or-json>}` in the chat tool_call arguments (the wrapper
// the response side later unwraps via extractCustomToolInput). Go forwards the
// raw input string, so chat clients/upstreams see a different payload.
func TestXFailCustomToolCallArgumentsWrapped(t *testing.T) {
	got := ResponsesToChatRequest(jb(t, `{
		"input":[{"type":"custom_tool_call","call_id":"call_c","name":"edit","input":"print('hi')"}]
	}`))
	want := `{"input":"print('hi')"}`
	if gotArgs := jsonxStr(dig(t, got, "messages", 0, "tool_calls", 0, "function", "arguments")); gotArgs != want {
		t.Fatalf("JS wraps custom input as %q; got %q", want, gotArgs)
	}
}

// Bug #5 — internal/translate/req_resp2chat.go:244-245 (ResponsesToChatRequest).
// JS openai-responses.js:198 joins the custom tool's format hint parts with
// "\n" ([syntax, definition].join("\n")) and then joins [description, hint]
// with "\n\n". Go folds all parts into one "\n\n" join, so a tool carrying
// both format.syntax and format.definition advertises a different description
// upstream.
func TestXFailCustomToolFormatHintJoinSeparator(t *testing.T) {
	got := ResponsesToChatRequest(jb(t, `{
		"input":"hi",
		"tools":[{"type":"custom","name":"edit","description":"d","format":{"syntax":"regex","definition":"\\d+"}}]
	}`))
	want := "d\n\nregex\n\\d+"
	if gotDesc := jsonxStr(dig(t, got, "tools", 0, "function", "description")); gotDesc != want {
		t.Fatalf("JS description = %q; Go produced %q", want, gotDesc)
	}
}

// Bug #4 — FIXED: ChatToRespState now emits the response.created +
// response.in_progress scaffold on the first chunk; the golden expectations
// live in stream_chat2resp_test.go.

func eventNamesOf(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Event)
	}
	return out
}

func jsonxStr(v any) string {
	s, _ := v.(string)
	return s
}

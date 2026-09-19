package router

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opencode-free-proxy/internal/jsonx"
	"opencode-free-proxy/internal/relay"
	"opencode-free-proxy/internal/translate"
)

// Bypass short-circuit, ported fully from open-sse/utils/bypassHandler.js at
// its chat.js entry point (src/sse/handlers/chat.js:99-103): claude-cli
// warmup / title / skip requests are answered with a fixed synthetic
// completion BEFORE any alias, format-detection or intent work, so they never
// waste an upstream slot. (9router also re-runs the check inside chatCore at
// chatCore.js:73-75; this proxy keeps the single entry-point check.)

// defaultBypassText is bypassHandler.js DEFAULT_BYPASS_TEXT (:94).
const defaultBypassText = "CLI Command Execution: Clear Terminal"

// skipPatterns ports SKIP_PATTERNS (open-sse/config/runtimeConfig.js:94-96):
// requests whose user-role text CONTAINS one of these bypass the provider.
var skipPatterns = []string{
	"Please write a 5-10 word title for the following conversation:",
}

// handleBypassRequest ports bypassHandler.js handleBypassRequest (:11-92).
// It reports whether a synthetic response was written. sourceFormat is the
// endpoint's source format (the JS handler re-detects from the body shape —
// see the note on writeBypassResponse for the verified difference). The
// ccFilterNaming pattern (P5, bypassHandler.js:58-71) is NOT ported: it is
// gated on a 9router settings flag (default false) this proxy does not have.
func (s *Server) handleBypassRequest(w http.ResponseWriter, body map[string]any, model, userAgent string, sourceFormat relay.Format) bool {
	// Gate: the User-Agent must contain the case-SENSITIVE substring
	// "claude-cli" (bypassHandler.js:12) — "Claude-CLI" does NOT match.
	if !strings.Contains(userAgent, "claude-cli") {
		return false
	}
	messages := jsonx.AsArr(body["messages"])
	if len(messages) == 0 {
		return false // bypassHandler.js:13
	}

	shouldBypass := false

	// Pattern 1: title extraction — the last message is an assistant message
	// whose content array starts with a text block holding the single
	// opening-brace character "{" (bypassHandler.js:28-31).
	lastMsg := jsonx.AsObj(messages[len(messages)-1])
	if lastMsg != nil && jsonx.AsStr(lastMsg["role"]) == "assistant" {
		if content := jsonx.AsArr(lastMsg["content"]); len(content) > 0 {
			if first := jsonx.AsObj(content[0]); first != nil && jsonx.AsStr(first["text"]) == "{" {
				shouldBypass = true
			}
		}
	}

	// Pattern 2: warmup — the first message's text equals "Warmup" exactly
	// (bypassHandler.js:34-39).
	if !shouldBypass {
		if first := jsonx.AsObj(messages[0]); first != nil {
			if bypassTextContent(first["content"]) == "Warmup" {
				shouldBypass = true
			}
		}
	}

	// Pattern 3: count — exactly one message, a user, whose text is "count"
	// (bypassHandler.js:42-47).
	if !shouldBypass && len(messages) == 1 {
		if first := jsonx.AsObj(messages[0]); first != nil &&
			jsonx.AsStr(first["role"]) == "user" &&
			bypassTextContent(first["content"]) == "count" {
			shouldBypass = true
		}
	}

	// Pattern 4: skip patterns — the user-role messages' text joined with " "
	// CONTAINS one of the patterns (bypassHandler.js:50-56).
	if !shouldBypass {
		var userTexts []string
		for _, raw := range messages {
			m := jsonx.AsObj(raw)
			if m == nil || jsonx.AsStr(m["role"]) != "user" {
				continue
			}
			userTexts = append(userTexts, bypassTextContent(m["content"]))
		}
		joined := strings.Join(userTexts, " ")
		for _, p := range skipPatterns {
			if strings.Contains(joined, p) { // String.includes → substring match
				shouldBypass = true
				break
			}
		}
	}

	// Pattern 5 (ccFilterNaming, bypassHandler.js:58-71) deliberately not
	// ported — see the doc comment.

	if !shouldBypass {
		return false // bypassHandler.js:73
	}

	// stream = body.stream !== false (bypassHandler.js:76): an absent stream
	// field means SSE.
	streamField, isBool := body["stream"].(bool)
	stream := !isBool || streamField

	writeBypassResponse(w, sourceFormat, model, defaultBypassText, stream)
	return true
}

// bypassTextContent ports the getText closure (bypassHandler.js:16-22):
// string → itself; array → the .text of every type=="text" element joined
// with " "; anything else → "".
func bypassTextContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, raw := range c {
			block := jsonx.AsObj(raw)
			if block == nil || jsonx.AsStr(block["type"]) != "text" {
				continue
			}
			parts = append(parts, jsonx.AsStr(block["text"]))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// writeBypassResponse ports bypassHandler.js createNonStreamingResponse
// (:128-176) / createStreamingResponse (:182-224) for the endpoint's source
// format. Verified difference from the JS: the JS handler derives the format
// from the BODY SHAPE (detectFormat, bypassHandler.js:75), whose
// openai-responses branch requires `!body.messages` (services/provider.js
// detectFormat) while the bypass gate requires a non-empty messages array —
// so JS's translated bypass branch can only ever be the Claude one and the
// Responses path below is this proxy's endpoint-format equivalent. The Claude
// mergeChunksToResponse reconstruction (bypassHandler.js:240-271) stays
// unported for the same reason.
func writeBypassResponse(w http.ResponseWriter, sourceFormat relay.Format, model, text string, stream bool) {
	// createOpenAIResponse (bypassHandler.js:99-122): id `chatcmpl-<ms>`,
	// created now-seconds, usage {1,1,2}.
	id := "chatcmpl-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	created := time.Now().Unix()

	if sourceFormat != relay.FormatResponses {
		// FORMATS.OPENAI: the OpenAI chunks ARE the output
		// (bypassHandler.js:132-142 direct return).
		if stream {
			writeBypassStream(w, openaiBypassChunks(id, created, model, text))
			return
		}
		writeJSON(w, http.StatusOK, openaiBypassResponse(id, created, model, text))
		return
	}

	// /v1/responses: JS runs the OPENAI chunks through the response translator
	// (bypassHandler.js:144-165 stream / :186-208 streaming). initState picks
	// the openai-responses response state (translator/index.js:227) with
	// state.model = model; the Go port is translate.ChatToRespState.
	state := translate.NewChatToRespState(model, created, "")
	var events []translate.Event
	for _, chunk := range openaiBypassChunks(id, created, model, text) {
		events = append(events, state.Convert(chunk)...)
	}
	// Flush remaining (bypassHandler.js:158-162 / :202-208). The state machine
	// emits the response.created/in_progress scaffolding on the first Convert,
	// exactly like the JS translator state.
	events = append(events, state.Convert(nil)...)

	if stream {
		var b strings.Builder
		for _, ev := range events {
			// formatSSE's framed branch (utils/streamHelpers.js): responses
			// items are {event, data} pairs → `event: <name>\ndata: <json>\n\n`.
			b.WriteString(relay.FormatEvent(ev.Event, ev.Data))
		}
		b.WriteString("data: [DONE]\n\n") // bypassHandler.js:211
		corsHeaders(w.Header(), true)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, b.String())
		return
	}

	// Non-streaming: mergeChunksToResponse takes the LAST translated item for
	// non-Claude formats (bypassHandler.js:237) — here the terminal event's
	// payload (response.completed).
	if len(events) == 0 {
		// bypassHandler.js:231-233 empty fallback — unreachable with the two
		// fixed chunks, kept for shape parity.
		writeJSON(w, http.StatusOK, openaiBypassResponse(id, created, "unknown", text))
		return
	}
	writeJSON(w, http.StatusOK, events[len(events)-1].Data)
}

// openaiBypassResponse ports createOpenAIResponse (bypassHandler.js:99-122).
func openaiBypassResponse(id string, created int64, model, text string) map[string]any {
	return jsonx.ObjOf(
		"id", id,
		"object", "chat.completion",
		"created", created,
		"model", model,
		"choices", jsonx.ArrOf(jsonx.ObjOf(
			"index", float64(0),
			"message", jsonx.ObjOf("role", "assistant", "content", text),
			"finish_reason", "stop",
		)),
		"usage", jsonx.ObjOf(
			"prompt_tokens", float64(1),
			"completion_tokens", float64(1),
			"total_tokens", float64(2),
		),
	)
}

// openaiBypassChunks ports createOpenAIStreamingChunks (bypassHandler.js:279-313):
// a content chunk with delta{role,content} then a finish chunk with
// finish_reason:"stop" carrying the usage object.
func openaiBypassChunks(id string, created int64, model, text string) []map[string]any {
	return []map[string]any{
		{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": jsonx.ArrOf(jsonx.ObjOf(
				"index", float64(0),
				"delta", jsonx.ObjOf("role", "assistant", "content", text),
				"finish_reason", nil,
			)),
		},
		{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": jsonx.ArrOf(jsonx.ObjOf(
				"index", float64(0),
				"delta", jsonx.ObjOf(),
				"finish_reason", "stop",
			)),
			"usage": jsonx.ObjOf(
				"prompt_tokens", float64(1),
				"completion_tokens", float64(1),
				"total_tokens", float64(2),
			),
		},
	}
}

// writeBypassStream emits the chat-format SSE frames: `data: <json>\n\n` per
// chunk (formatSSE's plain branch, utils/streamHelpers.js) plus the final
// [DONE] sentinel (bypassHandler.js:211), with the SSE headers of
// bypassHandler.js:216-221.
func writeBypassStream(w http.ResponseWriter, chunks []map[string]any) {
	var out strings.Builder
	for _, chunk := range chunks {
		frame, _ := json.Marshal(chunk)
		out.WriteString("data: ")
		out.Write(frame)
		out.WriteString("\n\n")
	}
	out.WriteString("data: [DONE]\n\n")
	corsHeaders(w.Header(), true)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, out.String())
}

// Package relay ports the streaming/relay layer of open-sse/utils/stream.js and
// the non-streaming SSE→JSON handlers. Four client cases live here:
//
//	passthrough (chat client + chat model, or responses client + muse-spark):
//	    normalize + forward, usage seam, [DONE] guarantee on flush
//	translate responses→chat (chat client + muse-spark)
//	translate chat→responses (responses client + chat model)
//	forced SSE→JSON aggregation (non-streaming client)
package relay

import (
	"encoding/json"
	"strings"

	"opencode-free-proxy/internal/jsonx"
)

// CleanUsagePayload drops null usage blocks and usage.perf_metrics:null before
// serialization (streamHelpers.js cleanUsagePayload). Recursive into .response.
func CleanUsagePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return payload
	}
	cleaned := payload
	if u, has := cleaned["usage"]; has {
		if u == nil {
			delete(cleaned, "usage")
		} else if uo := jsonx.AsObj(u); uo != nil {
			if _, hasPerf := uo["perf_metrics"]; hasPerf && uo["perf_metrics"] == nil {
				delete(uo, "perf_metrics")
			}
		}
	}
	if resp := jsonx.AsObj(cleaned["response"]); resp != nil {
		CleanUsagePayload(resp)
	}
	return cleaned
}

// FormatData renders one SSE frame: `data: {json}\n\n`. A {done:true} object
// renders as the [DONE] sentinel; an {event, data} wrapper renders as a named
// event frame (formatSSE's framed branch).
func FormatData(data map[string]any) string {
	if _, isDone := data["done"]; isDone {
		if b, ok := data["done"].(bool); ok && b {
			return "data: [DONE]\n\n"
		}
	}
	if ev, isStr := data["event"].(string); isStr && ev != "" {
		if inner := jsonx.AsObj(data["data"]); inner != nil {
			return FormatEvent(ev, inner)
		}
	}
	b, _ := json.Marshal(CleanUsagePayload(data))
	return "data: " + string(b) + "\n\n"
}

// FormatDataLine renders a passthrough line frame: `data: {json}\n` (single
// newline) with the cleanUsagePayload pass applied. The passthrough relay no
// longer re-serializes through here — stream.js emits its re-serialized
// passthrough chunks as bare `data: ${JSON.stringify(parsed)}\n`
// (stream.js:346/351/358/361; cleanUsagePayload is formatSSE-only,
// streamHelpers.js:119) — see formatDataLine in passthrough.go.
func FormatDataLine(data map[string]any) string {
	b, _ := json.Marshal(CleanUsagePayload(data))
	return "data: " + string(b) + "\n"
}

// FormatEvent renders one framed SSE event: `event: <name>\ndata: {json}\n\n`.
func FormatEvent(event string, data map[string]any) string {
	b, _ := json.Marshal(CleanUsagePayload(data))
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
}

// dataPayload returns the JSON text after "data:" (trimmed), or ok=false.
func dataPayload(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	return strings.TrimSpace(trimmed[len("data:"):]), true
}

// classifyDataLine splits one SSE line into the passthrough signals
// (stream.js:270 gate + 364 catch): isData marks a `data:` line, isDone the
// [DONE] sentinel, parsed carries the JSON body — non-nil only for a data line
// whose payload parsed into an OBJECT — and nonObjectJSON marks a payload that
// parsed as valid JSON but is not an object (JS JSON.parse accepts 42, "x",
// [1,2], true — stream.js:272). Such a chunk skips every mutation guard in the
// passthrough branch and is re-emitted verbatim through the !injectedUsage
// tail (stream.js:372-378), while a JSON `null` payload is NOT: fixInvalidId
// reads parsed.id on it, which throws in JS and lands in the catch
// (stream.js:274 + 364-369) — the chunk is dropped, so nonObjectJSON stays
// false for it.
func classifyDataLine(line string) (parsed map[string]any, isData bool, isDone bool, nonObjectJSON bool) {
	payload, ok := dataPayload(line)
	if !ok {
		return nil, false, false, false
	}
	if payload == "[DONE]" {
		return nil, true, true, false
	}
	var raw any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, true, false, false
	}
	parsed, isObj := raw.(map[string]any)
	if !isObj {
		// raw == nil is the JSON null payload — dropped in JS (see above).
		return nil, true, false, raw != nil
	}
	return parsed, true, false, false
}

// parseDataLine is the two-value form translate mode consumes: the parsed
// object plus "was a data line" — true for both a parsed object and the [DONE]
// sentinel (stream.js translate mode reads the sentinel as {done:true}).
// Non-object JSON payloads surface here as (nil, false): translate mode reads
// chunk properties through optional chaining and its translators emit nothing
// for them, so dropping is wire-equivalent (stream.js:390-511).
func parseDataLine(line string) (map[string]any, bool) {
	parsed, isData, isDone, _ := classifyDataLine(line)
	if parsed == nil {
		return nil, isData && isDone
	}
	return parsed, true
}

// TerminalResponsesEvent reports whether an event name marks a terminal
// Responses state (responsesStreamHelpers.js isOpenAIResponsesTerminalEvent),
// including response.status completed/failed. The name goes through
// getOpenAIResponsesEventName first: an absent `event:` line falls back to the
// chunk's own type field.
func TerminalResponsesEvent(eventName string, chunk map[string]any) bool {
	switch EventName(eventName, chunk) {
	case "response.completed", "response.done", "response.failed", "error":
		return true
	}
	status := jsonx.AsStr(jsonx.Get(jsonx.Get(chunk, "response"), "status"))
	return status == "completed" || status == "failed"
}

// EventName falls back to the chunk's own type field when no `event:` line
// preceded (getOpenAIResponsesEventName).
func EventName(eventName string, chunk map[string]any) string {
	if eventName != "" {
		return eventName
	}
	if t, is := chunk["type"].(string); is {
		return t
	}
	return ""
}

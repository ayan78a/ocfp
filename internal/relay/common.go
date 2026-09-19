package relay

import (
	"strings"
	"time"

	"opencode-free-proxy/internal/cloak"
	"opencode-free-proxy/internal/usage"
)

// Format identifies a client/upstream wire format (the two this proxy speaks).
type Format string

const (
	FormatChat      Format = "chat"
	FormatResponses Format = "responses"
)

// Synthesis resolves the per-request hidden-thinking decision: fabricate
// reasoning_tokens only when the request asked for thinking (reasoning_effort /
// reasoning / thinking / thinkingConfig / enable_thinking knob or a model
// suffix like "model(high)"); explicit "none" disables.
type Synthesis struct {
	Enabled bool
	Ratio   float64
}

// ResolveSynthesis ports shouldSynthesizeReasoning: model-suffix override
// wins, then the pre-translation intent snapshot, then the (post-translation)
// body. Any intent whose mode is not "none" enables synthesis; the ratio is
// the fixed default (no per-combo config in this proxy).
func ResolveSynthesis(body map[string]any, model string, intent *cloak.ThinkingCfg) Synthesis {
	_, override := cloak.ParseSuffix(model)
	cfg := override
	if cfg == nil {
		cfg = intent
	}
	if cfg == nil {
		cfg = cloak.ExtractThinking(body)
	}
	if cfg != nil && cfg.Mode != "none" {
		return Synthesis{Enabled: true, Ratio: usage.DefaultRatio}
	}
	return Synthesis{Enabled: false, Ratio: usage.DefaultRatio}
}

// fixInvalidID repairs generic/too-short ids (streamHelpers.js fixInvalidId).
// Reports whether it mutated the chunk.
func fixInvalidID(parsed map[string]any) bool {
	id, is := parsed["id"].(string)
	if !is || !(id == "chat" || id == "completion" || len(id) < 8) {
		return false
	}
	fallback := ""
	ef := parsed["extend_fields"]
	if efMap, isObj := ef.(map[string]any); isObj {
		if s, isStr := efMap["requestId"].(string); isStr && s != "" {
			fallback = s
		} else if s, isStr := efMap["traceId"].(string); isStr && s != "" {
			fallback = s
		}
	}
	if fallback == "" {
		fallback = formatInt36(time.Now().UnixMilli())
	}
	parsed["id"] = "chatcmpl-" + fallback
	return true
}

// formatInt36 renders n in base36 (JS Number.toString(36)).
func formatInt36(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%36]
		n /= 36
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// jsTruthy mirrors JS truthiness over the JSON-decoded value space. Falsy:
// null, false, 0, "" — everything else is truthy, INCLUDING empty arrays and
// empty objects (JS has no length/size truthiness; the length checks that
// matter are spelled out separately, e.g. stream.js:304
// `choice.delta.tool_calls.length === 0`, streamHelpers.js:47).
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	default:
		return true // arrays and objects are always truthy in JS
	}
}

// hasValuableContent filters empty chunks (streamHelpers.js:38-51, OpenAI
// branch — the only filter this proxy's formats apply). The JS predicate is
// gated on `chunk.choices?.[0]?.delta`: a chunk with no choices, an empty
// choices array, or a delta-less first choice always passes; only a delta
// carrying none of content/reasoning_content/tool_calls, no truthy
// finish_reason or role, and no valid usage is dropped. Usage-bearing chunks
// always pass.
func hasValuableContent(chunk map[string]any) bool {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return true
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return true
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		// JS gate: choices[0].delta must be present to enter the filter — a
		// delta-less choice keeps the chunk (streamHelpers.js:40).
		return true
	}
	if jsTruthy(delta["content"]) || jsTruthy(delta["reasoning_content"]) {
		return true
	}
	if tc, is := delta["tool_calls"].([]any); is && len(tc) > 0 {
		return true
	}
	if jsTruthy(choice["finish_reason"]) || jsTruthy(delta["role"]) {
		return true
	}
	u, _ := chunk["usage"].(map[string]any)
	return usage.HasValid(u)
}

// accumulate counts content/reasoning lengths for the usage estimate.
func accumulate(totalLen *int, content, thinking *strings.Builder, delta map[string]any) {
	if delta == nil {
		return
	}
	if c, is := delta["content"].(string); is && c != "" {
		*totalLen += len(c)
		content.WriteString(c)
	}
	if r, is := delta["reasoning_content"].(string); is && r != "" {
		*totalLen += len(r)
		thinking.WriteString(r)
	}
}

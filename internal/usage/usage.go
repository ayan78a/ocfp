// Package usage ports open-sse/utils/usageTracking.js for the two wire
// families this proxy speaks (OpenAI chat + Responses). Token accounting
// conventions here feed both client-facing usage blocks and the usage seam;
// keep them bit-identical to the JS engine.
package usage

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"opencode-free-proxy/internal/jsonx"
)

const bufferTokens = 2000

// SynthMaxOutput and DefaultRatio gate hidden-thinking synthesis: completions
// at or below the threshold are assumed reasoning-free; above it, a share of
// output tokens is attributed to thinking.
const (
	SynthMaxOutput = 10
	DefaultRatio   = 0.75
)

// numOK mirrors `Number.isFinite(Number(v))` over decoded-JSON values: the
// numeric value plus whether JS would consider it finite. Non-numeric strings,
// null and objects are not finite (normalizeUsage's assignNumber drops them);
// the empty string, false and [] coerce to 0.
func numOK(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, true // Number("") === 0
		}
		f, err := strconv.ParseFloat(s, 64) // full-string parse, like Number()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case map[string]any:
		return 0, false // Number({}) is NaN
	case []any:
		return 0, len(n) == 0 // Number([]) === 0, Number([x]) is NaN
	default:
		return 0, false
	}
}

// num coerces any value to a number, mapping everything non-finite to 0 —
// canonicalizeUsage's local `Number.isFinite(Number(v)) ? Number(v) : 0`
// idiom, used on arithmetic paths.
func num(v any) float64 {
	f, _ := numOK(v)
	return f
}

// jsTruthy mirrors JS truthiness over decoded-JSON values: null, false, 0,
// NaN and "" are falsy; every object/array (even empty) is truthy.
func jsTruthy(v any) bool {
	switch n := v.(type) {
	case nil:
		return false
	case bool:
		return n
	case float64:
		return n != 0 && !math.IsNaN(n)
	case string:
		return n != ""
	default:
		return true
	}
}

// jsOr mirrors JS `a || b || c`: the first truthy value wins, else the LAST
// operand's value — even when it is falsy (`undefined || 0` is 0, not
// undefined).
func jsOr(vals ...any) any {
	for _, v := range vals {
		if jsTruthy(v) {
			return v
		}
	}
	if len(vals) > 0 {
		return vals[len(vals)-1]
	}
	return nil
}

// firstNonNull returns the first argument that is not nil — the JS `??`
// nullish chain over already-read values (an absent key and JSON null are both
// nullish; a present 0 is not).
func firstNonNull(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// Normalize keeps only known numeric fields plus nested details objects, the
// way normalizeUsage's assignNumber does: absent/null fields are omitted (not
// zeroed) and values Number cannot render finite drop entirely, while numeric
// strings coerce (Number("7") === 7). Nil when nothing numeric survives
// (normalizeUsage returns null for an empty object).
func Normalize(u map[string]any) map[string]any {
	if u == nil {
		return nil
	}
	out := map[string]any{}
	for _, k := range []string{
		"prompt_tokens", "completion_tokens", "total_tokens",
		"cache_read_input_tokens", "cache_creation_input_tokens",
		"cached_tokens", "reasoning_tokens",
	} {
		if v := u[k]; v != nil {
			if f, ok := numOK(v); ok {
				out[k] = f
			}
		}
	}
	for _, k := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		if d := jsonx.AsObj(u[k]); d != nil {
			out[k] = d
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Canonical folds cache into prompt exactly once:
//
//	prompt_tokens = total input INCLUDING cache read + cache creation
//	cached_tokens = cache-read subset; cache_creation_input_tokens = write subset
//
// Claude-style reports (cache_read_input_tokens present, cached_tokens absent)
// fold; OpenAI-style (cached_tokens or nested details) pass through.
// Idempotent: the folded output always carries cached_tokens. Mirrors
// canonicalizeUsage key-for-key: the five canonical counts are ALWAYS present
// (0 included); only reasoning_tokens is conditional (> 0). The fallback
// chains are nullish (??), not zeroish: a present 0 beats an absent sibling,
// and the nested prompt_tokens_details shapes are only consulted when the
// top-level key is absent/null.
func Canonical(u map[string]any) map[string]any {
	if u == nil {
		return nil
	}
	completion := num(firstNonNull(u["completion_tokens"], u["output_tokens"]))
	reasoning := num(u["reasoning_tokens"])
	cacheCreation := num(firstNonNull(u["cache_creation_input_tokens"],
		jsonx.Get(u["prompt_tokens_details"], "cache_creation_tokens")))

	prompt := num(firstNonNull(u["prompt_tokens"], u["input_tokens"]))
	var cached float64
	_, hasCached := u["cached_tokens"]
	if !hasCached && (has(u, "cache_read_input_tokens") || has(u, "cache_creation_input_tokens")) {
		cached = num(u["cache_read_input_tokens"])
		prompt = prompt + cached + cacheCreation
	} else {
		cached = num(firstNonNull(u["cached_tokens"],
			jsonx.Get(u["prompt_tokens_details"], "cached_tokens")))
	}

	result := map[string]any{
		"prompt_tokens":               prompt,
		"completion_tokens":           completion,
		"total_tokens":                prompt + completion,
		"cached_tokens":               cached,
		"cache_creation_input_tokens": cacheCreation,
	}
	if reasoning > 0 {
		result["reasoning_tokens"] = reasoning
	}
	return result
}

// Merge field-wise maxes two usage maps (Anthropic-style split events; a
// no-op for single complete usage objects). Nested objects: latest wins.
// mergeUsage only Math.maxes when the previous value is itself a number
// (typeof check) and only replaces objects — strings and scalars never cross.
func Merge(prev, next map[string]any) map[string]any {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	merged := map[string]any{}
	for k, v := range prev {
		merged[k] = v
	}
	for k, v := range next {
		switch n := v.(type) {
		case float64:
			// typeof NaN === "number" — guard with Number.isFinite so one
			// malformed chunk can't poison the accumulation (the JS comment's
			// own rationale; Math.max(x, NaN) is NaN on both sides).
			if !math.IsNaN(n) && !math.IsInf(n, 0) {
				cur, _ := merged[k].(float64) // non-number prev bases at 0
				merged[k] = math.Max(cur, n)
			}
		default:
			if o := jsonx.AsObj(v); o != nil {
				merged[k] = o
			}
		}
	}
	return merged
}

// HasValid reports whether any known token field holds a real number > 0.
// hasValidUsage requires `typeof usage[field] === "number"` — numeric strings
// never validate.
func HasValid(u map[string]any) bool {
	if u == nil {
		return false
	}
	for _, k := range []string{
		"prompt_tokens", "completion_tokens", "total_tokens",
		"input_tokens", "output_tokens",
		"promptTokenCount", "candidatesTokenCount",
	} {
		if n, ok := u[k].(float64); ok && n > 0 {
			return true
		}
	}
	return false
}

// ExtractFromChat reads usage out of a Chat Completions chunk — extractUsage's
// OpenAI branch, which also covers DeepSeek's prompt_cache_hit_tokens. The
// candidate object funnels through Normalize exactly as the JS hands its
// literal to normalizeUsage: prompt_tokens forwards raw, the details objects
// forward RAW (never synthesized — a DeepSeek hit-only stream yields a bare
// cached_tokens and no prompt_tokens_details).
func ExtractFromChat(chunk map[string]any) map[string]any {
	u := jsonx.AsObj(chunk["usage"])
	if u == nil {
		return nil
	}
	if _, has := u["prompt_tokens"]; !has {
		return nil
	}
	return Normalize(map[string]any{
		"prompt_tokens":             u["prompt_tokens"],                // raw: a null drops the key
		"completion_tokens":         jsOr(u["completion_tokens"], 0.0), // `completion_tokens || 0`
		"cached_tokens":             jsOr(jsonx.Get(u["prompt_tokens_details"], "cached_tokens"), u["prompt_cache_hit_tokens"]),
		"reasoning_tokens":          jsonx.Get(u["completion_tokens_details"], "reasoning_tokens"),
		"prompt_tokens_details":     u["prompt_tokens_details"],
		"completion_tokens_details": u["completion_tokens_details"],
	})
}

// ExtractFromResponses reads usage out of a response.completed / response.done
// event — extractUsage's Responses branch. The `||` chains fall back to the
// chat-shaped field names, and the result funnels through Normalize:
// cached_tokens/reasoning_tokens keep an explicit 0 (finite after Number),
// while the synthesized prompt_tokens_details only appears when cached_tokens
// is truthy (`cachedTokens ? { cached_tokens: cachedTokens } : undefined`).
// JS never forwards input_tokens_details/output_tokens_details here.
func ExtractFromResponses(chunk map[string]any) map[string]any {
	t := jsonx.AsStr(chunk["type"])
	if t != "response.completed" && t != "response.done" {
		return nil
	}
	u := jsonx.AsObj(jsonx.Get(chunk["response"], "usage"))
	if u == nil {
		return nil
	}
	cached := jsonx.Get(u["input_tokens_details"], "cached_tokens")
	var ptd any
	if jsTruthy(cached) {
		ptd = jsonx.ObjOf("cached_tokens", cached) // raw, un-coerced value
	}
	return Normalize(map[string]any{
		"prompt_tokens":         jsOr(u["input_tokens"], u["prompt_tokens"], 0.0),
		"completion_tokens":     jsOr(u["output_tokens"], u["completion_tokens"], 0.0),
		"cached_tokens":         cached,
		"reasoning_tokens":      jsonx.Get(u["output_tokens_details"], "reasoning_tokens"),
		"prompt_tokens_details": ptd,
	})
}

// AddBuffer inflates usage by bufferTokens — context-error headroom on
// estimated usage. addBufferToUsage buffers BOTH input_tokens (Claude shape)
// and prompt_tokens (OpenAI shape), then buffers total_tokens or computes it
// from the already-buffered prompt + completion.
func AddBuffer(u map[string]any) map[string]any {
	if u == nil {
		return u
	}
	out := map[string]any{}
	for k, v := range u {
		out[k] = v
	}
	if _, has := out["input_tokens"]; has { // Claude format
		out["input_tokens"] = num(out["input_tokens"]) + bufferTokens
	}
	if _, has := out["prompt_tokens"]; has { // OpenAI format
		out["prompt_tokens"] = num(out["prompt_tokens"]) + bufferTokens
	}
	if _, has := out["total_tokens"]; has {
		out["total_tokens"] = num(out["total_tokens"]) + bufferTokens
	} else if _, hasP := out["prompt_tokens"]; hasP {
		if _, hasC := out["completion_tokens"]; hasC {
			out["total_tokens"] = num(out["prompt_tokens"]) + num(out["completion_tokens"])
		}
	}
	return out
}

// EstimateInput sums the whole request body /4 (~chars per token).
func EstimateInput(body any) float64 {
	b, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	return math.Ceil(float64(len(b)) / 4)
}

// EstimateOutput is content length /4 with a one-token floor —
// estimateOutputTokens returns Math.max(1, Math.floor(contentLength / 4)),
// so even a 1-3 char completion still counts 1 output token.
func EstimateOutput(contentLen int) float64 {
	if contentLen <= 0 {
		return 0
	}
	return math.Max(1, math.Floor(float64(contentLen)/4))
}

// Estimate builds estimated usage for the OpenAI chat shape with buffer
// (estimateUsage -> formatUsage's default branch -> addBufferToUsage).
func Estimate(body any, contentLen int) map[string]any {
	in := EstimateInput(body)
	out := EstimateOutput(contentLen)
	u := map[string]any{
		"prompt_tokens":     in,
		"completion_tokens": out,
		"total_tokens":      in + out,
		"estimated":         true,
	}
	return AddBuffer(u)
}

// EstimateResponses is estimateUsage for a Responses-format client. JS composes
// it in the canonical OpenAI shape — formatUsage's default branch, buffered by
// addBufferToUsage on prompt_tokens AND total_tokens — and then renames it
// with convertUsageForFormat, whose Responses family reads
// input_tokens ?? prompt_tokens and keeps only input/output tokens + estimated
// (total_tokens dropped). Net: input_tokens = raw + bufferTokens.
func EstimateResponses(body any, contentLen int) map[string]any {
	buffered := Estimate(body, contentLen)
	return map[string]any{
		"input_tokens":  num(buffered["prompt_tokens"]), // input_tokens ?? prompt_tokens
		"output_tokens": num(buffered["completion_tokens"]),
		"estimated":     true,
	}
}

// SynthesizeThinking fills reasoning tokens when upstream thinks silently —
// synthesizeThinkingTokens. completion is read through the nullish chain
// completion_tokens ?? output_tokens ?? candidatesTokenCount (a present 0
// short-circuits, and completion <= 0 leaves usage untouched); usage that
// already reports reasoning (reasoning_tokens ?? completion_tokens_details
// .reasoning_tokens ?? output_tokens_details.reasoning_tokens ??
// thoughtsTokenCount, then || 0) passes through. completion <= 10 → 0; else
// floor(ratio × completion); only a NON-FINITE ratio falls back to
// DefaultRatio (JS checks Number.isFinite — no sign check). target "responses"
// writes output_tokens_details.reasoning_tokens, "chat" (the JS default
// branch) writes completion_tokens_details.reasoning_tokens; the Claude/Gemini
// family outputs are outside this port's two-family scope. Every level of the
// result is a new object (JS spreads), so the stats side is never mutated.
func SynthesizeThinking(u map[string]any, target string, ratio float64) map[string]any {
	if u == nil {
		return u
	}
	completion := num(firstNonNull(
		u["completion_tokens"],
		u["output_tokens"],
		u["candidatesTokenCount"],
	))
	if completion <= 0 {
		return u
	}
	reported := num(firstNonNull(
		u["reasoning_tokens"],
		jsonx.Get(u["completion_tokens_details"], "reasoning_tokens"),
		jsonx.Get(u["output_tokens_details"], "reasoning_tokens"),
		u["thoughtsTokenCount"],
	))
	if reported > 0 {
		return u
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		ratio = DefaultRatio
	}
	synthesized := 0.0
	if completion > SynthMaxOutput {
		synthesized = math.Floor(completion * ratio)
	}
	out := map[string]any{}
	for k, v := range u {
		out[k] = v
	}
	key := "completion_tokens_details"
	if target == "responses" {
		key = "output_tokens_details"
	}
	src := jsonx.AsObj(u[key])
	details := make(map[string]any, len(src)+1)
	for k, v := range src { // JS spread: new object, not in-place mutation
		details[k] = v
	}
	details["reasoning_tokens"] = synthesized
	out[key] = details
	return out
}

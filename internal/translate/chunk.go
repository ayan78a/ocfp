package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"opencode-free-proxy/internal/jsonx"
)

// ModelFallback is the model name used when a stream carries none
// (schema/defaults.js MODEL_FALLBACK).
const ModelFallback = "unknown"

// BuildChunk builds an OpenAI chat.completion.chunk (concerns/chunk.js).
func BuildChunk(id string, created int64, model string, delta map[string]any, finishReason any) map[string]any {
	return jsonx.ObjOf(
		"id", id,
		"object", "chat.completion.chunk",
		"created", created,
		"model", model,
		"choices", jsonx.ArrOf(jsonx.ObjOf(
			"index", 0,
			"delta", delta,
			"finish_reason", finishReason,
		)),
	)
}

// BuildUsage builds an OpenAI usage object; detail blocks appear only when
// their counters are > 0 (concerns/usage.js buildUsage).
func BuildUsage(prompt, completion, total, cached, cacheCreation, reasoning float64) map[string]any {
	usage := jsonx.ObjOf(
		"prompt_tokens", prompt,
		"completion_tokens", completion,
		"total_tokens", total,
	)
	if cached > 0 || cacheCreation > 0 {
		details := jsonx.ObjOf()
		if cached > 0 {
			details["cached_tokens"] = cached
		}
		if cacheCreation > 0 {
			details["cache_creation_tokens"] = cacheCreation
		}
		usage["prompt_tokens_details"] = details
	}
	if reasoning > 0 {
		usage["completion_tokens_details"] = jsonx.ObjOf("reasoning_tokens", reasoning)
	}
	return usage
}

// ReasoningDelta is the vendor-neutral reasoning delta field.
func ReasoningDelta(text string) map[string]any {
	return jsonx.ObjOf("reasoning_content", text)
}

// ExtractReasoningText reads a streamed delta's reasoning across vendor
// shapes: reasoning_content, reasoning, reasoning_details[].
func ExtractReasoningText(delta map[string]any) string {
	if delta == nil {
		return ""
	}
	if s, is := delta["reasoning_content"].(string); is && s != "" {
		return s
	}
	if s, is := delta["reasoning"].(string); is && s != "" {
		return s
	}
	if details, is := delta["reasoning_details"].([]any); is {
		var sb strings.Builder
		for _, d := range details {
			switch v := d.(type) {
			case string:
				sb.WriteString(v)
			case map[string]any:
				if s, is := v["text"].(string); is && s != "" {
					sb.WriteString(s)
					continue
				}
				if s, is := v["content"].(string); is && s != "" {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// FallbackToolCallID mirrors concerns/toolCall.js fallbackToolCallId().
func FallbackToolCallID() string {
	return fmt.Sprintf("call_%d", time.Now().UnixMilli())
}

// JSONStringifyStr serializes v as JSON when it isn't already a string.
func JSONStringifyStr(v any) string {
	if s, is := v.(string); is {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

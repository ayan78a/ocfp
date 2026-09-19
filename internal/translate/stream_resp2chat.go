package translate

import (
	"encoding/json"
	"fmt"
	"time"

	"opencode-free-proxy/internal/jsonx"
)

// RespToChatState carries openaiResponsesToOpenAIResponse's accumulator across
// SSE events (Responses upstream → Chat client).
type RespToChatState struct {
	Started         bool
	ChatID          string
	Created         int64
	Model           string
	ToolCallIndex   int
	CurrentToolCall any // sticky: string call id or nil
	// item_id → chat tool_calls index; deltas key on item_id so parallel calls
	// stay separate when upstream emits all addeds before dones.
	RespToolChatIndex   map[string]int
	RespToolArgsEmitted map[int]bool
	FinishReasonSent    bool
	FinishReason        string
	Usage               map[string]any
	Error               map[string]any
	now                 func() time.Time
}

// NewRespToChatState seeds a translator state (stream.js createStreamState).
func NewRespToChatState(model string) *RespToChatState {
	return &RespToChatState{Model: model, now: time.Now}
}

func (s *RespToChatState) timestamp() int64 { return s.now().UnixMilli() }

func (s *RespToChatState) chunk(delta map[string]any, finish any) map[string]any {
	return BuildChunk(s.ChatID, s.Created, s.modelOrFallback(), delta, finish)
}

func (s *RespToChatState) modelOrFallback() string {
	if s.Model != "" {
		return s.Model
	}
	return ModelFallback
}

// computeFinishReason: tool_calls when any call was announced, else stop.
// CurrentToolCallId stays sticky so a flush can still finalize as tool_calls.
func (s *RespToChatState) computeFinishReason() string {
	if s.ToolCallIndex > 0 || s.CurrentToolCall != nil {
		return "tool_calls"
	}
	return "stop"
}

// finalChunk is the flush/completed chunk carrying finish_reason and usage.
func (s *RespToChatState) finalChunk() map[string]any {
	finish := s.computeFinishReason()
	s.FinishReasonSent = true
	s.FinishReason = finish
	id := s.ChatID
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", s.timestamp())
	}
	created := s.Created
	if created == 0 {
		created = s.timestamp() / 1000
	}
	final := BuildChunk(id, created, s.modelOrFallback(), map[string]any{}, finish)
	if s.Usage != nil {
		final["usage"] = s.Usage
	}
	return final
}

// Convert translates one parsed Responses SSE event into zero or one Chat
// chunk; nil means "ignore this event". A nil chunk input flushes.
func (s *RespToChatState) Convert(chunk map[string]any) map[string]any {
	if chunk == nil {
		if s.FinishReasonSent || !s.Started {
			return nil
		}
		return s.finalChunk()
	}

	eventType := jsonx.AsStr(chunk["type"])
	if eventType == "" {
		eventType = jsonx.AsStr(chunk["event"])
	}
	data := chunk
	if d, is := chunk["data"].(map[string]any); is {
		data = d
	}

	if !s.Started {
		s.Started = true
		s.ChatID = fmt.Sprintf("chatcmpl-%d", s.timestamp())
		s.Created = s.timestamp() / 1000
		s.ToolCallIndex = 0
		s.CurrentToolCall = nil
	}

	switch eventType {
	case "response.output_text.delta":
		delta := jsonx.AsStr(data["delta"])
		if delta == "" {
			return nil
		}
		return s.chunk(map[string]any{"content": delta}, nil)

	case "response.output_text.done":
		return nil

	case "response.output_item.added":
		item := jsonx.AsObj(data["item"])
		itemType := jsonx.AsStr(jsonx.Get(item, "type"))
		if itemType != ItemFunctionCall && itemType != "custom_tool_call" {
			return nil
		}
		callID := jsonx.AsStr(jsonx.Get(item, "call_id"))
		if callID != "" {
			s.CurrentToolCall = callID
		} else {
			s.CurrentToolCall = FallbackToolCallID()
		}
		key := jsonx.AsStr(jsonx.Get(item, "id"))
		if key == "" {
			key = jsonx.AsStr(data["item_id"])
		}
		if key == "" {
			if id, is := s.CurrentToolCall.(string); is {
				key = id
			}
		}
		var idx int
		if s.RespToolChatIndex == nil {
			s.RespToolChatIndex = map[string]int{}
		}
		if prev, seen := s.RespToolChatIndex[key]; key != "" && seen {
			idx = prev // duplicate added (retry) — reuse
		} else {
			idx = s.ToolCallIndex
			s.ToolCallIndex++
			if key != "" {
				s.RespToolChatIndex[key] = idx
			}
		}
		name := jsonx.AsStr(jsonx.Get(item, "name"))
		return s.chunk(map[string]any{
			"tool_calls": jsonx.ArrOf(jsonx.ObjOf(
				"index", idx,
				"id", s.CurrentToolCall,
				"type", BlockFunction,
				"function", jsonx.ObjOf("name", name, "arguments", ""))),
		}, nil)

	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		argsDelta := jsonx.AsStr(data["delta"])
		if argsDelta == "" {
			return nil
		}
		idx := 0
		if itemID := jsonx.AsStr(data["item_id"]); itemID != "" {
			if known, ok := s.RespToolChatIndex[itemID]; ok {
				idx = known
			} else {
				idx = maxInt(0, s.ToolCallIndex-1)
			}
		} else {
			idx = maxInt(0, s.ToolCallIndex-1)
		}
		if s.RespToolArgsEmitted == nil {
			s.RespToolArgsEmitted = map[int]bool{}
		}
		s.RespToolArgsEmitted[idx] = true
		return s.chunk(map[string]any{
			"tool_calls": jsonx.ArrOf(jsonx.ObjOf(
				"index", idx,
				"function", jsonx.ObjOf("arguments", argsDelta))),
		}, nil)

	case "response.output_item.done":
		item := jsonx.AsObj(data["item"])
		itemType := jsonx.AsStr(jsonx.Get(item, "type"))
		if itemType != ItemFunctionCall && itemType != "custom_tool_call" {
			return nil
		}
		key := jsonx.AsStr(jsonx.Get(item, "id"))
		if key == "" {
			key = jsonx.AsStr(data["item_id"])
		}
		idx := maxInt(0, s.ToolCallIndex-1)
		if known, ok := s.RespToolChatIndex[key]; ok {
			idx = known
		}
		fullArgs, isStr := jsonx.Get(item, "arguments").(string)
		if isStr && fullArgs != "" {
			if s.RespToolArgsEmitted == nil {
				s.RespToolArgsEmitted = map[int]bool{}
			}
			if !s.RespToolArgsEmitted[idx] {
				s.RespToolArgsEmitted[idx] = true
				return s.chunk(map[string]any{
					"tool_calls": jsonx.ArrOf(jsonx.ObjOf(
						"index", idx,
						"function", jsonx.ObjOf("arguments", fullArgs))),
				}, nil)
			}
		}
		return nil

	case "response.completed", "response.done":
		resp := jsonx.AsObj(data["response"])
		respUsage := jsonx.AsObj(jsonx.Get(resp, "usage"))
		if respUsage != nil {
			inputTokens := jsonx.AsF64(respUsage["input_tokens"])
			if inputTokens == 0 {
				inputTokens = jsonx.AsF64(respUsage["prompt_tokens"])
			}
			outputTokens := jsonx.AsF64(respUsage["output_tokens"])
			if outputTokens == 0 {
				outputTokens = jsonx.AsF64(respUsage["completion_tokens"])
			}
			cacheRead := jsonx.AsF64(jsonx.Get(respUsage["input_tokens_details"], "cached_tokens"))
			if cacheRead == 0 {
				cacheRead = jsonx.AsF64(respUsage["cache_read_input_tokens"])
			}
			s.Usage = BuildUsage(inputTokens, outputTokens, inputTokens+outputTokens, cacheRead, 0, 0)
		}
		if !s.FinishReasonSent {
			return s.finalChunk()
		}
		return nil

	case "error", "response.failed":
		if s.FinishReasonSent {
			return nil
		}
		errObj := jsonx.AsObj(data["error"])
		if errObj == nil {
			errObj = jsonx.AsObj(jsonx.Get(data["response"], "error"))
		}
		if errObj == nil {
			return nil
		}
		s.Error = errObj
		s.FinishReasonSent = true
		msg := jsonx.AsStr(errObj["message"])
		if msg == "" {
			b, _ := json.Marshal(errObj)
			msg = string(b)
		}
		id := s.ChatID
		if id == "" {
			id = fmt.Sprintf("chatcmpl-%d", s.timestamp())
		}
		created := s.Created
		if created == 0 {
			created = s.timestamp() / 1000
		}
		return BuildChunk(id, created, s.modelOrFallback(),
			map[string]any{"content": "[Error] " + msg}, "stop")

	case "response.reasoning_summary_text.delta":
		delta := jsonx.AsStr(data["delta"])
		if delta == "" {
			return nil
		}
		return s.chunk(ReasoningDelta(delta), nil)
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

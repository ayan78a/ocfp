package translate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"opencode-free-proxy/internal/jsonx"
)

// ChatToRespState carries openaiToOpenAIResponsesResponse's accumulator across
// chat chunks (Chat upstream → Responses client). Mirrors the JS state object
// field-for-field.
type ChatToRespState struct {
	Seq        int64
	Started    bool
	ResponseID string
	Created    int64
	Model      string

	InThinking bool

	ReasoningID        string
	ReasoningIndex     int
	ReasoningDone      bool
	ReasoningPartAdded bool
	ReasoningBuf       string

	MsgItemAdded    map[int]bool
	MsgContentAdded map[int]bool
	MsgTextBuf      map[int]string
	MsgItemDone     map[int]bool

	FuncCallIds   map[int]string
	FuncNames     map[int]string
	FuncArgsBuf   map[int]string
	FuncItemAdded map[int]bool
	FuncItemDone  map[int]bool
	FuncArgsDone  map[int]bool

	CustomToolNames map[string]bool

	CompletedSent bool
}

// NewChatToRespState seeds the state (stream.js createStreamState).
func NewChatToRespState(model string, created int64, responseID string) *ChatToRespState {
	return &ChatToRespState{
		Model:           model,
		Created:         created,
		ResponseID:      responseID,
		MsgItemAdded:    map[int]bool{},
		MsgContentAdded: map[int]bool{},
		MsgTextBuf:      map[int]string{},
		MsgItemDone:     map[int]bool{},
		FuncCallIds:     map[int]string{},
		FuncNames:       map[int]string{},
		FuncArgsBuf:     map[int]string{},
		FuncItemAdded:   map[int]bool{},
		FuncItemDone:    map[int]bool{},
		FuncArgsDone:    map[int]bool{},
		CustomToolNames: map[string]bool{},
	}
}

// Event is one emitted SSE frame.
type Event struct {
	Event string
	Data  map[string]any
}

func (s *ChatToRespState) emit(events *[]Event, eventType string, data map[string]any) {
	s.Seq++
	data["sequence_number"] = s.Seq
	*events = append(*events, Event{Event: eventType, Data: data})
}

func (s *ChatToRespState) startReasoning(events *[]Event, idx int) {
	if s.ReasoningID != "" {
		return
	}
	s.ReasoningID = "rs_" + s.ResponseID + "_" + itoa(idx)
	s.ReasoningIndex = idx

	s.emit(events, "response.output_item.added", jsonx.ObjOf(
		"type", "response.output_item.added",
		"output_index", idx,
		"item", jsonx.ObjOf("id", s.ReasoningID, "type", ItemReasoning, "summary", jsonx.ArrOf()),
	))
	s.emit(events, "response.reasoning_summary_part.added", jsonx.ObjOf(
		"type", "response.reasoning_summary_part.added",
		"item_id", s.ReasoningID,
		"output_index", idx,
		"summary_index", 0,
		"part", jsonx.ObjOf("type", ItemSummaryText, "text", ""),
	))
	s.ReasoningPartAdded = true
}

func (s *ChatToRespState) emitReasoningDelta(events *[]Event, text string) {
	if text == "" {
		return
	}
	s.ReasoningBuf += text
	s.emit(events, "response.reasoning_summary_text.delta", jsonx.ObjOf(
		"type", "response.reasoning_summary_text.delta",
		"item_id", s.ReasoningID,
		"output_index", s.ReasoningIndex,
		"summary_index", 0,
		"delta", text,
	))
}

func (s *ChatToRespState) closeReasoning(events *[]Event) {
	if s.ReasoningID == "" || s.ReasoningDone {
		return
	}
	s.ReasoningDone = true
	s.emit(events, "response.reasoning_summary_text.done", jsonx.ObjOf(
		"type", "response.reasoning_summary_text.done",
		"item_id", s.ReasoningID,
		"output_index", s.ReasoningIndex,
		"summary_index", 0,
		"text", s.ReasoningBuf,
	))
	s.emit(events, "response.reasoning_summary_part.done", jsonx.ObjOf(
		"type", "response.reasoning_summary_part.done",
		"item_id", s.ReasoningID,
		"output_index", s.ReasoningIndex,
		"summary_index", 0,
		"part", jsonx.ObjOf("type", ItemSummaryText, "text", s.ReasoningBuf),
	))
	s.emit(events, "response.output_item.done", jsonx.ObjOf(
		"type", "response.output_item.done",
		"output_index", s.ReasoningIndex,
		"item", jsonx.ObjOf(
			"id", s.ReasoningID,
			"type", ItemReasoning,
			"summary", jsonx.ArrOf(jsonx.ObjOf("type", ItemSummaryText, "text", s.ReasoningBuf))),
	))
}

func (s *ChatToRespState) msgID(idx int) string {
	return "msg_" + s.ResponseID + "_" + itoa(idx)
}

func (s *ChatToRespState) emitTextContent(events *[]Event, idx int, content string) {
	if !s.MsgItemAdded[idx] {
		s.MsgItemAdded[idx] = true
		s.emit(events, "response.output_item.added", jsonx.ObjOf(
			"type", "response.output_item.added",
			"output_index", idx,
			"item", jsonx.ObjOf(
				"id", s.msgID(idx),
				"type", ItemMessage,
				"content", jsonx.ArrOf(),
				"role", RoleAssistant),
		))
	}
	if !s.MsgContentAdded[idx] {
		s.MsgContentAdded[idx] = true
		s.emit(events, "response.content_part.added", jsonx.ObjOf(
			"type", "response.content_part.added",
			"item_id", s.msgID(idx),
			"output_index", idx,
			"content_index", 0,
			"part", jsonx.ObjOf("type", ItemOutputText, "annotations", jsonx.ArrOf(), "logprobs", jsonx.ArrOf(), "text", ""),
		))
	}
	s.emit(events, "response.output_text.delta", jsonx.ObjOf(
		"type", "response.output_text.delta",
		"item_id", s.msgID(idx),
		"output_index", idx,
		"content_index", 0,
		"delta", content,
		"logprobs", jsonx.ArrOf(),
	))
	s.MsgTextBuf[idx] += content
}

func (s *ChatToRespState) closeMessage(events *[]Event, idx int) {
	if !s.MsgItemAdded[idx] || s.MsgItemDone[idx] {
		return
	}
	s.MsgItemDone[idx] = true
	fullText := s.MsgTextBuf[idx]
	msgID := s.msgID(idx)

	s.emit(events, "response.output_text.done", jsonx.ObjOf(
		"type", "response.output_text.done",
		"item_id", msgID,
		"output_index", idx,
		"content_index", 0,
		"text", fullText,
		"logprobs", jsonx.ArrOf(),
	))
	s.emit(events, "response.content_part.done", jsonx.ObjOf(
		"type", "response.content_part.done",
		"item_id", msgID,
		"output_index", idx,
		"content_index", 0,
		"part", jsonx.ObjOf("type", ItemOutputText, "annotations", jsonx.ArrOf(), "logprobs", jsonx.ArrOf(), "text", fullText),
	))
	s.emit(events, "response.output_item.done", jsonx.ObjOf(
		"type", "response.output_item.done",
		"output_index", idx,
		"item", jsonx.ObjOf(
			"id", msgID,
			"type", ItemMessage,
			"content", jsonx.ArrOf(jsonx.ObjOf("type", ItemOutputText, "annotations", jsonx.ArrOf(), "logprobs", jsonx.ArrOf(), "text", fullText)),
			"role", RoleAssistant),
	))
}

func (s *ChatToRespState) isCustomTool(name string) bool {
	return name != "" && s.CustomToolNames[name]
}

// extractCustomToolInput unwraps the {"input": "..."} JSON wrapper Chat's
// custom-tool encoding adds; unparsable fragments pass through raw.
func extractCustomToolInput(argumentsText string) string {
	trimmed := strings.TrimSpace(argumentsText)
	var parsed any
	if json.Unmarshal([]byte(trimmed), &parsed) == nil {
		if o, isObj := parsed.(map[string]any); isObj {
			if in, isStr := o["input"].(string); isStr {
				return in
			}
		}
	}
	return argumentsText
}

func (s *ChatToRespState) emitToolCall(events *[]Event, tc map[string]any) {
	tcIdx := 0
	if v, is := tc["index"].(float64); is {
		tcIdx = int(v)
	}
	newCallID := jsonx.AsStr(tc["id"])
	fn := jsonx.AsObj(tc["function"])
	funcName := jsonx.AsStr(jsonx.Get(fn, "name"))

	if funcName != "" {
		s.FuncNames[tcIdx] = funcName
	}
	if newCallID != "" {
		s.FuncCallIds[tcIdx] = newCallID
	}

	// Wait for id AND name before announcing: some providers split them
	// across chunks and an early announce mislabels custom tools.
	callID := s.FuncCallIds[tcIdx]
	if !s.FuncItemAdded[tcIdx] && callID != "" && s.FuncNames[tcIdx] != "" {
		s.FuncItemAdded[tcIdx] = true
		custom := s.isCustomTool(s.FuncNames[tcIdx])
		prefix := "fc"
		item := jsonx.ObjOf(
			"id", prefix+"_"+callID,
			"type", ItemFunctionCall,
			"call_id", callID,
			"name", s.FuncNames[tcIdx],
		)
		if custom {
			item["id"] = "ctc_" + callID
			item["type"] = ItemCustomToolCall
			item["input"] = ""
		} else {
			item["arguments"] = ""
		}
		s.emit(events, "response.output_item.added", jsonx.ObjOf(
			"type", "response.output_item.added",
			"output_index", tcIdx,
			"item", item,
		))
	}

	if args := jsonx.AsStr(jsonx.Get(fn, "arguments")); args != "" {
		refCallID := s.FuncCallIds[tcIdx]
		if refCallID == "" {
			refCallID = newCallID
		}
		if s.FuncItemAdded[tcIdx] && refCallID != "" && !s.isCustomTool(s.FuncNames[tcIdx]) {
			s.emit(events, "response.function_call_arguments.delta", jsonx.ObjOf(
				"type", "response.function_call_arguments.delta",
				"item_id", "fc_"+refCallID,
				"output_index", tcIdx,
				"delta", args,
			))
		}
		// Custom input is emitted once at close (raw fragments would leak the
		// {"input":"..."} wrapper Codex must not see).
		s.FuncArgsBuf[tcIdx] += args
	}
}

func (s *ChatToRespState) closeToolCall(events *[]Event, idx int) {
	callID := s.FuncCallIds[idx]
	if callID == "" || s.FuncItemDone[idx] {
		return
	}
	args := s.FuncArgsBuf[idx]
	if args == "" {
		args = "{}"
	}
	custom := s.isCustomTool(s.FuncNames[idx])

	if custom {
		input := extractCustomToolInput(args)
		s.emit(events, "response.custom_tool_call_input.delta", jsonx.ObjOf(
			"type", "response.custom_tool_call_input.delta",
			"item_id", "ctc_"+callID,
			"output_index", idx,
			"delta", input,
		))
		s.emit(events, "response.custom_tool_call_input.done", jsonx.ObjOf(
			"type", "response.custom_tool_call_input.done",
			"item_id", "ctc_"+callID,
			"output_index", idx,
			"input", input,
		))
	} else {
		s.emit(events, "response.function_call_arguments.done", jsonx.ObjOf(
			"type", "response.function_call_arguments.done",
			"item_id", "fc_"+callID,
			"output_index", idx,
			"arguments", args,
		))
	}

	item := jsonx.ObjOf(
		"id", "fc_"+callID,
		"type", ItemFunctionCall,
		"call_id", callID,
		"name", s.FuncNames[idx],
	)
	if custom {
		item["id"] = "ctc_" + callID
		item["type"] = ItemCustomToolCall
		item["input"] = extractCustomToolInput(args)
	} else {
		item["arguments"] = args
	}
	s.emit(events, "response.output_item.done", jsonx.ObjOf(
		"type", "response.output_item.done",
		"output_index", idx,
		"item", item,
	))

	s.FuncItemDone[idx] = true
	s.FuncArgsDone[idx] = true
}

func (s *ChatToRespState) sendCompleted(events *[]Event) {
	if s.CompletedSent {
		return
	}
	s.CompletedSent = true
	s.emit(events, "response.completed", jsonx.ObjOf(
		"type", "response.completed",
		"response", jsonx.ObjOf(
			"id", s.ResponseID,
			"object", "response",
			"created_at", s.Created,
			"status", "completed",
			"background", false,
			"error", nil,
		),
	))
}

// flushEvents closes any open items and completes the response.
func (s *ChatToRespState) flushEvents() []Event {
	if s.CompletedSent {
		return nil
	}
	var events []Event
	// JS `for (const i in obj)` walks integer-like keys in ascending numeric
	// order — replicate it so parallel items close deterministically.
	for _, idx := range sortedKeys(s.MsgItemAdded) {
		s.closeMessage(&events, idx)
	}
	s.closeReasoning(&events)
	for _, idx := range sortedKeys(s.FuncCallIds) {
		s.closeToolCall(&events, idx)
	}
	s.sendCompleted(&events)
	return events
}

func sortedKeys[T any](m map[int]T) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// Convert translates one chat chunk into Responses events. Nil chunk flushes.
func (s *ChatToRespState) Convert(chunk map[string]any) []Event {
	if chunk == nil {
		return s.flushEvents()
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	var events []Event
	choice := jsonx.AsObj(choices[0])
	if choice == nil {
		return nil
	}
	idx := 0
	if v, is := choice["index"].(float64); is {
		idx = int(v)
	}
	delta := jsonx.AsObj(choice["delta"])
	if delta == nil {
		delta = jsonx.ObjOf()
	}

	if !s.Started {
		s.Started = true
		if id := jsonx.AsStr(chunk["id"]); id != "" {
			s.ResponseID = "resp_" + id
		}
		if s.ResponseID == "" {
			// JS initState: responseId: `resp_${Date.now()}` — a Responses
			// client must always see the scaffold, even when the first chat
			// chunk carries no id (openai-responses.js response translator).
			s.ResponseID = fmt.Sprintf("resp_%d", time.Now().UnixMilli())
		}
		// JS nests the payload under `response` with a `type` marker
		// (openai-responses.js response translator scaffold emits).
		s.emit(&events, "response.created", jsonx.ObjOf(
			"type", "response.created",
			"response", jsonx.ObjOf(
				"id", s.ResponseID,
				"object", "response",
				"created_at", s.Created,
				"status", "in_progress",
				"background", false,
				"error", nil,
				"output", jsonx.ArrOf())))
		s.emit(&events, "response.in_progress", jsonx.ObjOf(
			"type", "response.in_progress",
			"response", jsonx.ObjOf(
				"id", s.ResponseID,
				"object", "response",
				"created_at", s.Created,
				"status", "in_progress")))
	}

	// Reasoning across vendor shapes.
	if reasoningText := ExtractReasoningText(delta); reasoningText != "" {
		s.startReasoning(&events, idx)
		s.emitReasoningDelta(&events, reasoningText)
	}

	if content := jsonx.AsStr(delta["content"]); content != "" {
		if strings.Contains(content, "<think>") {
			s.InThinking = true
			content = strings.Replace(content, "<think>", "", 1)
			s.startReasoning(&events, idx)
		}
		if strings.Contains(content, "</think>") {
			// JS split/join semantics: first part is reasoning, the rest
			// re-joined with the delimiter stays content.
			parts := strings.Split(content, "</think>")
			thinkPart := parts[0]
			textPart := strings.Join(parts[1:], "</think>")
			if thinkPart != "" {
				s.emitReasoningDelta(&events, thinkPart)
			}
			s.closeReasoning(&events)
			s.InThinking = false
			content = textPart
		}
		if s.InThinking && content != "" {
			s.emitReasoningDelta(&events, content)
			return events
		}
		if content != "" {
			s.emitTextContent(&events, idx, content)
		}
	}

	// tool_calls: empty array is truthy in JS — require a real call.
	if calls, is := delta["tool_calls"].([]any); is && len(calls) > 0 {
		s.closeMessage(&events, idx)
		for _, raw := range calls {
			if tc := jsonx.AsObj(raw); tc != nil {
				s.emitToolCall(&events, tc)
			}
		}
	}

	if finish := jsonx.AsStr(choice["finish_reason"]); finish != "" {
		for i := range s.MsgItemAdded {
			s.closeMessage(&events, i)
		}
		s.closeReasoning(&events)
		for i := range s.FuncCallIds {
			s.closeToolCall(&events, i)
		}
		s.sendCompleted(&events)
	}

	return events
}

// itoa is a tiny int→string helper keeping the id concatenations readable.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

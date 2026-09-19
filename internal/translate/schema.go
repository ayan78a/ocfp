// Package translate ports the 9router request/response translators this proxy
// needs: OpenAI Chat Completions ↔ OpenAI Responses API, both requests and
// streams. Mirrors open-sse/translator/{request,response}/openai-responses.js.
package translate

// Roles (schema/roles.js).
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleDeveloper = "developer"
)

// OpenAI chat content blocks (schema/blocks.js OPENAI_BLOCK).
const (
	BlockText     = "text"
	BlockImageURL = "image_url"
	BlockFunction = "function"
)

// Responses API item types (schema/blocks.js RESPONSES_ITEM).
const (
	ItemMessage              = "message"
	ItemFunctionCall         = "function_call"
	ItemFunctionCallOutput   = "function_call_output"
	ItemCustomToolCall       = "custom_tool_call"
	ItemCustomToolCallOutput = "custom_tool_call_output"
	ItemAdditionalTools      = "additional_tools"
	ItemReasoning            = "reasoning"
	ItemOutputText           = "output_text"
	ItemInputText            = "input_text"
	ItemInputImage           = "input_image"
	ItemSummaryText          = "summary_text"
)

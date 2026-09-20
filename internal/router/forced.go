package router

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"opencode-free-proxy/internal/cloak"
	"opencode-free-proxy/internal/relay"
)

// forcedUpstreamIsSSE ports the handleForcedSSEToJson gate
// (open-sse/handlers/chatCore/sseToJsonHandler.js:185-188):
//
//	isSSE = contentType.includes("text/event-stream")
//	        || (contentType === "" && isResponsesProvider(provider))
//
// isResponsesProvider(p) is `PROVIDERS[p]?.format === FORMATS.OPENAI_RESPONSES`
// (sseToJsonHandler.js:12). For this proxy's single provider the transport
// declares no format, so PROVIDERS["opencode"].format is the schema default
// "openai" (providers/index.js:14 + providers/schema.js:44-46;
// providers/registry/opencode.js has no transport.format — its muse models
// carry a per-model targetFormat instead, which is NOT what
// isResponsesProvider reads). The empty-content-type disjunct is therefore
// dead here, and a non-streaming client whose upstream reply lacks both an
// SSE content type and a parseable stream falls through to s.stream(...) —
// exactly the JS flow when the gate returns null (chatCore.js:456-470 falls
// through to handleStreamingResponse).
func forcedUpstreamIsSSE(resp *http.Response) bool {
	return strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
}

// forcedSSEToJson ports handleForcedSSEToJson: the upstream always streams
// (forceStream quirk) but the client asked for JSON. Branching follows the
// UPSTREAM format — a Responses client behind a chat-native upstream still
// gets the standard path. The SSE content-type gate lives at the relay() call
// site (forcedUpstreamIsSSE above); reaching this function with a non-SSE
// body is the JS parse-failure case, which answers 502 below
// (sseToJsonHandler.js "Failed to convert streaming response to JSON").
func (s *Server) forcedSSEToJson(w http.ResponseWriter, r *http.Request, resp *http.Response, sourceFormat, targetFormat relay.Format, model string, customToolNames map[string]bool, reqBody map[string]any, upstreamModel string, intent *cloak.ThinkingCfg) {
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Failed to convert streaming response to JSON")
		return
	}
	synthesize := nonstreamSynthesize(sourceFormat, reqBody, upstreamModel, intent)

	if targetFormat == relay.FormatResponses {
		// Muse Spark upstream spoke Responses SSE.
		jsonResponse := relay.ConvertResponsesStreamToJson(string(raw))
		if sourceFormat == relay.FormatResponses {
			// Responses client: return the aggregated object as-is (usage
			// synthesized on the client-facing copy only).
			if usage, has := jsonResponse["usage"].(map[string]any); has {
				jsonResponse["usage"] = synthesize(usage)
			}
			writeJSON(w, http.StatusOK, jsonResponse)
			return
		}
		writeJSON(w, http.StatusOK, relay.BuildChatFromResponses(jsonResponse, model, synthesize))
		return
	}

	// Standard Chat Completions SSE path.
	parsed, errBody, ok := relay.ParseSSEToOpenAIResponse(string(raw), model)
	if !ok {
		writeError(w, http.StatusBadGateway, "Invalid SSE response for non-streaming request")
		return
	}
	if errBody != nil {
		msg, _ := errBody["message"].(string)
		if msg == "" {
			msg = "Upstream SSE stream failed"
		}
		writeError(w, http.StatusBadGateway, msg)
		return
	}
	if usage, has := parsed["usage"].(map[string]any); has && len(usage) > 0 {
		parsed["usage"] = synthesize(usage)
	}
	relay.StripRedundantReasoning(parsed)
	if sourceFormat == relay.FormatResponses {
		writeJSON(w, http.StatusOK, synthesizeFinal(relay.ChatCompletionToResponses(parsed, customToolNames), synthesize))
		return
	}
	writeJSON(w, http.StatusOK, parsed)
}

// synthesizeFinal re-applies the seam on the converted shape —
// chatCompletionToResponses rebuilds usage from scratch and drops the details
// objects (sseToJsonHandler.js final synthesize).
func synthesizeFinal(body map[string]any, synthesize func(map[string]any) map[string]any) map[string]any {
	if usage, has := body["usage"].(map[string]any); has {
		body["usage"] = synthesize(usage)
	}
	return body
}

// writeJSON emits a JSON body with CORS headers.
func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	corsHeaders(w.Header(), false)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

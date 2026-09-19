package router

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"opencode-free-proxy/internal/jsonx"
)

// Test-connection probe, ported from open-sse/utils/testConnectionHandler.js
// via its only call site src/sse/handlers/chat.js:91-97: a chat request
// carrying the probe header is answered immediately with a fixed synthetic
// chat.completion — no upstream call, no alias/rotation work, no usage
// accounting. The model is echoed verbatim and a fresh id is generated per
// response; every other field is constant. Always non-streaming OpenAI JSON,
// even when the client asked to stream.
//
// Scope verified: grep isTestConnectionRequest across src/sse/handlers/ hits
// ONLY src/sse/handlers/chat.js:94 (open-sse/handlers/chatCore.js never calls
// it). chat.js handleChat is the shared handler for /v1/chat/completions,
// /v1/messages, /v1/responses and /v1/responses/compact (each route.js calls
// handleChat), so the probe is live on every route this proxy mirrors with
// relay() — and absent from the routes that bypass handleChat (models,
// embeddings, …).

// Constants from open-sse/config/runtimeConfig.js:102-105.
const (
	testConnectionHeader   = "x-test-connection"
	testConnectionIDPrefix = "router-"
	testConnectionObject   = "chat.completion"
	testConnectionContent  = "Hello!"
)

// isTestConnectionRequest ports testConnectionHandler.js isTestConnectionRequest
// (:26-28): header PRESENCE with any value — `headers.get(name) != null` — so a
// present-but-empty header still counts. Go's Header map lookup keeps that
// distinction (Header.Get would collapse empty and absent).
func isTestConnectionRequest(r *http.Request) bool {
	_, ok := r.Header[http.CanonicalHeaderKey(testConnectionHeader)]
	return ok
}

// createTestConnectionResponse ports testConnectionHandler.js
// createTestConnectionResponse (:45-66): a 200 JSON body with Content-Type
// application/json + Access-Control-Allow-Origin * (the proxy attaches the
// full CORS set to every response, corsHeaders).
func createTestConnectionResponse(w http.ResponseWriter, model string) {
	// randomHex32 (:31-38): 32 hex chars from 16 crypto-random bytes
	// (crypto.randomUUID minus dashes, getRandomValues fallback).
	idBytes := make([]byte, 16)
	_, _ = rand.Read(idBytes)

	payload := jsonx.ObjOf(
		"id", testConnectionIDPrefix+hex.EncodeToString(idBytes),
		"object", testConnectionObject,
		"created", time.Now().Unix(), // :49 Math.floor(Date.now() / 1000)
		"model", model, // echoed verbatim from the stripped request model
		"choices", jsonx.ArrOf(jsonx.ObjOf(
			"index", float64(0),
			"message", jsonx.ObjOf("role", "assistant", "content", testConnectionContent), // :53
			"finish_reason", "stop", // :54 OPENAI_FINISH.STOP
		)),
		// TEST_CONNECTION_USAGE (runtimeConfig.js:106-111), hardcoded per the
		// port contract (structuredClone of the constant, :56).
		"usage", jsonx.ObjOf(
			"prompt_tokens", float64(18),
			"completion_tokens", float64(1),
			"total_tokens", float64(19),
			"prompt_tokens_details", jsonx.ObjOf("cached_tokens", float64(0)),
		),
	)

	b, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	corsHeaders(w.Header(), false)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

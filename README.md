# opencode-free-proxy

A minimal OpenAI-compatible router that exposes **only** the OpenCode free
provider, ported line-for-line from the hardened OpenCode handling in
`9router-src` (`open-sse/`). Chat Completions and Responses APIs, streaming
and non-streaming.

## Run

```sh
go run ./cmd/server          # listens on :8090, upstream https://opencode.ai
```

Environment:

| Var | Default | Meaning |
|---|---|---|
| `PORT` | `8090` | Listen port |
| `OFP_UPSTREAM_BASE` | `https://opencode.ai` | Zen upstream base (all routes incl. `/v1/models`) |
| `OFP_API_KEY` | *(empty = auth off)* | Bearer key required from clients |

## Endpoints

- `POST /v1/chat/completions` — OpenAI Chat Completions (SSE or JSON).
- `POST /v1/responses` — OpenAI Responses API (SSE or JSON).
- `GET /v1/models` — live free-tier model list from the upstream
  (ids ending `-free` plus `big-pickle`, minus known-dead ids), with a static
  registry fallback.
- `GET /healthz`.

## Model naming

`oc/` prefix is optional. Thinking suffixes work on any model:
`muse-spark-1.2-contributor-free(high)`, `(8192)`, `(none)`, `(auto)` —
parsed, applied to the upstream body (`reasoning.effort` / `reasoning_effort`)
and stripped before dispatch. `muse-spark*` models speak the Responses API
upstream and are translated transparently for chat clients.

## Pipeline (mirrors 9router chatCore + the chat.js pre-resolution stages)

1. `[1m]` context-marker strip (Claude Code 1M beta annotation).
2. Auth → missing-model check → `x-test-connection` probe (fixed synthetic
   completion, no upstream call) → claude-cli bypass short-circuit (warmup /
   title extraction / count / title-prompt patterns answer without an upstream
   call, streaming or not).
3. Detect endpoint format → snapshot the client's thinking intent.
4. Unsupported-modality strip: `image_url` / `input_image` / `file` / audio
   blocks are replaced with text placeholders when the model's resolved
   modality capabilities (exact table → glob patterns → name heuristic) say
   the model can't read them.
5. Prenorms: `normalizeThinkingConfig` → `ensureToolCallIds` →
   `fixMissingToolResponses`.
6. Format translation (`needsTranslation` when source ≠ target).
7. `applyThinking` (suffix/effort resolution) + `filterToOpenAIFormat` for
   chat-native targets, then claude-client tool dedupe (MCP/built-in
   duplicates).
8. Executor: session resolution (`x-opencode-*` headers, claude-code /
   antigravity extraction, assistant-text hashing), request transform
   (fingerprint tools, `max_output_tokens` clamp, `store=false`, input
   normalization), header forging (`Bearer public`, UA version gate ≥ 1.17
   with pinned fallback, `x-opencode-client/request/project/session`) —
   headers are rebuilt on every retry attempt.
9. Retry matrix: 429 → no retry; 502 ×3 @3s; 503 ×3 @2s; 504 ×2 @3s;
   network errors follow 502 — one shared attempt budget across all retryable
   statuses. 60 s response-header timeout, 360 s stream stall (reset per line).
10. Relay: format-matched passthrough (with usage estimation seam) or
    translation; non-streaming clients get the forced SSE→JSON aggregate with
    the same usage/thinking synthesis as the JS router (only when the upstream
    actually answered SSE — otherwise the stream path handles it, exactly like
    the JS fall-through).

## Layout

| Package | Ports |
|---|---|
| `internal/identity` | session/request-id generation, UA cache, session resolution chain |
| `internal/cloak` | thinking suffix parse/apply, model id/URL, fingerprint tools, responses sanitization |
| `internal/translate` | request translators (chat ↔ responses), SSE state machines, prenorms |
| `internal/relay` | passthrough/translate SSE relays, SSE→JSON aggregation, usage seam |
| `internal/usage` | usage normalization/merge/estimation/thinking synthesis |
| `internal/upstream` | HTTP client (retry, SSE line scan), executor transforms, headers |
| `internal/caps` | per-model input-modality resolution (vision/pdf/audio/video) |
| `internal/router` | endpoints, chatCore pipeline, forced-SSE-to-JSON, bypass/test-connection/modality/tool-dedupe stages |
| `e2e/` | black-box e2e suite (`-tags e2e`): compiled server subprocess + fake upstream; opt-in live suite |

## Tests

```sh
go test ./...                # offline unit suite
go test -tags e2e ./e2e/     # black-box e2e: compiles the server, runs it as a
                             # subprocess against a fake zen upstream
E2E_LIVE=1 go test -tags e2e ./e2e/ -run TestLive   # optional: real proxy + real upstream
```

See `e2e/README.md`. Golden unit vectors are ported from
`9router-src/tests/unit/opencode-*.test.js`
(session ids, client version gate, tool-choice forcing, max output tokens,
muse-spark thinking) plus end-to-end httptest coverage of every relay branch.

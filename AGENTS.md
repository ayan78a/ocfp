# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

`opencode-free-proxy` is a minimal OpenAI-compatible router exposing **only**
the OpenCode **free** provider (opencode zen free tier). It is a
line-for-line port of the hardened OpenCode handling in the 9router sources
(`~/9router/9router-src/open-sse/`), which are **not** part of this repo —
they are the external source of truth for behavior.

Scope is fixed: free tier only (`Bearer public`, fingerprint-tool gate,
`-free` + `big-pickle` models). No paid-tier, multi-provider, or API-key
upstream support belongs here.

## Commands

```sh
go build ./...                       # compile
go test ./...                        # offline unit suite (httptest only, no egress)
go test -tags e2e ./e2e/             # black-box e2e: builds cmd/server, runs it as a
                                     #   subprocess against a fake zen upstream
E2E_LIVE=1 go test -tags e2e ./e2e/ -run TestLive   # optional: real proxy + real upstream
go vet ./... && go vet -tags e2e ./e2e/ && gofmt -l .   # must all be clean
```

All three gates (vet both tag sets, gofmt, full test suites) must pass before
every commit. Commit messages follow Conventional Commits (`feat:`, `fix:`,
`test:`, `docs:`).

## Layout

| Package | Role |
|---|---|
| `cmd/server` | entrypoint; also serves `healthcheck` (Docker HEALTHCHECK on `scratch`) |
| `internal/config` | every runtime constant + env vars (`PORT`, `OFP_API_KEY`, `OFP_UPSTREAM_BASE`) |
| `internal/router` | endpoints + chatCore pipeline + bypass/test-connection/modality/tool-dedupe stages |
| `internal/relay` | passthrough/translate SSE relays, SSE→JSON aggregation, usage seam |
| `internal/translate` | request translators (chat ↔ responses), SSE state machines, prenorms, modality strip |
| `internal/upstream` | HTTP client (retry matrix, SSE line scan), executor transforms, header forging |
| `internal/cloak` | thinking suffix parse/apply, model id/URL, fingerprint tools |
| `internal/identity` | session/request ids, opencode UA triple cache + GitHub sync loop (fail-open), session resolution chain |
| `internal/caps` | per-model input-modality resolution (exact table → glob patterns → name heuristic) |
| `internal/usage` | usage normalization/merge/estimation/thinking synthesis |
| `internal/jsonx` | JS-semantics JSON accessors (`AsStr`/`AsArr`/`Truthy`/…) |
| `e2e/` | black-box e2e suite behind the `e2e` build tag (see `e2e/README.md`) |

## Porting discipline (the rules that keep parity)

1. **Cite the JS source.** Every ported behavior carries its source location
   in the Go comment (`bypassHandler.js:34-39`, `chatCore.js:169-180`, …).
   A comment without a citation is a parity risk.
2. **JS semantics, not Go reflexes.** Falsy/truthiness chains (`!x`,
   `x?.error`) go through `jsonx` helpers — never Go zero-value checks. `[]`
   and `{}` are truthy in JS; `"0"` is truthy; `null`/`false`/`0`/`""` are not.
3. **Constants live only in `internal/config`**, mirroring `open-sse/config`
   (URLs, retry matrix, timeouts, fingerprints, error envelopes). Nothing
   upstream-shaped may be hardcoded elsewhere.
4. **Deliberate divergences are documented**, with the reason, at the site:
   e.g. the JS `customToolNames?.has()` array crash is not replicated, the
   ccFilterNaming bypass pattern (P5) is dropped for lack of the settings
   flag, `formatDataLine`'s key re-serialization order differs (Go sorts map
   keys — semantics unchanged). If you can't articulate why Go differs, Go
   is wrong.
5. **Behavioral changes need JS evidence.** Before "fixing" relay/pipeline
   behavior, read the matching 9router code and cite it — the weirdness is
   usually load-bearing (double `[DONE]` on chat→chat, `data: null` drops,
   non-iterable `choices` chunk drops, shared retry budget across statuses).
6. **Fail-open vs fail-closed is part of the contract** (UA cache warm probe,
   models fallback to the static registry, 429 never retried). Keep it.

## Environment

| Var | Default | Meaning |
|---|---|---|
| `PORT` | `8090` | Listen port (`0` valid in tests) |
| `OFP_API_KEY` | *(empty = auth off)* | Bearer key required from clients |
| `OFP_UPSTREAM_BASE` | `https://opencode.ai` | Zen upstream base, all routes |
| `OFP_UA_SYNC_INTERVAL` | `3600000` | UA identity sync cadence in ms (background ticker; hot path never fetches) |

## Security

Never commit credentials, live API keys, or real session ids — test fixtures
use clearly fake values (`e2e-secret`, `public`). The 9router workspace may
contain live secrets; never copy anything from it into this repo.

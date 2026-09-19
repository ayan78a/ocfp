# E2E tests

Black-box tests: the real server binary is compiled, launched as a subprocess
(`PORT` / `OFP_API_KEY` / `OFP_UPSTREAM_BASE`) against a fake OpenCode Zen
upstream, and exercised over HTTP like any external client.

They are behind the `e2e` build tag — the default `go test ./...` unit suite
(fully offline, `httptest`-only) never builds them.

## Local suite (fake upstream, hermetic)

```sh
go test -tags e2e ./e2e/
```

`TestMain` builds `./cmd/server`, starts a fake `/zen/v1` upstream (SSE chat +
responses + models, request recording, a loose replica of the free-tier gate:
mandatory `stream:true`, `opencode/*` UA) and polls `/healthz` before the tests
run.

Covered, end to end through the wire:

- `/healthz`; auth on/off (401 envelope + CORS, no upstream call)
- chat non-streaming: forced upstream SSE → JSON aggregate (content, usage
  5/2/7, finish reason); upstream sees `stream:true`, `Bearer public`,
  `opencode/*` UA, the fingerprint tool quartet
- chat streaming: deltas + usage chunk + `[DONE]` forwarded
- `/v1/responses` non-streaming → aggregated `response` object (id, status
  `completed`, usage) against the muse-spark upstream path
- `/v1/responses` streaming → event-framed SSE, no synthesized
  `response.failed` when a terminal event exists, `[DONE]` terminator
- upstream 403 → `[403]: …` error envelope (`permission_error` /
  `insufficient_quota`)
- `x-test-connection` probe → fixed `Hello!` completion, zero upstream calls
- claude-cli `Warmup` bypass → synthetic completion, zero upstream calls
- `big-pickle[1m]` → marker stripped before dispatch
- `image_url` on a non-vision model → stripped, placeholder text upstream
- `/v1/models` → free filter (`-free` + `big-pickle`, dead ids dropped, sorted)

## Live suite (real proxy + real upstream)

Against an already-running proxy (default `http://127.0.0.1:8090`). Skipped
unless `E2E_LIVE=1`; spends free-tier quota.

```sh
E2E_LIVE=1 go test -tags e2e ./e2e/ -run TestLive
# optional:
#   E2E_BASE_URL=http://127.0.0.1:9000   target proxy
#   E2E_API_KEY=...                      value of the proxy's OFP_API_KEY
```

Covers the live model list (free filter holds), one tiny chat completion, and
one muse-spark streaming responses request.

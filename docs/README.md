# docs

Investigation and recon records that would otherwise get lost between
sessions. Public artifact — English, dated, with source citations.

| File | What it covers |
|---|---|
| `recon-opencode-ua.md` | How the official opencode CLI builds its compound User-Agent, the live binary capture, the per-segment version chains, and the sync design they feed |
| `recon-session-continuity.md` | Live upstream check: continuation requests with different session / project ids — error behavior, server-side memory, validation, and prompt-cache keying |
| `recon-ip-switch.md` | Live upstream check: one conversation continued across a mid-stream egress IP change with fully rotated identity — blocking behavior, continuity, prompt-cache keying |

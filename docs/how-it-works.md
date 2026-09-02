# How It Works — Proem

## Auth variants (validated + live-captured)

- `ANTHROPIC_API_KEY` -> `x-api-key`
- `ANTHROPIC_AUTH_TOKEN` -> `Authorization: Bearer` generic
- `CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...` -> `Authorization: Bearer <oat> + anthropic-beta: oauth-2025-04-20`

Precedence API wins over oauth. Captured via `@anthropic-ai/claude-agent-sdk` query() against mock server: oat sends `Authorization: Bearer sk-ant-oat01... + anthropic-beta: oauth-2025-04-20 + x-claude-code-session-id`. See `probe/capture.log`.

## Two layers of credentials

The proxy sits between two independent credentials, and they must not be confused:

| | Held by | Looks like | Checked against |
| --- | --- | --- | --- |
| **Client token** | the calling agent | `sk-ant-oat01-<random>` (issued by the proxy) | `clients.yaml` digests |
| **Pool credential** | the proxy | a real Anthropic oat / API key / vendor key | the upstream provider |

The client token is authenticated and then removed; the pool credential is injected in its place. A caller never sees a real upstream credential, and a leaked client token grants nothing beyond this proxy. See [client tokens](client-tokens.md).

## Request flow

1. SDK sets `ANTHROPIC_BASE_URL=http://proem:8080` and `CLAUDE_CODE_OAUTH_TOKEN=<issued token>`, then sends `POST /v1/messages`.
2. `proxy.Auth` resolves the token against the client registry. Unknown token → 401, disabled client → 403. On success the client name goes on the request context and the caller's `Authorization` / `x-api-key` headers are stripped.
3. `handler.ServeHTTP` clones body for replay, reads pool `atomic` snapshot, extracts `x-claude-code-session-id`.
4. Loop:
   a. `router.Pick` filters `IsEnabled` + `FilterHealthy` (MGet cooldown:{ids}). Checks sticky if `trySticky` (first attempt, redis mode).
   b. `forward` builds `httptarget = member.BaseURL + original path+query`, copies headers, `AuthHeaders`, `RewriteBody` modelMap, `Do`.
   c. `ShouldFailover` checks `Retry-After` OR status+keyword. If true, `SetCooldown(member, CooldownTTL)` + `Failovers` inc, continue loop.
   d. Success: `SetSticky(session, member)` if redis, `recordTokens` (usage.input_tokens/output_tokens), `Requests/Latency` observe, write response.
5. Exhausted: return last status/body or 502.

## Cooldown TTL

`Retry-After` header value if present and numeric else `member.CooldownSec` else `18000` anthropic else `60`.

## ModelMap

For `openrouter/deepseek`, proxy rewrites `"model":"claude-sonnet-4"` -> `"model":"anthropic/claude-sonnet-4"` via string replace (fast, no JSON unmarshal). Add `modelMap` per member.

## Metrics

Scrape `:9090/metrics`. `requests`, `tokens`, `latency` and `failovers` all carry a `client` label for per-agent attribution. Alert on `proem_member_cooldown==1` for all members, `rate(proem_failovers_total[5m])` spike, `proem_tokens_total` per member for cost.

## Streaming and failover

The proxy is transparent to the response shape: a client that asks to stream gets a stream, and one that does not gets a single body. Neither is buffered unnecessarily, and bytes are never rewritten.

That has to coexist with failover, which needs to see a response before deciding to retry — and bytes already sent cannot be recalled. The decision is therefore made from the status and headers alone, before anything is committed:

1. `failover.MayFailover(status, headers)` asks whether this response could still trigger a retry (a `429/401/529`, or any response carrying `Retry-After`).
2. If it could, the body is read into memory and `ShouldFailover` inspects it. These are small error envelopes. On a match the member is cooled and the next one is tried.
3. Otherwise the response is committed and copied straight through, flushed chunk by chunk, so a stream reaches the caller as it is produced.

A usage observer reads along with the copy without altering or delaying it, extracting token counts from either a JSON `usage` object or the `message_start` / `message_delta` events of a stream. Upstreams disagree about which event carries which counter, so every counter is merged from whichever event reports it.

Once streaming has begun the response is committed: an error arriving mid-stream is passed through to the client rather than failed over, because the client has already seen part of the answer.

`--upstream-timeout` bounds how long a member may take to *respond*, not how long it may stream: it maps to the transport's response-header timeout, so a long generation is never truncated.

## Limits

- Redis down = fail-open (all healthy), sticky miss.
- A member that fails after the first byte cannot be failed over.

---

[← Back to README](../README.md) · [Client tokens](client-tokens.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

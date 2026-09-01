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

## Limits

- No streaming chunked passthrough yet (buffers body). For streaming, switch `forward` to `httputil.ReverseProxy` with `FlushInterval`.
- Redis down = fail-open (all healthy), sticky miss.

---

[← Back to README](../README.md) · [Client tokens](client-tokens.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

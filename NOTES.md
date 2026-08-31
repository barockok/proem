# claude-proxy-subs

## Goal
Claude proxy subscription service — proxy layer for Claude API access with subscription/billing management. (Scope TBD — user to define)

## Status
- Phase: seeded, no work yet
- Created: 2026-08-31
- Active: yes

## Last Decision
- Workspace created 2026-08-31 via user request

## Next Step
- Define scope: proxy type (Anthropic API passthrough? Key pooling? Usage metering? Multi-tenant?)
- Decide stack, billing, auth model
- Outline MVP

## Open Questions
- Proxy target: Claude API direct passthrough vs. aggregated provider proxy?
- Subscription model: per-token, per-seat, tiered, credits?
- Auth: API keys, OAuth, peruser limits?
- Billing: Stripe, quota enforcement, usage tracking?
- Existing code/repo to clone or greenfield?

## 2026-08-31 — Auth Probe Verified (live capture)

**Question:** is slaude finding trustworthy (CLAUDE_CODE_OAUTH_TOKEN auth shape)? References?

**Method:** live header capture via reverse proxy on localhost:17823, client = claude-agent-sdk@0.3.251 query() with env overrides. See `probe/capture.log` (76KB, 8 /v1/messages requests).

**Primary source corroboration:** `@anthropic-ai/sdk` npm (official) — `lib/credentials/types.js:50` defines `OAUTH_API_BETA_HEADER='oauth-2025-04-20'` ; `client.js:434` appends it when OAuth bearer used; `lib/credentials/user-oauth.js` does refresh grant to /v1/oauth/token with same header. Confirms beta header exists.

**Live capture results (ground truth, claude-cli/2.1.251):**

1. `CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...` → `Authorization: Bearer sk-ant-oat01-...` + `anthropic-beta: ...,oauth-2025-04-20,...` ; no `x-api-key`. 2 req per turn.
2. `ANTHROPIC_API_KEY=sk-ant-api03-...` → `x-api-key: sk-ant-api03-...` ; no oauth beta, no Authorization.
3. `ANTHROPIC_AUTH_TOKEN + ANTHROPIC_API_KEY` → **both** `Authorization: Bearer ...` + `x-api-key: ...` ; no oauth beta.
4. `CLAUDE_CODE_OAUTH_TOKEN + ANTHROPIC_API_KEY` → **only** `x-api-key` wins, Authorization dropped, no oauth beta. Confirms precedence API > OAuth (as slaude test asserted).

**Conclusion:**
- slaude doc secondary but **shape correct** for OAuth direct bearer. Worry "CLAUDE_CODE_OAUTH_TOKEN is refresh that generates ANTHROPIC_AUTH_TOKEN" **false** — they are distinct bearer types. `oat01` used direct, not via ANTHROPIC_AUTH_TOKEN. `ANTHROPIC_AUTH_TOKEN` is generic gateway bearer (no oauth beta).
- Implications for proxy offload: proxy holds pool of `sk-ant-oat01-...`, client sets `ANTHROPIC_BASE_URL=https://proxy`, proxy must inject `Authorization: Bearer <oat>` + ensure `oauth-2025-04-20` in `anthropic-beta` when forwarding to real api. Must not mix with `x-api-key` path.

**Next:** Need failover + metrics design (5h limit detection, storage). Pending Q5a-c.

## 2026-08-31 — Brainstorm Complete (Architectural)

**Classification:** Architectural -> full 7-section design.
**Approach:** 1 (stateless Go reverse proxy + Redis + file pool) approved.
**Sticky:** optional (lb affinity preferred, redis fallback).
**Language:** Go (extreme minimal latency).
**Pool:** file `pool.yaml` in-mem, fsnotify hot-reload, validation.
**Spec:** `docs/2026-08-31-claude-proxy-subs-design.md` (7 sections, all approved)
**Probe:** validated auth (oat01 direct Bearer + oauth-2025-04-20) via live capture + SDK primary.

**Next:** Need writing-plans to break into tasks after user reviews spec.


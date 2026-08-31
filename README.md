# pro-ant — Claude Proxy Subs

Reverse proxy pooling Anthropic Pro/Max `sk-ant-oat01-...` + heterogeneous members (OpenRouter, DeepSeek) with failover on 5h limit, optional sticky for cache, Prometheus metrics.

**Status:** design draft 2026-08-31 (see `docs/2026-08-31-claude-proxy-subs-design.md`)

**Stack:** Go + Redis + file pool (`pool.yaml`) — minimal deps, HA stateless.

**Validated auth:** live capture `probe/capture.log` (claude-cli/2.1.251) confirms `CLAUDE_CODE_OAUTH_TOKEN` -> `Bearer oat + oauth-2025-04-20`.

## Quickstart (design)

- `ANTHROPIC_BASE_URL=https://proxy/v1` on clients, dummy key, proxy injects pooled oat.
- Pool file hot-reloads, Redis tracks cooldown (5h TTL) + optional sticky.
- Failover body-check on 429 + rate_limit body, Prometheus `/metrics`.

See spec for architecture, components, data flow.

## Workspace
Original Second-Brain workspace: `workspace/claude-proxy-subs` (now graduated to this repo).

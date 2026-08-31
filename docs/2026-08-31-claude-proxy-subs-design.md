# Claude Proxy Subs — Design Spec
**Date:** 2026-08-31
**Status:** Draft (all sections approved via brainstorm)
**Workspace:** `workspace/claude-proxy-subs`
**Stack:** Go (reverse proxy), Redis (cooldown + optional sticky), file pool, Prometheus

## 1. Context & Goals

**Goal:** Offload `CLAUDE_CODE_OAUTH_TOKEN` pool from `claude-agent-sdk` clients. Pool N Anthropic Pro/Max `sk-ant-oat01-...` plus heterogeneous members (OpenRouter, DeepSeek) with per-member model mapping. Provide failover on 5h limit (body-checked), optional sticky for prompt-cache maximization, Prometheus visibility, minimal deps, HA.

**Heterogeneous pool:** each member defines `modelMap` (e.g., `claude-sonnet-4 -> deepseek-chat`). If no map, member assumed Anthropic passthrough.

**Validated auth (live capture 2026-08-31, claude-cli/2.1.251):**
- `CLAUDE_CODE_OAUTH_TOKEN` -> `Authorization: Bearer sk-ant-oat01-...` + `anthropic-beta: ...,oauth-2025-04-20,...` (no x-api-key)
- `ANTHROPIC_API_KEY` -> `x-api-key: ...` only
- `ANTHROPIC_AUTH_TOKEN` -> `Authorization: Bearer ...` (generic gateway bearer, separate from oauth)
- Precedence: `ANTHROPIC_API_KEY` wins over `CLAUDE_CODE_OAUTH_TOKEN` when both set.
- Proxy must inject `Bearer oat + oauth-2025-04-20` for anthropic_oauth members.

Primary source: `@anthropic-ai/sdk` `lib/credentials/types.js:50` `OAUTH_API_BETA_HEADER='oauth-2025-04-20'`, `client.js:434` append logic, plus `probe/capture.log`.

## 2. Non-Goals
- Billing/usage DB, dashboard CRUD (Prometheus only for now)
- OAuth refresh exchange service (oat used direct)
- Forward/regular HTTP proxy mode (reverse only via ANTHROPIC_BASE_URL)

## 3. Architecture

```
[claude-agent-sdk] --ANTHROPIC_BASE_URL=https://proxy/v1--> [LB] -> [Go proxy pods xN] <-> [Redis Sentinel/Cluster]
                                                            |  ^ file watch pool.yaml (in-mem atomic)
                                                            |-> [Upstreams: Anthropic oauth/api, OpenRouter, DeepSeek] (modelMap)
                                                            `-> /metrics :9090, /healthz, /readyz
```

- Stateless Go pods; shared runtime state only in Redis (cooldown always, sticky if mode=redis).
- File pool: yaml/json on disk, validated + loaded into `map[id]Member` in memory, hot-reloaded via fsnotify, atomic swap.
- LB affinity optional: if present, sticky via LB hash on `x-claude-code-session-id`; else redis sticky.

## 4. Components

### 4.1 PoolLoader (internal/pool)
```yaml
# pool.yaml example
members:
  - id: anthropic-a
    type: anthropic_oauth
    cred: { env: CLAUDE_OAT_A } # or file:/run/secrets/oat_a
    baseURL: https://api.anthropic.com
    modelMap: {}
    weight: 1
    enabled: true
  - id: openrouter-1
    type: openrouter
    cred: { env: OPENROUTER_KEY }
    baseURL: https://openrouter.ai/api/v1
    modelMap: { "claude-sonnet-4-20250514": "anthropic/claude-sonnet-4", "claude-haiku-4-5-20251001": "deepseek/deepseek-chat" }
    cooldownSec: 60
    enabled: true
```
Validate: id unique, type enum, cred resolvable, baseURL https, modelMap keys valid. On reload error keep old pool, metric `proxy_config_reload_total{status="error"}`.

### 4.2 Router (internal/router)
- Filter: `enabled && !cooldown` (pipelined `MGET cooldown:{ids}`).
- Sticky: if `sticky.mode=redis` -> `GET sticky:{sessionId}` if healthy return it; else hash. If `lb` -> skip Redis, use `crc32(sessionId) % len(healthy)` weighted.
- SessionId: `r.Header.Get("x-claude-code-session-id")` (confirmed in capture). Fallback hash of `Authorization` prefix + IP if missing.
- No sessionId -> weighted random.

### 4.3 Forwarder (internal/proxy)
- ReverseProxy with custom Director: rewrite `r.URL.Path` to upstream baseURL, map `body.model` via `member.modelMap`, set auth headers per type, copy `x-claude-code-session-id` through.
- Header injection:
  - anthropic_oauth: `Authorization: Bearer <oat>` + ensure `anthropic-beta` contains `oauth-2025-04-20` (append if missing)
  - anthropic_api: `x-api-key: <key>` strip Authorization
  - openrouter/deepseek: `Authorization: Bearer <key>` per vendor
- Streaming: if `stream==true` use `io.Copy` + flush; else buffer JSON for modelMap then forward.

### 4.4 FailoverLoop
- Retry <= len(pool). After each resp, tee first 4KB body: if `status 429|401|529` && body matches `rate_limit|overload|oauth|quota|credit` (ci) OR `retry-after` header -> `SET cooldown:{id} 1 EX <ttl>` (ttl from retry-after else member.cooldownSec else 18000), `proxy_failover_total{from,reason}` inc, pick next healthy, loop.
- Else break.
- All exhausted -> return last error 429 with `Retry-After: minTTL`.

### 4.5 Redis (internal/store)
- go-redis/v9, Cluster/Sentinel URL.
- Keys: `cooldown:{id}` TTL, `sticky:{sessionId}` TTL 3600 (if mode redis).
- Fail open on Redis error: treat all healthy, log, `proxy_redis_errors`.

### 4.6 Metrics/Health
- Prometheus: `proxy_requests_total{member,model,status}`, `proxy_failover_total{from,to,reason}`, `proxy_cooldown_members`, `proxy_tokens_{input,output,cache_read,cache_creation}{member,model}`, `proxy_latency_seconds{member}`, `proxy_sticky_hit_total{mode}`, `proxy_config_reload_total{status}`, `proxy_pool_size`.
- Logs: JSON per req (sessionId, reqModel->mapped, member, status, retries, latency).
- Endpoints: `/metrics`, `/healthz`, `/readyz` (pool loaded + Redis ping optional).

## 5. Data Flow
1. Client POST /v1/messages {model, messages}
2. Extract sessionId, reqModel
3. Sticky lookup (redis GET pipelined with cooldown MGET)
4. Pick healthy member via Router
5. Map model, inject auth, forward
6. Inspect resp -> cooldown + loop if needed else break
7. If redis sticky miss -> SET sticky
8. Parse usage -> metrics, return resp

Config reload: fsnotify Write -> validate -> atomic.Store activePool

## 6. Error & Edge Cases
- Body without failover marker 400/5xx -> no cooldown, no retry, return directly.
- Redis down -> degrade: no cooldown, no sticky, still serve.
- Bad pool file -> keep old.
- Timeout 60s per member -> failover.
- Pool exhausted -> 429 + Retry-After.

## 7. Testing
- Unit: pool validate, router hash, header injection, body matcher.
- Integration: miniredis + httptest upstreams 429/200, sticky, modelMap, metrics.
- Probe replay: capture.log fixtures.
- Load: k6 500-2k rps, p50 <1ms added.
- Chaos: kill upstream, TTL expiry recovery, bad config reload.

## 8. Deployment & HA
- Build: `go build -o proxy`, Docker `golang:1.22 -> distroless`, binary 8MB.
- Config: pool.yaml via ConfigMap/volume, creds via env/file secrets (never plain yaml).
- Run: `proxy --config /etc/proxy/pool.yaml --redis-url redis://... --metrics-addr :9090 --listen :8080 --sticky-mode lb|redis`
- HA: 2-3 pods behind LB (hash on x-claude-code-session-id for lb mode), Redis Sentinel 3 nodes, readiness checks.
- Updates: file watcher live swap, or SIGHUP, or rollout restart.

## 9. Open Questions -> Resolved
- CLAUDE_CODE_OAUTH_TOKEN auth shape -> validated live + SDK primary.
- Reverse vs forward -> reverse via ANTHROPIC_BASE_URL.
- Sticky via Redis -> optional, LB affinity preferred.
- File pool -> yes, in-mem with validation.
- Language -> Go.
- Redis only for runtime -> yes (cooldown always, sticky optional).

## 10. Next Steps
1. Scaffold Go module, implement PoolLoader + Router + Forwarder + Failover + Redis + Metrics.
2. Add pool.yaml example + Dockerfile.
3. Write unit + integration tests (miniredis).
4. Bench latency, tune pipelining.
5. Deploy to VPS/k8s, point claude-agent-sdk clients at it, observe Prometheus failover metrics.

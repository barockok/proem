# proem Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stateless Go reverse proxy pooling Anthropic `oat01` + heterogeneous OpenRouter/DeepSeek members with file pool hot-reload, Redis cooldown (5h) + optional sticky, body-checked failover, Prometheus metrics.

**Architecture:** Stateless Go pods behind LB -> file `pool.yaml` in-mem (fsnotify atomic swap) -> Redis Sentinel for `cooldown:{id}` TTL + optional `sticky:{sid}` -> per-member auth injection + modelMap rewrite -> failover loop (max N, body 429+rate_limit) -> /metrics.

**Tech Stack:** Go 1.22+, chi, go-redis/v9, fsnotify, prometheus/client_golang, yaml.v3, httptest/miniredis for tests, Docker distroless.

**Spec:** `docs/2026-08-31-claude-proxy-subs-design.md`

## Global Constraints

- Language: Go (not Bun/TS) for extreme minimal latency (<1ms added p50)
- Runtime state only in Redis: `cooldown:{memberId}` always, `sticky:{sessionId}` only if `sticky.mode=redis`; LB affinity (`hash x-claude-code-session-id`) preferred
- Pool config: file `pool.yaml` validated + loaded into memory via fsnotify atomic swap; no DB
- Auth injection: `anthropic_oauth` -> `Authorization: Bearer <oat>` + ensure `anthropic-beta` contains `oauth-2025-04-20`; `anthropic_api` -> `x-api-key`; openrouter/deepseek -> `Bearer`; precedence API wins (validated)
- Failover: body-check `429|401|529` + body contains `rate_limit|overload|oauth|quota|credit` OR `retry-after` header -> cooldown TTL from retry-after else 18000s (Anthropic 5h) else member.cooldownSec else 60s; loop max pool size
- Metrics: Prometheus `/metrics` required (requests, failover, tokens from `usage`, latency, cooldown gauge, sticky hit, config reload)
- HA: stateless pods, Redis Sentinel/Cluster, single binary ~8MB
- Minimal deps: only go-redis, chi, fsnotify, prom client, yaml

---

## File Structure

```
proem/
├── go.mod
├── cmd/proem/main.go              # flag parse, pool load, redis, server start
├── internal/
│   ├── pool/loader.go             # File watch, validate, atomic Store, yaml unmarshal
│   ├── pool/types.go              # Member, Pool structs, validation
│   ├── router/router.go           # Pick healthy member: filter cooldown, sticky lookup, consistent hash
│   ├── proxy/forwarder.go         # ReverseProxy Director: modelMap rewrite, auth injection, header copy
│   ├── proxy/handler.go           # HTTP handler wrapping failover loop, sticky pin, metrics
│   ├── failover/detector.go       # Body matcher, status check, retry-after parse
│   ├── store/redis.go             # go-redis wrapper: MGET cooldown, GET/SET sticky, SET cooldown
│   ├── metrics/metrics.go         # Prometheus counters/histograms, /metrics handler
│   └── config/config.go           # Flags/env, sticky.mode, timeouts
├── pool.yaml.example
├── Dockerfile
├── Makefile
├── docs/2026-08-31-claude-proxy-subs-design.md
└── tests/
    ├── pool_test.go
    ├── router_test.go
    ├── forwarder_test.go
    ├── failover_test.go
    └── integration_test.go (miniredis + httptest)
```

---

### Task 1: Scaffold Go module + config + pool types

**Files:**
- Create: `go.mod`
- Create: `internal/pool/types.go`
- Create: `internal/config/config.go`
- Create: `pool.yaml.example`
- Test: `go vet` passes

**Interfaces:**
- Consumes: spec pool yaml shape
- Produces: `pool.Member`, `pool.Pool`, `config.Config` used by loader/router/forwarder

- [ ] Step 1: `go mod init github.com/barockok/proem`, add deps `github.com/go-chi/chi/v5`, `github.com/redis/go-redis/v9`, `github.com/fsnotify/fsnotify`, `github.com/prometheus/client_golang`, `gopkg.in/yaml.v3`
- [ ] Step 2: Write `internal/pool/types.go` structs `Member{ID, Type, Cred, BaseURL, ModelMap, Weight, Enabled, CooldownSec}`, `Pool{Members []Member}`, validation func
- [ ] Step 3: Write `internal/config/config.go` flags: `--config`, `--redis-url`, `--listen`, `--metrics-addr`, `--sticky-mode lb|redis`, timeouts
- [ ] Step 4: Write `pool.yaml.example` with 2 anthropic_oauth + 1 openrouter sample
- [ ] Step 5: `go vet ./...` and commit

---

### Task 2: PoolLoader with file watch + validation

**Files:**
- Create: `internal/pool/loader.go`
- Test: `tests/pool_test.go` (bad yaml, dup id, missing cred, hot-reload)

**Interfaces:**
- Consumes: pool/types.go, config.Config
- Produces: `loader.ActivePool() *Pool` atomic, `loader.Reloaded` channel, `metrics proxy_config_reload_total`

- [ ] Step 1: Write failing test: loader loads valid yaml, rejects dup id, keeps old on bad reload
- [ ] Step 2: Implement `Loader` with `sync/atomic.Pointer[Pool]`, `fsnotify.Watcher`, validate on Write, `Store` on ok else metric error
- [ ] Step 3: Run `go test ./... -run Pool`
- [ ] Step 4: Commit

---

### Task 3: Redis store (cooldown + optional sticky)

**Files:**
- Create: `internal/store/redis.go`
- Test: `tests/store_test.go` with miniredis

**Interfaces:**
- Consumes: config redis URL
- Produces: `store.IsCooldown(id)->bool, SetCooldown(id,ttl), GetSticky(sid)->id, SetSticky(sid,id,ttl)`, pipelined MGET

- [ ] Step 1: Write failing test using `github.com/alicebob/miniredis/v2`: SetCooldown -> IsCooldown true, expiry, GetSticky miss/hit
- [ ] Step 2: Implement `RedisStore` with go-redis, `MGET cooldown:{ids}` pipelined, `SET cooldown:{id} EX ttl NX`
- [ ] Step 3: Handle Redis down fail-open (return not-cooldown), log warn
- [ ] Step 4: Test + commit

---

### Task 4: Router (healthy filter + sticky + hash)

**Files:**
- Create: `internal/router/router.go`
- Test: `tests/router_test.go`

**Interfaces:**
- Consumes: pool.Pool, store.RedisStore, sessionId, reqModel
- Produces: `router.Pick(sessionId, reqModel) (*Member, error)`

- [ ] Step 1: Write failing test: Pick with 2 healthy, one cooldown -> picks healthy; sticky redis hit returns pinned; lb mode hash deterministic
- [ ] Step 2: Implement Router: filter enabled+not cooldown (MGET), if redis mode check sticky, else consistent hash `crc32(sessionId) % len(healthy)` weighted, fallback random
- [ ] Step 3: Test + commit

---

### Task 5: Failover detector (body matcher)

**Files:**
- Create: `internal/failover/detector.go`
- Test: `tests/failover_test.go`

**Interfaces:**
- Consumes: resp status, body bytes, headers
- Produces: `detector.ShouldFailover(status, body, headers) (bool, ttl, reason)`

- [ ] Step 1: Write failing test: 429 body `{"error":{"type":"rate_limit"}}` -> true ttl 18000, 400 body -> false, 529+retry-after 120 -> ttl 120
- [ ] Step 2: Implement matcher: status 429|401|529 && body contains rate_limit|overload|oauth|quota|credit (ci) OR retry-after present; parse retry-after to ttl
- [ ] Step 3: Test + commit

---

### Task 6: Forwarder (modelMap + auth injection + reverse proxy)

**Files:**
- Create: `internal/proxy/forwarder.go`
- Test: `tests/forwarder_test.go` (httptest upstreams)

**Interfaces:**
- Consumes: pool.Member, req body
- Produces: forwarded request with mapped model + correct headers

- [ ] Step 1: Write failing test: for anthropic_oauth member, assert Authorization Bearer oat + oauth beta present; for anthropic_api assert x-api-key; for openrouter assert Bearer + modelMap rewrite `claude-sonnet-4 -> deepseek-chat`
- [ ] Step 2: Implement Forwarder: read body JSON, map `body.model` via member.ModelMap, set headers per type, ensure anthropic-beta contains oauth-2025-04-20, copy x-claude-code-session-id through, use ReverseProxy with custom Director and streaming Flush
- [ ] Step 3: Test via httptest servers
- [ ] Step 4: Commit

---

### Task 7: Handler (failover loop + sticky pin + metrics)

**Files:**
- Create: `internal/proxy/handler.go`
- Test: `tests/integration_test.go`

**Interfaces:**
- Consumes: router, forwarder, detector, store, metrics
- Produces: `http.Handler` for `/v1/messages` + proxy for other paths

- [ ] Step 1: Write failing integration test with miniredis + 2 fake upstreams: first returns 429 rate_limit, second 200 -> handler retries, sets cooldown, pins sticky, returns 200, metrics inc
- [ ] Step 2: Implement Handler: loop max pool size, call forwarder, peek resp body (tee), detector.ShouldFailover -> store.SetCooldown + metrics + continue; else break; handle sticky SET after success, parse usage tokens -> metrics
- [ ] Step 3: Handle pool exhausted -> return 429 with Retry-After minTTL
- [ ] Step 4: Test + commit

---

### Task 8: Metrics + health endpoints

**Files:**
- Create: `internal/metrics/metrics.go`
- Modify: `cmd/proem/main.go` to expose /metrics, /healthz, /readyz

**Interfaces:**
- Produces: Prometheus handlers

- [ ] Step 1: Define metrics: proxy_requests_total, proxy_failover_total, proxy_cooldown_members, proxy_tokens_*, proxy_latency_seconds, proxy_sticky_hit_total, proxy_config_reload_total, proxy_pool_size
- [ ] Step 2: Expose `/metrics` via promhttp, `/healthz` always 200, `/readyz` checks pool loaded + Redis ping (degraded ok)
- [ ] Step 3: Wire handler to increment metrics
- [ ] Step 4: Test /metrics scrape
- [ ] Step 5: Commit

---

### Task 9: Main binary + wiring + Dockerfile

**Files:**
- Create: `cmd/proem/main.go`
- Create: `Dockerfile`, `Makefile`

- [ ] Step 1: Implement main: flag parse, init loader (load pool.yaml), init redis store, init metrics, init router, create handler, start `http.Server` on :8080 and metrics on :9090, handle SIGTERM/SIGHUP
- [ ] Step 2: Write Dockerfile multi-stage golang:1.22-alpine -> scratch
- [ ] Step 3: Makefile targets: build, test, docker, run
- [ ] Step 4: Manual e2e: run proxy + redis, set ANTHROPIC_BASE_URL to proxy, run probe/run.mjs against real pool (single anthropic member) -> verify header injection via probe capture
- [ ] Step 5: Commit

---

### Task 10: Load & chaos testing + docs

**Files:**
- Create: `tests/load_test.go` (k6 script + go bench)
- Modify: `README.md` with run instructions

- [ ] Step 1: k6 script 500-2k rps, assert p50 added latency <1ms, Redis pipelining
- [ ] Step 2: Chaos: kill one upstream, verify failover + TTL expiry recovery
- [ ] Step 3: Update README with quickstart, pool.yaml docs, metrics dashboard, deploy steps
- [ ] Step 4: Tag v0.1.0, push


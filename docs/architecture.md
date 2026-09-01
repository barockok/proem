# Architecture — pro-ant

```
client (SDK) --issued token--> pro-ant pods (stateless, LB) --pool credential--> upstreams
                                    |     |  |
                     clients.yaml (auth)  |  pool.yaml (fsnotify atomic.Pointer[Pool])
                                          |
                                   Redis (cooldown:{id} SET EX ttl, sticky:{sid})
                                          |
                                   Prometheus /metrics (labelled by client)
```

## Components

- **client/** — `clients.yaml` registry: SHA-256 digests of issued tokens, `atomic.Pointer[Registry]` + fsnotify hot reload, `Lookup(token)` by digest. `IssueToken`/`IssueAndDescribe` mint tokens for the `issue-token` subcommand.
- **proxy/auth.go** — middleware in front of the failover handler. Resolves the caller's token to a client, rejects unknown/disabled callers with an Anthropic-shaped error, strips the caller's own auth headers so they cannot override the injected pool credential, and puts the client name on the request context.
- **pool/loader.go** — `fsnotify` watches `pool.yaml`, `yaml.v3` + `Validate()`, `atomic.Pointer[Pool]` swap, defaults `Weight 1`, `CooldownSec` 18000/60.
- **store/redis.go** — `go-redis/v9`, `MGet cooldown:{ids}` pipelined `FilterHealthy`, `Exists` `IsCooldown`, `Get/Set sticky:{sid}`. Fail-open on Redis error.
- **failover/detector.go** — `ShouldFailover(status, body, headers)`. Triggers on `Retry-After` header OR `status 429|401|529` + body contains `rate_limit|overload|oauth|quota|credit`. `CooldownTTL` prefers `Retry-After` value.
- **router/router.go** — filters `IsEnabled` + not in cooldown, then sticky hit (redis mode only), else `crc32(sessionID) % weighted` hash (lb) or `rand.Intn` weighted. Deterministic per session.
- **proxy/forwarder.go** — `AuthHeaders` per `MemberType` (`Bearer oat + anthropic-beta: oauth-2025-04-20` for oauth, `x-api-key` for api, `Bearer` for others), `RewriteBody` modelMap naive JSON replace, `NewProxy` reverseproxy director (unused path, handler uses direct `http.Client` forward for streaming-friendly).
- **proxy/handler.go** — `ServeHTTP` failover loop max `len(members)`: `CloneBody` for replay, `router.Pick` with `FilterHealthy`, `forward` via `http.Client` (header+body copy, auth inject, modelMap), `ShouldFailover` -> `SetCooldown` + `Failovers` counter, success -> `SetSticky` (redis mode) + `Tokens` from `usage` + `Latency`.
- **metrics/metrics.go** — `requests_total{member,code}`, `failovers_total{from_member,reason}`, `tokens_total{member,type}`, `upstream_latency_seconds{member}`, `member_cooldown{member}`, `sticky_hits_total{result}`, `config_reloads_total{result}`.
- **app/app.go** — wiring: loads both registries, starts both watchers, mounts `/health` unauthenticated and everything else behind `proxy.Auth`, runs and shuts down the proxy and metrics servers.
- **config/config.go** — flags `--config`, `--clients` (required), `--redis-url`, `--listen`, `--metrics-addr`, `--sticky-mode lb|redis|none`, timeouts.

## HA

Pods stateless, share Redis (Sentinel/Cluster) for cooldown/sticky. No DB. Binary ~8MB. Single `go vet` + `go test -race`.

## Latency

Go adds 0.2-0.5ms p50. No body parse except failover check + token extraction. Streaming proxied via body read/write (non-streaming buffering acceptable for SDK's non-stream path; streaming needs chunked forward in future).

---

[← Back to README](../README.md) · [Client tokens](client-tokens.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

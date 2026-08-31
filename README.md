# pro-ant

Stateless Go reverse proxy that pools Anthropic Pro/Max OAuth tokens (`sk-ant-oat01-…`) alongside heterogeneous upstreams (Anthropic API, OpenRouter, DeepSeek). When a member hits its 5h limit the proxy fails over to the next healthy one and puts the exhausted member on a Redis cooldown. Token usage, failovers and latency are exported to Prometheus.

Point your client at the proxy and nothing else changes:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
```

## Documentation

| Guide | What it covers |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, configure `pool.yaml`, run locally or via Docker, first request |
| [Architecture](docs/architecture.md) | Components, request path, HA model, latency budget |
| [How it works](docs/how-it-works.md) | Auth variants, failover and cooldown rules, model mapping, metrics |
| [Adding a pool member](docs/adding-pool-member.md) | Add, weight, disable or remove an upstream — with hot reload |
| [Design spec](docs/2026-08-31-claude-proxy-subs-design.md) | Original design document |

## Quick start

```bash
cp pool.yaml.example pool.yaml         # then edit members
export CLAUDE_OAT_A=sk-ant-oat01-...   # secrets come from env or files
docker run -d -p 6379:6379 redis:7-alpine

go run ./cmd/proxy --config ./pool.yaml --redis-url redis://localhost:6379/0
```

Health check on `:8080/health`, Prometheus metrics on `:9090/metrics`. Full walkthrough in [getting started](docs/getting-started.md).

## Features

- **Failover on rate limits** — body-checked `429/401/529` plus `Retry-After`, with per-member cooldown in Redis (5h default for Anthropic).
- **Heterogeneous pool** — mix OAuth, API-key and third-party members; `modelMap` rewrites model names per upstream.
- **Hot reload** — `pool.yaml` is watched and swapped atomically; invalid edits keep the previous pool.
- **Optional stickiness** — `lb` (hash on session id, no Redis), `redis` (pinned sessions), or `none`.
- **Fail-open** — if Redis is unavailable the proxy keeps serving with cooldown and stickiness disabled.
- **Observability** — requests, failovers, tokens, latency, cooldown state and config reloads as Prometheus series.

## Development

```bash
make test      # go test ./... -race with coverage profile
make vet       # go vet ./...
make build     # binary into bin/pro-ant
./scripts/coverage.sh   # tests + enforce the internal coverage minimum
```

CI runs gofmt, vet, the race-enabled coverage gate, a binary smoke test against a live Redis service, and a Docker build. Tagging `v*` publishes binaries and a `ghcr.io` image.

**Auth grounding:** the OAuth header shape (`Authorization: Bearer <oat>` plus `anthropic-beta: oauth-2025-04-20`) is confirmed by a live capture in `probe/capture.log` (claude-cli/2.1.251), not inferred.

## Workspace

Started as `workspace/claude-proxy-subs` in Second-Brain, now graduated to this repo.

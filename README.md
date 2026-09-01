# Proem

> **proem** *(n.)* — from Latin *prooemium*, an introductory discourse; a preamble.
>
> Every request gets a preface before the main text: the proxy authenticates the caller, chooses which credential speaks for them, and hands the conversation to the upstream. The agent writes the book; Proem writes the front matter.

Stateless Go reverse proxy that pools Anthropic Pro/Max OAuth tokens (`sk-ant-oat01-…`) alongside heterogeneous upstreams (Anthropic API, OpenRouter, DeepSeek). When a member hits its 5h limit the proxy fails over to the next healthy one and puts the exhausted member on a Redis cooldown. Token usage, failovers and latency are exported to Prometheus.

Each agent gets its own proxy-issued token, so usage is attributable per agent and no caller ever holds a real Anthropic credential:

```bash
export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...   # issued by the proxy
export ANTHROPIC_BASE_URL=http://localhost:8080
```

## Documentation

| Guide | What it covers |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, configure `pool.yaml`, run locally or via Docker, first request |
| [Architecture](docs/architecture.md) | Components, request path, HA model, latency budget |
| [How it works](docs/how-it-works.md) | Auth variants, failover and cooldown rules, model mapping, metrics |
| [Adding a pool member](docs/adding-pool-member.md) | Add, weight, disable or remove an upstream — with hot reload |
| [Client tokens](docs/client-tokens.md) | Issue tokens to agents, revoke them, attribute usage per agent |
| [Design spec](docs/2026-08-31-claude-proxy-subs-design.md) | Original design document |

## Quick start

```bash
cp pool.yaml.example pool.yaml         # then edit members
export CLAUDE_OAT_A=sk-ant-oat01-...   # secrets come from env or files
docker run -d -p 6379:6379 redis:7-alpine

# issue a token per agent; paste the printed entry into clients.yaml
go run ./cmd/proem issue-token agent-maria > /dev/null && \
  go run ./cmd/proem issue-token agent-maria

go run ./cmd/proem --config ./pool.yaml --clients ./clients.yaml \
  --redis-url redis://localhost:6379/0
```

Health check on `:8080/health`, Prometheus metrics on `:9090/metrics`. Full walkthrough in [getting started](docs/getting-started.md).

## Features

- **Per-agent tokens** — each caller gets an issued token; the proxy authenticates it, stores only its hash, and swaps it for a pooled credential upstream.
- **Usage attribution** — `proem_tokens_total{client="agent-maria"}` shows exactly what each agent consumed.
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
make build     # binary into bin/proem
./scripts/coverage.sh   # tests + enforce the internal coverage minimum
```

CI runs gofmt, vet, the race-enabled coverage gate, a binary smoke test against a live Redis service, and a Docker build. Tagging `v*` publishes binaries and a `ghcr.io` image.

**Auth grounding:** the OAuth header shape (`Authorization: Bearer <oat>` plus `anthropic-beta: oauth-2025-04-20`) is confirmed by a live capture in `probe/capture.log` (claude-cli/2.1.251), not inferred.

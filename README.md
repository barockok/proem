# Proem

> **proem** *(n.)* — from Latin *prooemium*, an introductory discourse; a preamble.
>
> Every request gets a preface before the main text: the proxy authenticates the caller, chooses which credential speaks for them, and hands the conversation to the upstream.

**A reverse proxy for Anthropic-compatible APIs.** Point your clients at Proem instead of an upstream provider, and it authenticates the caller, routes to a healthy member of a configured pool, injects that member's credential, and records what the request consumed — attributed to the caller that made it.

Anything speaking the Anthropic Messages API works unchanged — the Anthropic SDKs, the Claude Agent SDK, Claude Code, or your own HTTP client. It is a base-URL change:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-ant-oat01-...   # issued by Proem, valid only here
```

📖 **[Documentation](docs/site/_content/index.md)** — also published at [barockok.github.io/proem](https://barockok.github.io/proem/)

## Why

- **One endpoint over several providers.** Mix the Anthropic API with any Anthropic-compatible gateway — OpenRouter, DeepSeek, something self-hosted. Per-member model mapping lets a client keep asking for one model name.
- **Credentials stay put.** Provider keys live in Proem's config, read from env vars or files. Callers hold their own issued tokens, revocable individually.
- **Usage you can attribute.** `sum by (client) (increase(proem_tokens_total[24h]))` answers who consumed what — including cache reads and writes, which dominate cached workloads.
- **Failover without breaking streaming.** A rate-limited member is cooled and the next one tried. Responses that cannot fail over stream straight through, unbuffered.

Proem does not change the terms you have with any provider. It routes requests to endpoints you name, using credentials you supply.

## Quick start

```bash
make install
cp pool.yaml.example pool.yaml       # then edit members
proem issue-token agent-maria        # paste the printed entry into clients.yaml
proem --config ./pool.yaml --clients ./clients.yaml \
      --redis-url redis://localhost:6379/0
```

Health on `:8080/health`, metrics on `:9090/metrics`. Full walkthrough in the [quickstart](docs/site/_content/start/quickstart.md).

## Documentation

| | |
| --- | --- |
| [Quickstart](docs/site/_content/start/quickstart.md) | Running in about five minutes |
| [How it works](docs/site/_content/start/how-it-works.md) | The request path, end to end |
| [Pool members](docs/site/_content/guides/pool-members.md) | Adding, weighting and mapping upstreams |
| [Client tokens](docs/site/_content/guides/client-tokens.md) | Issuing, revoking, attributing |
| [Observability](docs/site/_content/guides/observability.md) | Access log, auth alerts, client IP |
| [Architecture](docs/site/_content/reference/architecture.md) | Components and state |
| [Configuration](docs/site/_content/reference/configuration.md) | Every flag |
| [Metrics](docs/site/_content/reference/metrics.md) | Every series |
| [Install and upgrade](docs/site/_content/deploy/install.md) | Building, installing, upgrading safely |
| [Docker](docs/site/_content/deploy/docker.md) | Running from the published image |

These pages are the source of the [published site](https://barockok.github.io/proem/); they render on GitHub too, so the links above work without leaving the repo.

## Development

```bash
make test      # go test ./... -race, plus the install-script test
make vet
make build
make install   # atomic-rename install, safe while the proxy is running
./scripts/coverage.sh    # enforce the internal coverage minimum
npm ci && node docs/site/build.mjs   # build the docs site locally
```

CI runs gofmt, vet, a race-enabled coverage gate (95% minimum over `internal/`), a binary smoke test against a live Redis, and a Docker build. Tagging `v*` publishes signed binaries and a `ghcr.io` image, then republishes the docs site.

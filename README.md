# Proem

> **proem** *(n.)* — from Latin *prooemium*, an introductory discourse; a preamble.
>
> Each request gets a preface before the main text. The proxy authenticates the caller, selects the credential that speaks for it, and hands the conversation to the member.

**A reverse proxy for Anthropic-compatible APIs.** Your clients send requests to Proem instead of to a provider. Proem authenticates the client, selects a healthy member from a configured pool, adds that member's credential, and records what the request used.

Any software that calls the Anthropic Messages API works without change. This includes the Anthropic SDKs, the Claude Agent SDK, Claude Code, and your own HTTP client. You change one setting:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-ant-oat01-...   # issued by Proem, valid only here
```

📖 **[Documentation](docs/site/_content/index.md)**. It is also published at [barockok.github.io/proem](https://barockok.github.io/proem/).

## What Proem does

- **It puts one endpoint in front of several members.** A pool can contain the Anthropic API and any Anthropic-compatible gateway, such as OpenRouter, DeepSeek, or a gateway you host. Each member can map model names, so a client asks for one name whatever member answers.
- **It keeps credentials in one place.** Member credentials stay in Proem's configuration, read from environment variables or files. Each client holds its own token, and you can revoke one client without changing the others.
- **It attributes usage.** The query `sum by (client) (increase(proem_tokens_total[24h]))` reports what each client used. The count includes cache reads and cache writes, which are the largest counts on a cached workload.
- **It fails over without stopping a stream.** Proem puts a rate-limited member in cooldown and tries the next one. It sends a response that cannot fail over straight to the client, without buffering.

Proem does not change the terms of your agreement with a provider. It sends requests to the endpoints you configure, with the credentials you supply.

## Quick start

```bash
make install
cp pool.yaml.example pool.yaml       # then edit the members
proem issue-token agent-maria        # add the printed entry to clients.yaml
proem --config ./pool.yaml --clients ./clients.yaml \
      --redis-url redis://localhost:6379/0
```

Proem serves the proxy on port 8080 and metrics on port 9090. The [quickstart](docs/site/_content/start/quickstart.md) explains each step.

## Documentation

| Page | Contents |
| --- | --- |
| [Quickstart](docs/site/_content/start/quickstart.md) | How to run Proem |
| [How it works](docs/site/_content/start/how-it-works.md) | The path of a request |
| [Pool members](docs/site/_content/guides/pool-members.md) | How to add, weight and map a member |
| [Client tokens](docs/site/_content/guides/client-tokens.md) | How to issue, revoke and attribute |
| [Observability](docs/site/_content/guides/observability.md) | Access log, alerts, client IP |
| [Architecture](docs/site/_content/reference/architecture.md) | The parts and the state |
| [Configuration](docs/site/_content/reference/configuration.md) | Every option |
| [Metrics](docs/site/_content/reference/metrics.md) | Every series |
| [Install and upgrade](docs/site/_content/deploy/install.md) | How to build, install and upgrade |
| [Docker](docs/site/_content/deploy/docker.md) | How to run the published image |

These pages are the source of the [published site](https://barockok.github.io/proem/). They also render on GitHub, so the links above work inside the repository.

## Development

```bash
make test      # go test ./... -race, and the install script test
make vet
make build
make install   # installs by atomic rename, which is safe while Proem runs
./scripts/coverage.sh    # enforce the coverage minimum
npm ci && node docs/site/build.mjs   # build the documentation site
```

CI runs gofmt, vet, a race-enabled coverage gate with a 95% minimum over `internal/`, a smoke test of the binary against a live Redis, and a Docker build. A `v*` tag publishes signed binaries and a `ghcr.io` image, and then publishes the documentation site.

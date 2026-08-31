# Getting Started — pro-ant

Stateless Go reverse proxy pooling Anthropic OAuth + API + heterogeneous OpenRouter/DeepSeek upstreams. Failover on 5h rate limits via body-checked 429/401/529 + cooldown TTL. Redis for cooldown + optional sticky.

## Quick start

```bash
# 1. clone
git clone git@github.com:barockok/pro-ant.git && cd pro-ant

# 2. pool config
cp pool.yaml.example pool.yaml
# edit pool.yaml — set cred.env for each member to env var name that holds token
export CLAUDE_OAT_A=sk-ant-oat01-...
export CLAUDE_OAT_B=sk-ant-oat01-...
export OPENROUTER_KEY=sk-or-...

# 3. redis
docker run -d -p 6379:6379 redis:7-alpine

# 4. run
go run ./cmd/proxy --config ./pool.yaml --redis-url redis://localhost:6379/0 --listen :8080 --metrics-addr :9090 --sticky-mode lb

# 5. use as Anthropic base URL (claude-agent-sdk / SDK)
export ANTHROPIC_BASE_URL=http://localhost:8080
# then run your SDK client — requests fan out through pool with failover
```

Docker:

```bash
docker build -t pro-ant:local .
docker run -p 8080:8080 -p 9090:9090 -v $PWD/pool.yaml:/pool.yaml --env-file .env pro-ant:local --config /pool.yaml --redis-url redis://host.docker.internal:6379/0
```

Health: `curl localhost:8080/health` → `ok`. Metrics: `curl localhost:9090/metrics`.

## Pool file

See `pool.yaml.example`. Each member:

```yaml
members:
  - id: a         # unique
    type: anthropic_oauth  # anthropic_oauth|anthropic_api|openrouter|deepseek|generic
    cred: {env: CLAUDE_OAT_A}  # env or {file: /run/secrets/oat}
    baseURL: https://api.anthropic.com
    weight: 1
    cooldownSec: 18000  # default 18000 anthropic, 60 others
    modelMap: {"claude-sonnet-4": "anthropic/claude-sonnet-4"} # only for openrouter/deepseek
```

Hot reload: edit `pool.yaml` — proxy fsnotify reloads atomically, bad yaml keeps old pool.

## Sticky

- `lb` (default): `crc32(sessionID) % weighted` — no Redis, LB affinity preferred.
- `redis`: `GET/SET sticky:{sid}` TTL 1h — pins session to member.
- `none`: random weighted.
Set via `--sticky-mode` and client header `x-claude-code-session-id` (SDK sends this).

---

[← Back to README](../README.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

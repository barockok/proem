---
title: Configuration
description: Command-line flags, defaults, and the files Proem reads.
---

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `./pool.yaml` | Pool file. YAML or JSON |
| `--clients` | `./clients.yaml` | Client registry. Required; Proem is fail-closed |
| `--redis-url` | `redis://localhost:6379/0` | Cooldown and sticky state. Fails open if unreachable |
| `--listen` | `:8080` | Proxy address |
| `--metrics-addr` | `:9090` | Prometheus address |
| `--sticky-mode` | `lb` | `lb`, `redis` or `none` |
| `--trusted-proxies` | *(empty)* | CIDRs or IPs whose `X-Forwarded-For` is believed |
| `--access-log` | `true` | One log line per request |
| `--log-format` | `text` | `text` or `json` |
| `--read-timeout` | `10s` | Request read timeout |
| `--write-timeout` | `60s` | Response write timeout |
| `--upstream-timeout` | `60s` | Time an upstream may take to **respond** |

`--upstream-timeout` bounds the wait for response headers, not the duration of
a stream. A long generation is never truncated by it.

## Subcommands

```bash
proem issue-token <name>   # mint a client token and print its registry entry
proem version              # print the build version
```

## Endpoints

| Path | Auth | Purpose |
|---|---|---|
| `/health` | none | Liveness probe. Returns `ok` |
| `/api/hello` | none | Client reachability probe |
| `/*` | required | Proxied to a pool member |
| `/metrics` (metrics port) | none | Prometheus exposition |

`/health` and `/api/hello` are answered locally so probes do not consume
upstream quota or pollute the auth-failure metric. Bind the metrics address to
a private interface if the proxy is exposed.

## Files

Both files are validated on load and on every change, and may be written as
YAML or JSON.

- **Pool** — see [pool members](../guides/pool-members.md)
- **Clients** — see [client tokens](../guides/client-tokens.md)

Credentials are never written in either file; they are referenced by `env:` or
`file:` and read from the process environment or disk.

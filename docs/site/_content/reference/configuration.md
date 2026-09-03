---
title: Configuration
description: The command-line options, their defaults, and the files that Proem reads.
---

## Options

| Option | Default | Meaning |
|---|---|---|
| `--config` | `./pool.yaml` | The pool file. YAML or JSON. |
| `--clients` | `./clients.yaml` | The client registry. It is required, because Proem is fail-closed. |
| `--redis-url` | `redis://localhost:6379/0` | Cooldown and affinity state. Proem fails open if Redis is not available. |
| `--listen` | `:8080` | The address of the proxy. |
| `--metrics-addr` | `:9090` | The address of the metrics endpoint. |
| `--sticky-mode` | `lb` | `lb`, `redis` or `none`. |
| `--trusted-proxies` | empty | The CIDR ranges or addresses whose `X-Forwarded-For` header Proem reads. |
| `--access-log` | `true` | Write one log line for each request. |
| `--log-format` | `text` | `text` or `json`. |
| `--read-timeout` | `10s` | How long Proem waits to read a request. |
| `--write-timeout` | `60s` | How long Proem waits to write a response. |
| `--upstream-timeout` | `60s` | How long a member has to start a response. |

`--upstream-timeout` limits the wait for the response headers. It does not
limit the length of a stream, so it never cuts a long generation.

## Commands

```bash
proem issue-token <name>   # create a client token and print its registry entry
proem version              # print the version of the build
```

## Endpoints

| Path | Authentication | Purpose |
|---|---|---|
| `/health` | none | Liveness probe. It returns `ok`. |
| `/api/hello` | none | Reachability probe for clients. |
| `/*` | required | Proem forwards the request to a member. |
| `/metrics`, on the metrics port | none | Prometheus metrics. |

Proem answers `/health` and `/api/hello` itself. These probes therefore do not
use member quota and do not increase the authentication failure counter.

Bind the metrics address to a private interface if the proxy is reachable from
a network.

## Files

Proem validates both files when it loads them and after every change. You can
write either file as YAML or as JSON.

- [Pool members](../guides/pool-members.md) describes the pool file.
- [Client tokens](../guides/client-tokens.md) describes the client registry.

Neither file contains a credential. Each member points to an environment
variable or to a file, and Proem reads the credential from there.

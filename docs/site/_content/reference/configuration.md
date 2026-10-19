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

| `--tls-cert`, `--tls-key` | empty | Serve the proxy listener with TLS. Set both or neither. |

### SSO gateway

The gateway is off unless `--public-url` is set. [SSO login](../guides/sso-login.md)
explains the setup.

| Option | Environment | Default | Meaning |
|---|---|---|---|
| `--public-url` | `PROEM_PUBLIC_URL` | empty | The URL that Claude Code and browsers use. It must be `https://` unless the host is loopback. |
| `--oidc-issuer` | `OIDC_ISSUER` | empty | The OpenID Connect issuer. |
| | `OIDC_CLIENT_ID` | empty | The client ID at the IdP. |
| | `OIDC_CLIENT_SECRET` | empty | The client secret at the IdP. |
| `--oidc-scopes` | `OIDC_SCOPES` | `openid,email,profile` | Add `offline_access` for Keycloak. |
| `--oidc-auth-params` | `OIDC_AUTH_PARAMS` | empty | Extra authorize parameters, `k=v,k=v`. |
| `--oidc-allowed-domain` | `OIDC_ALLOWED_DOMAIN` | empty | Accept only emails in this domain. |
| | `PROEM_JWT_SECRET` | empty | Comma list, each 32 bytes or more. The first signs. |
| | `PROEM_REFRESH_KEY` | empty | Comma list, each 32 bytes, base64. The first encrypts. |
| `--session-ttl` | `PROEM_SESSION_TTL` | `1h` | The lifetime of a session token. |
| `--session-max` | `PROEM_SESSION_MAX` | `720h` | The maximum time from login to the last refresh. |
| `--device-rate-limit` | | `10/min` | The limit for each IP on `/device`, as `N/s`, `N/min` or `N/h`. |
| `--device-grant-ttl` | | `10m` | How long a login can wait for the browser. |

Proem does not start if the gateway is on and a value is missing, or if Redis
does not answer.

`--upstream-timeout` limits the wait for the response headers. It does not
limit the length of a stream, so it never cuts a long generation.

## Commands

```bash
proem issue-token <name>   # create a client token and print its registry entry
proem version              # print the version of the build
proem healthcheck <url>    # exit 0 if <url> answers 200, for container healthchecks
```

## Endpoints

| Path | Authentication | Purpose |
|---|---|---|
| `/health` | none | Liveness probe. It returns `ok`. |
| `/api/hello` | none | Reachability probe for clients. |
| `/*` | required | Proem forwards the request to a member. |
| `/.well-known/oauth-authorization-server` | none | Gateway only. Login metadata for Claude Code. |
| `/oauth/device_authorization`, `/oauth/token` | none | Gateway only. The device login and refresh. |
| `/device`, `/oauth/callback` | none | Gateway only. The browser half of the login. |
| `/managed/settings` | none | Gateway only. Always 404: Proem serves no policy. |
| `/v1/metrics`, `/v1/logs`, `/v1/traces` | session token | Gateway only. Accepts Claude Code telemetry and discards it. |
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

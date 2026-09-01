# Observability

Proem emits a structured access log, warns on every rejected request, and exports Prometheus metrics labelled by calling agent.

## What is never logged

Bodies are never recorded, in either direction. Requests carry prompts and responses carry completions, and both may contain whatever the caller put in them. The same applies to credentials:

| Never logged | Why |
| --- | --- |
| Request body | prompts, user data |
| Response body | completions |
| URL query string | callers can put anything there; only the path is logged |
| Client tokens | a rejected token is logged as a 12-character fingerprint instead |
| Pool credentials | never touched by logging at all |

A rejected token appears as `token_fp`, the first 12 hex characters of its SHA-256 digest. That is enough to tell "the same wrong token 500 times" from "500 different wrong tokens" without recording the credential.

## Access log

One line per request, after it completes. `/health` is skipped so probe traffic does not drown the log.

```json
{"time":"2026-09-01T08:27:08Z","level":"INFO","msg":"request","method":"POST",
 "path":"/v1/messages","status":502,"duration_ms":1,"bytes":21,
 "client":"agent-maria","ip":"203.0.113.77","user_agent":"claude-cli/2.1.251"}
```

| Field | Notes |
| --- | --- |
| `client` | authenticated client name, or `unknown` for a rejected request |
| `ip` | caller address, resolved per the trusted-proxy policy below |
| `status` | response status actually written |
| `bytes` | response body size |
| `duration_ms` | time to serve, including upstream and any failover attempts |

Flags: `--access-log=false` turns it off; `--log-format json` (default `text`) selects the encoding.

## Auth failures

Every rejection is counted and logged at `WARN`:

```json
{"level":"WARN","msg":"auth failed","reason":"unknown_token","ip":"203.0.113.5",
 "method":"POST","path":"/v1/messages","token_fp":"95ed1bfa42ef"}
```

```promql
# someone is probing for a valid token
sum by (reason) (rate(proem_auth_failures_total[5m]))
```

| `reason` | Meaning |
| --- | --- |
| `missing_credentials` | no `Authorization` or `x-api-key` header |
| `unknown_token` | token not in the registry (includes `token_fp`) |
| `client_disabled` | known client with `enabled: false` (includes `client`) |

Worth alerting on: a sustained non-zero `unknown_token` rate means someone is guessing, and a spike in `missing_credentials` usually means an agent was deployed without its token.

## Client IP and trusted proxies

`X-Forwarded-For` is caller-supplied and trivially forged, so Proem ignores it unless the immediate peer is a proxy you have named:

```bash
proem --trusted-proxies '10.0.0.0/8,192.168.1.1'
```

- **No `--trusted-proxies` (default):** the header is ignored entirely and the peer address is logged. Correct when clients reach Proem directly.
- **With entries:** if the peer is trusted, the header is walked right to left and the first address that is not itself one of your proxies is used. A caller prepending a fake hop cannot win, because the walk starts from the end your proxy appended.
- If the peer is not in the list, its own address is used and the header is discarded.

Accepts CIDR blocks or bare IPs, IPv4 and IPv6. Set this to your load balancer or ingress range; leaving it empty behind a proxy means every request logs the balancer's address, and setting it too broadly lets clients forge their own.

## Metrics

| Metric | Labels |
| --- | --- |
| `proem_requests_total` | `client`, `member`, `code` |
| `proem_tokens_total` | `client`, `member`, `type` |
| `proem_upstream_latency_seconds` | `client`, `member` |
| `proem_failovers_total` | `client`, `from_member`, `reason` |
| `proem_auth_failures_total` | `reason` |
| `proem_member_cooldown` | `member` |
| `proem_sticky_hits_total` | `result` |
| `proem_config_reloads_total` | `result` |

See [client tokens](client-tokens.md) for per-agent attribution queries.

---

[← Back to README](../README.md) · [Client tokens](client-tokens.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

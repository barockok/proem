# Client tokens

Every caller of the proxy is issued its own token. The proxy uses it for two things: deciding whether the request is allowed at all, and labelling the usage metrics so you can see how much each agent consumed.

Callers never hold a real Anthropic credential. They hold a proxy-issued token; the proxy swaps it for the pooled credential of whichever member it routes to.

```
agent-maria ──token A──┐
                       ├──> proem ──(pool credential)──> Anthropic / OpenRouter / …
agent-sora  ──token B──┘
```

## Issue a token

```bash
proem issue-token agent-maria
```

```
Token for agent-maria (shown once, store it now):

  sk-ant-oat01-y5PRGD7JmUVBqYJ1W_a-2HKRTeuFGkVGXjtTur5pXLc

Add to clients.yaml:

  - name: agent-maria
    tokenSHA256: 6bc4a596d198c83d80bd44a4e46e3f4007e12a35a086a8452abf9f79b0ec1f66

The client uses it as:

  export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-y5PRGD7JmUVBqYJ1W_a-2HKRTeuFGkVGXjtTur5pXLc
  export ANTHROPIC_BASE_URL=http://<proxy-host>:8080
```

The raw token is printed once and never stored. Only its SHA-256 digest goes in `clients.yaml`, so the file is not itself a secret and a leaked copy cannot be used to call the proxy. If a token is lost, issue a new one and replace the entry.

Issued tokens keep the `sk-ant-oat01-` prefix on purpose: the agent SDK only sends a credential as `Authorization: Bearer` (with the OAuth beta header) when it looks like a Claude Code OAuth token. A token without that prefix would be sent as `x-api-key` instead.

## clients.yaml

```yaml
clients:
  - name: agent-maria
    tokenSHA256: 6bc4a596d198c83d80bd44a4e46e3f4007e12a35a086a8452abf9f79b0ec1f66

  - name: agent-sora
    tokenSHA256: 2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae

  - name: agent-retired
    tokenSHA256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
    enabled: false
```

| Field | Meaning |
| --- | --- |
| `name` | Attribution label. Alphanumeric plus `.`, `_`, `-`, max 64 chars — it becomes a Prometheus label value. |
| `tokenSHA256` | Hex SHA-256 digest of the issued token. Must be unique. |
| `enabled` | Optional, defaults to true. `false` revokes access while keeping the record. |

The file is validated on load and on every change: duplicate names, duplicate tokens, malformed digests or an empty list are all rejected. Like `pool.yaml`, it is hot-reloaded — save the file and the new registry applies to the next request. A rejected edit is logged and the previous registry stays in force, so a typo cannot lock out every agent.

Like the pool file, the registry may be written as YAML or JSON — YAML 1.2 is a
superset of JSON, so both are parsed identically, with the same validation and
hot reload:

```json
{"clients":[{"name":"agent-maria","tokenSHA256":"6bc4a596d198c83d80bd44a4e46e3f4007e12a35a086a8452abf9f79b0ec1f66"}]}
```

Pass it with `--clients`:

```bash
proem --config pool.yaml --clients clients.yaml --redis-url redis://localhost:6379/0
```

**The proxy is fail-closed.** It will not start without a client registry, and any request without a recognised token is rejected. There is no anonymous mode.

## What clients see

| Situation | Status | Error type |
| --- | --- | --- |
| No `Authorization` or `x-api-key` header | 401 | `authentication_error` |
| Token not in the registry | 401 | `authentication_error` |
| Client present but `enabled: false` | 403 | `permission_error` |

Errors use the Anthropic envelope, so the SDK surfaces them as authentication failures rather than opaque proxy errors:

```json
{
  "type": "error",
  "error": {
    "type": "authentication_error",
    "message": "invalid credentials: this token is not registered with the proxy"
  }
}
```

`/health` is deliberately left unauthenticated so load balancers and container probes can reach it.

## Attribution in metrics

The client name is a label on every request-scoped metric:

```promql
# tokens consumed per agent over 24h
sum by (client) (increase(proem_tokens_total[24h]))

# output tokens for one agent, split by which pool member served it
sum by (member) (increase(proem_tokens_total{client="agent-maria",type="output"}[24h]))

# which agent is burning through rate limits
sum by (client) (rate(proem_failovers_total[5m]))

# p95 latency per agent
histogram_quantile(0.95,
  sum by (le, client) (rate(proem_upstream_latency_seconds_bucket[5m])))
```

Labelled metrics: `proem_requests_total`, `proem_tokens_total`, `proem_upstream_latency_seconds`, `proem_failovers_total`.

Series count scales with clients × members, and the latency histogram multiplies that by its buckets. At a few dozen agents this is unremarkable; past a few hundred, consider dropping the client label from the histogram in your scrape config.

Requests that somehow reach the handler without passing authentication are labelled `client="unknown"`. In a normally configured proxy this should stay at zero, so it is worth alerting on.

## Rotating and revoking

- **Rotate:** issue a new token, add it as a second entry under a temporary name, move the agent over, then delete the old entry.
- **Revoke immediately:** set `enabled: false` (or delete the entry) and save. The next request from that agent is rejected; no restart needed.
- Historical metrics keep the old client name, so past usage stays attributable.

---

[← Back to README](../README.md) · [Getting started](getting-started.md) · [Architecture](architecture.md) · [How it works](how-it-works.md) · [Adding a pool member](adding-pool-member.md)

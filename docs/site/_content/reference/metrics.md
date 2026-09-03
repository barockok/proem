---
title: Metrics
description: Every series that Proem exports, and the number of series to expect.
---

| Metric | Labels | Notes |
|---|---|---|
| `proem_requests_total` | `client`, `member`, `code` | `code` is the status from the member, or `error` for a transport failure. |
| `proem_tokens_total` | `client`, `member`, `type` | `type` is `input`, `output`, `cache_read` or `cache_creation`. |
| `proem_thinking_tokens_total` | `client`, `member` | Part of the `output` count. Proem reports it apart, so a sum of `tokens_total` does not count it twice. |
| `proem_upstream_latency_seconds` | `client`, `member` | A histogram. It includes the time of any failover attempt. |
| `proem_failovers_total` | `client`, `from_member`, `reason` | `reason` is the keyword that matched, `retry-after`, or `transport`. |
| `proem_auth_failures_total` | `reason` | `missing_credentials`, `unknown_token` or `client_disabled`. |
| `proem_member_cooldown` | `member` | The value is `1` while the member is in cooldown. |
| `proem_sticky_hits_total` | `result` | `hit` or `miss`. Proem reports it only in `redis` affinity mode. |
| `proem_config_reloads_total` | `result` | `success` or `error`. |

## Token counts

Proem reads token counts from the shape that the member returns. It reads a
single JSON body and it reads an event stream. Accounting therefore does not
depend on whether the client asked to stream.

Proem counts cache tokens apart from input and output tokens. Members bill them
at a different rate. On a cached workload the cache counts are much larger. A
response that reports two input tokens and several hundred thousand cache reads
is normal. If you count only `input` and `output`, you see a small part of what
the workload used.

```promql
# what each client used, cache included
sum by (client) (increase(proem_tokens_total[24h]))

# how much of the input came from cache
sum(increase(proem_tokens_total{type="cache_read"}[24h]))
  / sum(increase(proem_tokens_total{type=~"input|cache_read"}[24h]))
```

## Number of series

The number of series grows with the number of clients multiplied by the number
of members. The latency histogram multiplies that number again by its buckets.

A few dozen clients create a small number of series. If you run several hundred
clients, remove the `client` label from the histogram in your scrape
configuration and keep it on the counters.

Proem labels a request that reaches the handler without authentication as
`client="unknown"`. In a correct configuration this count stays at zero, so an
alert on it is useful.

## Useful alerts

```promql
# every member is in cooldown, so there is no capacity
min(proem_member_cooldown) == 1

# someone tries to guess a token
rate(proem_auth_failures_total{reason="unknown_token"}[5m]) > 0

# Proem rejected a configuration change and still runs the old one
increase(proem_config_reloads_total{result="error"}[15m]) > 0
```

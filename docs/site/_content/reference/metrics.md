---
title: Metrics
description: Every series Proem exports, and the cardinality to expect.
---

| Metric | Labels | Notes |
|---|---|---|
| `proem_requests_total` | `client`, `member`, `code` | `code` is the upstream status, or `error` for a transport failure |
| `proem_tokens_total` | `client`, `member`, `type` | `type` is `input`, `output`, `cache_read` or `cache_creation` |
| `proem_thinking_tokens_total` | `client`, `member` | A **subset** of `output`, reported separately so summing `tokens_total` does not double-count |
| `proem_upstream_latency_seconds` | `client`, `member` | Histogram, includes any failover attempts |
| `proem_failovers_total` | `client`, `from_member`, `reason` | `reason` is the matched keyword, `retry-after`, or `transport` |
| `proem_auth_failures_total` | `reason` | `missing_credentials`, `unknown_token`, `client_disabled` |
| `proem_member_cooldown` | `member` | `1` while a member is cooling |
| `proem_sticky_hits_total` | `result` | `hit` or `miss`, only in `redis` sticky mode |
| `proem_config_reloads_total` | `result` | `success` or `error` |

## Counting tokens

Token counts are read from whichever shape the upstream returns — a JSON body
or an event stream — so accounting does not depend on whether the client asked
to stream.

Cache tokens are counted separately because they are billed differently and,
on a cached workload, dominate everything else. A response carrying two input
tokens against several hundred thousand cache reads is ordinary; counting only
`input` and `output` would report a small fraction of real consumption.

```promql
# real consumption per caller, cache included
sum by (client) (increase(proem_tokens_total[24h]))

# cache effectiveness
sum(increase(proem_tokens_total{type="cache_read"}[24h]))
  / sum(increase(proem_tokens_total{type=~"input|cache_read"}[24h]))
```

## Cardinality

Series scale with clients × members, and the latency histogram multiplies that
by its buckets. At a few dozen callers this is unremarkable. Past a few hundred,
consider dropping the `client` label from the histogram in your scrape config
while keeping it on the counters.

Requests that somehow reach the handler without passing authentication are
labelled `client="unknown"`. In a correctly configured deployment that stays at
zero, so it is worth alerting on.

## Worth alerting on

```promql
# every member cooling at once: no capacity left
min(proem_member_cooldown) == 1

# someone probing for a valid token
rate(proem_auth_failures_total{reason="unknown_token"}[5m]) > 0

# a config edit was rejected and the old one is still live
increase(proem_config_reloads_total{result="error"}[15m]) > 0
```

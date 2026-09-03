---
title: How it works
description: The path of a request through Proem. Authentication, member selection, credential injection, failover and accounting.
---

## Two credentials

Proem holds two credentials that must stay separate.

| Credential | Held by | Checked against |
|---|---|---|
| Client token | the client | the digests in `clients.yaml` |
| Member credential | Proem | the member |

Proem authenticates the client token and then removes it from the request. It
adds the member credential in place of it. A stolen client token gives access
to Proem only. To revoke that access, you change one line of configuration.

## The path of a request

```mermaid
sequenceDiagram
  autonumber
  participant C as Client
  participant A as Auth
  participant R as Router
  participant U as Member
  participant M as Metrics

  C->>A: POST /v1/messages with issued token
  alt token unknown or client disabled
    A-->>C: 401 or 403 in the Anthropic error shape
    A->>M: proem_auth_failures_total
  else client authenticated
    A->>R: request and client name
    R->>R: remove disabled and cooled members
    R->>U: forward with the member credential
    alt member reports a rate limit
      U-->>R: 429, 401 or 529
      R->>R: put the member in cooldown
      R->>U: retry on the next member
    end
    U-->>C: response streamed to the client
    R->>M: tokens, latency and status by client
  end
```

## How Proem selects a member

The router first removes members that are disabled or in cooldown. It then
selects one of the members that remain.

If the request carries a session identifier, the router hashes it. The same
session then reaches the same member each time. If there is no session
identifier, the router selects at random.

Both methods respect weight. A member with `weight: 4` receives about four
times the requests of a member with `weight: 1`.

Session affinity has three modes:

- `lb` hashes the session identifier. It needs no shared state.
- `redis` stores the pinned member in Redis.
- `none` disables affinity.

## Failover and streaming

Failover must read a response before it decides to retry. Proem cannot recall
bytes that it already sent to the client. Proem therefore decides from the
status and the headers, before it sends anything.

```mermaid
flowchart TB
  Resp["Response from member"] --> Cand{"Can this response<br/>fail over?<br/>429, 401, 529<br/>or Retry-After"}
  Cand -->|no| Stream["Send to the client<br/>flush each chunk"]
  Cand -->|yes| Buf["Read the body<br/>it is a small error"]
  Buf --> Check{"Does the body confirm<br/>a rate limit?"}
  Check -->|yes| Cool["Put the member in cooldown<br/>try the next member"]
  Check -->|no| Send["Send to the client"]
  Stream --> Usage["Read usage<br/>while copying"]
  Send --> Usage
```

Proem copies a response that cannot fail over straight to the client. It
flushes each chunk as it arrives, so a streaming client receives tokens as the
member produces them.

After a stream starts, Proem cannot fail over. It sends an error that arrives
in the middle of a stream to the client, because the client already has part of
the answer.

## Cooldown

When a member reports a rate limit, Proem writes a key to Redis with a time to
live. The router skips that member until the key expires.

Proem selects the time to live in this order:

1. The `Retry-After` header of the response, if the member sent one.
2. The `cooldownSec` field of the member.
3. A built-in default.

If Redis is not available, Proem fails open. It treats every member as healthy
and skips session affinity. A Redis outage lowers the quality of routing. It
does not stop traffic.

## Accounting

A usage observer reads the response while Proem copies it. The observer does
not change the bytes and does not delay them.

The observer reads both response shapes: a single JSON body, and an event
stream. Accounting therefore does not depend on whether the client asked to
stream.

Members disagree about which stream event carries which counter. The observer
therefore merges each counter from whichever event reports it, and keeps the
highest value it sees. It records input tokens, output tokens, cache reads and
cache writes, labelled by client and by member.

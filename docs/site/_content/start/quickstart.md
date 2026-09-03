---
title: Quickstart
description: Run Proem in front of one upstream, issue a client token, and make an authenticated request.
---

## 1. Install

```bash
git clone https://github.com/barockok/proem.git && cd proem
make install            # builds and installs to ~/.local/bin/proem
```

Or take a binary from the [releases page](https://github.com/barockok/proem/releases)
and install it with `./scripts/install-binary.sh <binary> ~/.local/bin/proem`.
See [install and upgrade](../deploy/install.md) for why that script exists.

## 2. Describe the pool

`pool.yaml` lists the upstreams Proem may route to. One is enough to start:

```yaml
members:
  - id: anthropic
    type: anthropic_api
    cred:
      env: ANTHROPIC_API_KEY     # or: file: /run/secrets/anthropic
    baseURL: https://api.anthropic.com
```

`type` selects how the credential is presented: `anthropic_api` sends
`x-api-key`, `anthropic_oauth` sends a bearer token with the OAuth beta header,
and `openrouter`, `deepseek` and `generic` send a plain bearer token.

## 3. Issue a token for each caller

```bash
proem issue-token agent-maria
```

```
Token for agent-maria (shown once, store it now):

  sk-ant-oat01-EXAMPLE00000000000000000000000000000000000000

Add to clients.yaml:

  - name: agent-maria
    tokenSHA256: 0000000000000000000000000000000000000000000000000000000000000001
```

Paste that entry into `clients.yaml`. Only the digest is stored, so the file is
not itself a secret. Proem will not start without a client registry, and any
request without a recognised token is rejected.

## 4. Run it

```bash
docker run -d -p 6379:6379 redis:7-alpine    # cooldown state; optional but recommended

export ANTHROPIC_API_KEY=sk-ant-api03-...
proem --config ./pool.yaml --clients ./clients.yaml \
      --redis-url redis://localhost:6379/0
```

Health on `:8080/health`, metrics on `:9090/metrics`.

## 5. Point a client at it

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-ant-oat01-...   # the token from step 3
```

Then use any Anthropic-compatible client:

```bash
curl -s $ANTHROPIC_BASE_URL/v1/messages \
  -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" \
  -H 'content-type: application/json' \
  -d '{"model":"claude-sonnet-4-20250514","max_tokens":64,
       "messages":[{"role":"user","content":"say hello"}]}'
```

Usage shows up immediately, attributed to the caller:

```bash
curl -s localhost:9090/metrics | grep proem_tokens_total
```

```
proem_tokens_total{client="agent-maria",member="anthropic",type="input"} 12
proem_tokens_total{client="agent-maria",member="anthropic",type="output"} 48
```

## Notes on client credentials

Proem accepts the caller's token from either `Authorization: Bearer` or
`x-api-key`, so it does not matter which environment variable your client uses
to carry it.

One caveat worth knowing: `CLAUDE_CODE_OAUTH_TOKEN` makes Claude Code assume it
is talking to Anthropic directly and request a one-hour prompt cache. Upstreams
that do not implement that reject the request with
``400 `cache_control.ttl: 1h` is not supported``. Passing the same token as
`ANTHROPIC_AUTH_TOKEN` avoids it.

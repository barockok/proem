---
title: Quickstart
description: Run Proem in front of one member, issue a client token, and send an authenticated request.
---

## 1. Install Proem

```bash
git clone https://github.com/barockok/proem.git
cd proem
make install
```

`make install` builds the binary and installs it to `~/.local/bin/proem`.

You can also download a binary from the
[releases page](https://github.com/barockok/proem/releases). Install it with
this command:

```bash
./scripts/install-binary.sh ./proem-darwin-arm64 ~/.local/bin/proem
```

[Install and upgrade](../deploy/install.md) explains why you must use that
script and not `cp`.

## 2. Write the pool file

`pool.yaml` lists the members that Proem can route to. One member is enough to
start.

```yaml
members:
  - id: anthropic
    type: anthropic_api
    cred:
      env: ANTHROPIC_API_KEY
    baseURL: https://api.anthropic.com
```

The `type` field selects how Proem sends the credential:

- `anthropic_api` sends the `x-api-key` header.
- `anthropic_oauth` sends a bearer token and the OAuth beta header.
- `openrouter`, `deepseek` and `generic` send a bearer token.

## 3. Issue a token for each client

```bash
proem issue-token agent-maria
```

The command prints the token one time. It also prints the entry to add to
`clients.yaml`:

```
Token for agent-maria (shown once, store it now):

  sk-ant-oat01-EXAMPLE00000000000000000000000000000000000000

Add to clients.yaml:

  - name: agent-maria
    tokenSHA256: 0000000000000000000000000000000000000000000000000000000000000001
```

Add that entry to `clients.yaml`. Proem stores only the digest, so the file is
not a secret. Proem does not start without a client registry, and it refuses
every request that does not carry a known token.

## 4. Run Proem

Start Redis first. Redis holds cooldown state. It is optional, but Proem routes
better with it.

```bash
docker run -d -p 6379:6379 redis:7-alpine
```

Then start Proem:

```bash
export ANTHROPIC_API_KEY=sk-ant-api03-...
proem --config ./pool.yaml --clients ./clients.yaml \
      --redis-url redis://localhost:6379/0
```

Proem serves the proxy on port 8080 and metrics on port 9090.

## 5. Send a request

Set two variables in the client:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-ant-oat01-...
```

Use the token from step 3. Then call the API with any Anthropic-compatible
client:

```bash
curl -s $ANTHROPIC_BASE_URL/v1/messages \
  -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" \
  -H 'content-type: application/json' \
  -d '{"model":"claude-sonnet-4-20250514","max_tokens":64,
       "messages":[{"role":"user","content":"say hello"}]}'
```

Proem records the usage immediately:

```bash
curl -s localhost:9090/metrics | grep proem_tokens_total
```

```
proem_tokens_total{client="agent-maria",member="anthropic",type="input"} 12
proem_tokens_total{client="agent-maria",member="anthropic",type="output"} 48
```

## Which variable carries the token

Proem reads the client token from the `Authorization` header or from the
`x-api-key` header. Both work, so the variable name in your client does not
matter to Proem.

The variable name can matter to the client. `CLAUDE_CODE_OAUTH_TOKEN` makes
Claude Code assume that it calls Anthropic directly. Claude Code then asks for
a one-hour prompt cache. A member that does not support that cache rejects the
request:

```
400 `cache_control.ttl: 1h` is not supported
```

To avoid this, pass the same token as `ANTHROPIC_AUTH_TOKEN`.

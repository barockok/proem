---
title: Docker
description: How to run Proem from the published container image.
---

CI publishes an image to the GitHub Container Registry for each release.

```bash
docker pull ghcr.io/barockok/proem:0.3.2   # also :0.3 and :latest
```

```bash
docker run -d --name proem \
  -p 8080:8080 -p 9090:9090 \
  -v $PWD/pool.yaml:/pool.yaml:ro \
  -v $PWD/clients.yaml:/clients.yaml:ro \
  -e ANTHROPIC_API_KEY \
  ghcr.io/barockok/proem:0.3.2 \
  --config /pool.yaml --clients /clients.yaml \
  --redis-url redis://redis:6379/0
```

The image is distroless and runs as a user that is not root. A credential file
that you mount into the container must be readable by that user.

## Compose

```yaml
services:
  proem:
    image: ghcr.io/barockok/proem:0.3.2
    command: >-
      --config /pool.yaml --clients /clients.yaml
      --redis-url redis://redis:6379/0
      --log-format json
    ports: ["8080:8080", "9090:9090"]
    environment:
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    volumes:
      - ./pool.yaml:/pool.yaml:ro
      - ./clients.yaml:/clients.yaml:ro
    depends_on: [redis]

  redis:
    image: redis:7-alpine
```

## Credentials

Use a mounted file instead of an environment variable when your orchestrator
supports secrets:

```yaml
members:
  - id: anthropic
    type: anthropic_api
    cred:
      file: /run/secrets/anthropic
    baseURL: https://api.anthropic.com
```

## Reload inside a container

Proem watches the path that you give it. It reloads a mounted file that you
edit in place.

Some orchestrators replace a configuration file by moving a symbolic link.
Proem handles that too. If a change fails validation, Proem keeps the
configuration that is already running and increases
`proem_config_reloads_total{result="error"}`.

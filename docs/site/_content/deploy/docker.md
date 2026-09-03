---
title: Docker
description: Running Proem from the published container image.
---

Images are published to the GitHub Container Registry on every release:

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

The image is distroless and runs as a non-root user, so credential files
mounted into it must be readable by that user.

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

Prefer a mounted file over an environment variable where your orchestrator
supports secrets:

```yaml
members:
  - id: anthropic
    type: anthropic_api
    cred:
      file: /run/secrets/anthropic
    baseURL: https://api.anthropic.com
```

## Hot reload in a container

The config watcher follows the path it was given. A bind-mounted file that is
edited in place reloads normally. Some orchestrators replace configuration by
swapping a symlink instead, which is also handled — a rejected edit leaves the
previous configuration in force and is reported on
`proem_config_reloads_total{result="error"}`.

# SSO gateway end-to-end suite

This suite proves the whole SSO login with a real Keycloak and the real Claude
Code CLI. It needs Docker, no secrets, and makes no call to Anthropic.

```bash
make e2e
```

`make e2e` and the `E2E` GitHub workflow do the same steps:

1. `e2e/certs/gen.sh` creates a throwaway CA and a certificate for the proem
   and mock hosts in `e2e/certs/out/`.
2. `docker compose -f e2e/docker-compose.yml up -d --build --wait` starts the
   stack and waits for every healthcheck.
3. `... --profile driver run --rm driver` runs the protocol scenarios.
4. `... --profile claude run --rm claude` runs the real Claude Code smoke.
5. `... down -v` removes the stack.

## Services

| Service | Role |
|---|---|
| `keycloak` | Realm `proem` from `keycloak/realm-proem.json`. Issuer `http://keycloak:8080/realms/proem`. |
| `redis` | Device grants. |
| `mock-upstream` | Stands in for `api.anthropic.com` over TLS. `GET /_requests` lists what it received. |
| `proem` | Gateway on, native TLS, `https://proem:8443`. |
| `proem-short` | Gateway on with short grant TTL, session-max and rate limit. |
| `proem-legacy` | Gateway off. Token clients only. |
| `driver` | `go test -tags e2e ./e2e/driver`. Plays Claude Code and the browser. |
| `claude` | Claude Code 2.1.197 in headless gateway mode, with a JWT from the driver. |

`clients.yaml` lives in a shared volume, not a single-file bind mount, so the
driver can replace it by rename, as editors and ConfigMaps do.

## Users

| User | Password | In `clients.yaml` | Expected |
|---|---|---|---|
| `alice@example.com` | `alice-pw` | yes | signs in |
| `bob@example.com` | `bob-pw` | no | "not registered" |
| `dave@example.com` | `dave-pw` | yes, email not verified | rejected |

## Image overrides

Set `KEYCLOAK_IMAGE` to pull Keycloak from a mirror, for example
`KEYCLOAK_IMAGE=keycloak/keycloak:26.4.0 make e2e`.

## Bumping Claude Code

The Claude Code version is pinned in `claude/Dockerfile`. Bump it on purpose:
a protocol change in a new release then fails the e2e check on that pull
request. The interactive `/login` (TLS trust prompt and browser) stays a manual
staging check.

# SSO Gateway Login for Claude Code

- **Status:** Draft, pending review
- **Date:** 2026-10-19
- **Target release:** v0.5.0

## Goal

Let Claude Code users sign in to proem with company SSO through `/login`, instead of holding a
static `sk-ant-oat01-` token. Proem acts as the Claude Code "enterprise gateway"
(`forceLoginGatewayUrl`). After SSO, proem issues its own session token. Each request maps the
signed-in email to exactly one client in `clients.yaml`.

## Success criteria

1. With managed policy `forceLoginMethod: "gateway"` and `forceLoginGatewayUrl: <proem public URL>`,
   `/login` in Claude Code opens the configured OIDC provider (Keycloak or Google) and completes.
2. Only users whose verified email matches a `clients.yaml` entry get a token.
3. Usage, metrics, and sticky routing attribute to that client name, same as token clients today.
4. Removing or disabling the client in `clients.yaml` rejects the next request (401/403).
5. Disabling the user at the provider (or ending their session) stops access at the next refresh
   (at most `session-ttl`, default 1h).
6. Existing `sk-ant-oat01-` clients work unchanged.

## Non-goals

- No change to the upstream path. After `Auth` resolves a client name, pool selection, sticky
  routing, failover, and upstream credential injection are identical for SSO and token clients.
- No policy served from `/managed/settings` (returns 404).
- No `/v1/models` discovery, no `/user/bootstrap` (Desktop only), no admin or spend API.
- No group-based or domain-wide mapping. One client per person, by email.

## Background: Claude Code gateway protocol (v2.1.197)

Observed in the Claude Code 2.1.197 client and its built-in `claude gateway` server.

- Login is the OAuth 2.0 device authorization grant (RFC 8628). No loopback callback, no client ID
  or PKCE on the client side.
- Client calls `GET {base}/.well-known/oauth-authorization-server` and reads
  `device_authorization_endpoint` and `token_endpoint`. Endpoints not same-origin as `base` are
  ignored (falls back to `/oauth/device_authorization` and `/oauth/token`). Therefore the client can
  never refresh directly with the IdP.
- `POST device_authorization_endpoint` (empty form body) expects
  `{device_code, user_code, verification_uri, verification_uri_complete?, expires_in, interval?}`.
- Client polls `POST token_endpoint` with `grant_type=urn:ietf:params:oauth:grant-type:device_code`.
  Success: `{access_token, expires_in, refresh_token?}`. Errors read from `error`:
  `authorization_pending`, `slow_down` (+5s), `expired_token`, `access_denied`.
- Refresh: `grant_type=refresh_token` to the same endpoint, 5 minutes before expiry. `invalid_grant`
  clears the refresh token (user must `/login` again); other errors are retried as transient.
  If the response omits `refresh_token`, the client keeps the old one.
- The access token is opaque to the client. It is sent as `Authorization: Bearer` to
  `/v1/messages`, `/v1/messages/count_tokens`, `GET /managed/settings`, and OTLP
  `/v1/{metrics,logs,traces}` (only when telemetry is enabled).
- `https://` is required except for loopback hosts. The client pins the TLS leaf certificate on
  first login and re-checks it on startup.
- A 401 with Anthropic error envelope prompts the user to log in again.

## Architecture

```
Claude Code --/login--> GET  /.well-known/oauth-authorization-server
            ---------->  POST /oauth/device_authorization   -> Redis grant {pending}
browser     ---------->  GET  /device?user_code=XXXX-XXXX   (confirm page)
            ---------->  POST /device                       -> 302 to IdP (PKCE + state)
IdP         ---------->  GET  /oauth/callback               -> verify ID token -> email
                                                             -> clients.yaml lookup
                                                             -> grant approved | denied
Claude Code --poll---->  POST /oauth/token (device_code)    -> {proem JWT, enc(IdP refresh)}
            --refresh->  POST /oauth/token (refresh_token)  -> IdP token endpoint (server side)
                                                             -> re-check client -> new pair
runtime:    Bearer <proem JWT>       -> Auth: verify JWT -> email -> client -> existing proxy
            Bearer sk-ant-oat01-...  -> Auth: SHA-256 lookup (unchanged)  -> existing proxy
```

The gateway is disabled unless `--public-url` is set. With it unset, proem behaves exactly as v0.4.0.

## Components

New package `internal/gateway`. One responsibility per file.

| File | Responsibility |
|---|---|
| `gateway.go` | `Config`, `New(...)`, `Routes(chi.Router)`; wires the parts below |
| `metadata.go` | `GET /.well-known/oauth-authorization-server` (RFC 8414; same-origin endpoints, `grant_types_supported`) |
| `device.go` | `POST /oauth/device_authorization`; `GET /device` confirm page; `POST /device` redirect to IdP |
| `callback.go` | `GET /oauth/callback`: check state, exchange code with PKCE verifier, verify ID token, resolve client, mark grant |
| `token.go` | `POST /oauth/token`: `device_code` and `refresh_token` grants |
| `session.go` | Issue and verify proem JWT (HS256, multi-key); encrypt and decrypt refresh blobs (AES-256-GCM) |
| `grants.go` | Redis-backed grant store (create, get by device code, get by user code, approve, deny, poll rate) |
| `oidc.go` | Provider abstraction over `coreos/go-oidc` + `golang.org/x/oauth2`: auth URL, exchange, refresh, userinfo fallback, claim checks |
| `stubs.go` | `GET /managed/settings` -> 404; `POST /v1/{metrics,logs,traces}` -> 200, body discarded (JWT required) |

Changes to existing code:

- `internal/client/types.go`: optional `Email string` on `Client`. `Validate` builds a lowercase
  email index, rejects duplicate emails, and requires at least one of `email` or `tokenSHA256`.
  New `Registry.LookupEmail(email string) (Client, bool)`, case-insensitive.
- `internal/proxy/auth.go`: new optional `SessionVerifier` dependency. If set and the bearer token
  verifies as a proem JWT, resolve the client via `LookupEmail`. Otherwise fall through to the
  existing hash lookup. `IsEnabled`, credential stripping, and context tagging stay shared.
- `internal/app/app.go`: when enabled, mount gateway routes before the `/*` catch-all.
  Pass the verifier to `proxy.Auth`.
- `internal/config/config.go`: new flags and env (below). Startup error if the gateway is enabled
  without Redis, OIDC issuer, client credentials, JWT secret, or refresh key.
- `cmd/proem`: optional TLS listener when `--tls-cert` and `--tls-key` are set.
- `clients.yaml.example`, README, docs site: document `email` entries and gateway setup.

## Data

### clients.yaml

```yaml
clients:
  - name: agent-test            # token client, unchanged
    tokenSHA256: 3f1a...
  - name: alice                 # SSO client
    email: alice@example.com
  - name: both                  # may hold both
    email: someone@example.com
    tokenSHA256: 9c2b...
    enabled: false
```

### Session JWT (access token)

- HS256. Header `kid` = first 8 bytes of sha256(secret), hex.
- Claims: `iss` = public URL, `aud` = `proem`, `sub` = IdP subject, `email`, `iat`, `exp`
  (`iat` + `session-ttl`).
- Verification: signature against any configured key (rotation), `iss`, `aud`, `exp` with 60s skew.
- The JWT does not carry the client name. The client is resolved from `email` on every request so
  `clients.yaml` changes take effect immediately.

### Refresh token (returned to the client)

- `prt1.` + base64url(AES-256-GCM(nonce, JSON{idp_refresh_token, sub, email, iat})).
- Opaque to the client. The IdP refresh token is never exposed in clear.
- Decryption failure or unknown prefix -> `invalid_grant`.

### Device grant (Redis)

- Keys: `proem:gw:dc:<sha256(device_code)>` -> JSON grant; `proem:gw:uc:<user_code>` -> device code hash.
- Grant: `{status: pending|approved|denied|consumed, user_code, pkce_verifier, state, email,
  sub, enc_refresh, last_poll, created}`. TTL `--device-grant-ttl` (default 600s).
- `device_code`: 32 random bytes, base64url. `user_code`: 8 chars from
  `BCDFGHJKMNPQRSTVWXYZ`, displayed `XXXX-XXXX`.
- Poll faster than `interval` (5s) -> `slow_down`. Approved grant is consumed on first successful
  token exchange (single use).

## Flows

### Login

1. `POST /oauth/device_authorization` creates a pending grant. Response uses
   `verification_uri = {public}/device` and `verification_uri_complete = {public}/device?user_code=...`.
2. `GET /device?user_code=...` shows the code and a "Continue to sign in" button (CSRF-protected
   form). Unknown or expired code -> error page.
3. `POST /device` generates `state` and PKCE verifier, stores them on the grant, sets a short-lived
   HttpOnly `gw_dev` cookie binding the browser to the grant, and redirects to the IdP authorize URL
   with configured scopes and extra params.
4. `GET /oauth/callback` checks `state` against the cookie-bound grant, exchanges the code, and
   verifies the ID token (issuer, audience, nonce, expiry). Required: `email`, `email_verified=true`.
   If `--oidc-allowed-domain` is set, the email domain (and Google `hd` claim when present) must match.
5. `LookupEmail(email)`: found and enabled -> grant approved, store encrypted IdP refresh token,
   page says "Signed in. Return to Claude Code." Not found or disabled -> grant denied, page says
   "This account is not registered with proem. Ask an admin to add it."
6. Client poll on `/oauth/token` returns the token pair.

### Refresh

1. Decrypt refresh blob. Call the IdP token endpoint with `grant_type=refresh_token` (server side).
2. Take identity from the new ID token if returned, else call the userinfo endpoint. `sub` must equal
   the original `sub`.
3. Re-check `LookupEmail` and `IsEnabled`.
4. Issue a new JWT. Return a new refresh blob wrapping the rotated IdP refresh token if the IdP
   returned one, otherwise the original.

### Runtime

`Auth` order: extract bearer or `x-api-key` -> if verifier set and token verifies as proem JWT ->
`LookupEmail` -> else `Lookup` (hash) -> `IsEnabled` -> strip credentials -> existing handler.
A token that looks like a JWT (three dot-separated base64url parts) but fails verification is
rejected as `invalid_session` (401), not passed to the hash lookup.

## Configuration

| Flag | Env | Default | Notes |
|---|---|---|---|
| `--public-url` | `PROEM_PUBLIC_URL` | empty (gateway off) | Must be `https://` unless loopback |
| `--oidc-issuer` | `OIDC_ISSUER` | | Keycloak `https://kc/realms/<realm>` or `https://accounts.google.com` |
| | `OIDC_CLIENT_ID` | | |
| | `OIDC_CLIENT_SECRET` | | Vault |
| `--oidc-scopes` | `OIDC_SCOPES` | `openid,email,profile` | Keycloak may add `offline_access` |
| `--oidc-auth-params` | `OIDC_AUTH_PARAMS` | empty | Google: `access_type=offline,prompt=consent` |
| `--oidc-allowed-domain` | `OIDC_ALLOWED_DOMAIN` | empty | Optional email domain restriction |
| | `PROEM_JWT_SECRET` | | Vault. Comma list, each >= 32 bytes. First signs, all verify |
| | `PROEM_REFRESH_KEY` | | Vault. 32 bytes, base64 |
| `--session-ttl` | `PROEM_SESSION_TTL` | `1h` | |
| `--device-grant-ttl` | | `10m` | Device grant lifetime (e2e shortens it) |
| `--tls-cert`, `--tls-key` | | empty | Optional native TLS for the proxy listener |

Redirect URI to register at the IdP: `{public-url}/oauth/callback`.

## Provider notes

| | Keycloak | Google |
|---|---|---|
| Refresh token issued | Yes (session-bound; `offline_access` for offline tokens) | Only with `access_type=offline` + `prompt=consent` |
| Refresh rotation | Often rotates | Usually does not |
| ID token on refresh | Yes | Usually yes |
| Session end | Realm SSO session max / admin logout | User removal, token revocation |

Both handled by the same code path; differences are configuration only.

## Errors

| Case | Response |
|---|---|
| Missing/invalid JWT on proxy routes | 401 Anthropic envelope `authentication_error` (client prompts `/login`) |
| Client disabled | 403 `permission_error` |
| Email not in `clients.yaml` at runtime | 401 `authentication_error`, reason `unknown_email` |
| Token endpoint, pending/slow/expired/denied | 400 OAuth `error` per RFC 8628 |
| Refresh rejected by IdP or user removed | 400 `invalid_grant` |
| IdP or Redis unreachable during refresh/login | 503 `temporarily_unavailable` |
| Redis down | Login fails (503). Runtime requests with valid JWT unaffected |

All rejections increment `metrics.AuthFailures{reason}` and log without the token (fingerprint only),
matching existing behaviour.

## Security

- Device code and user code high entropy; user code single-use; grant TTL 600s; poll rate limited.
- PKCE (S256) and `state` on the IdP leg; `gw_dev` cookie binds callback to the browser that started it.
- `/device` POST is CSRF-protected.
- IdP refresh token only ever stored encrypted (client side) and in Redis for at most the grant TTL.
- JWT secrets and refresh key from Vault, never logged. Secret rotation via comma list.
- Constant-time comparisons for codes. No token values in logs.

## Testing

- Unit: JWT issue/verify (expiry, bad signature, wrong `iss`/`aud`, rotation); refresh blob round
  trip and tamper rejection; registry email index (case-insensitive, duplicates, email-only,
  token-only, both); `Auth` with JWT, hash token, malformed JWT, disabled client.
- Gateway integration: `httptest` fake OIDC provider (discovery, JWKS, authorize, token, userinfo)
  plus `miniredis`. Cover full device flow, `slow_down`, expiry, denied (not registered,
  `email_verified=false`, wrong domain), refresh success with and without rotation, refresh
  `invalid_grant`, IdP down (503), `sub` mismatch on refresh.
- Regression: all existing tests pass unchanged.
- staging end to end: Keycloak test client and a disposable Claude Code container with managed policy pointing at
  `https://proem.staging.example.com`. `/login`, send a message, force refresh, remove email from
  `clients.yaml`, confirm 401.

## End-to-end automation (docker-compose, GitHub Actions)

A self-contained e2e suite proves the whole flow with a real Keycloak, with no secrets and no
calls to Anthropic. It runs locally with `make e2e` and in CI on every pull request.

### Layout

```
e2e/
  docker-compose.yml
  certs/gen.sh                 # throwaway CA + leaf for proem (SAN: proem), generated per run
  keycloak/realm-proem.json    # realm import: client, users
  proem/pool.yaml              # one anthropic_api member -> mock-upstream
  proem/clients.yaml           # alice (email), legacy token client, disabled client
  mock-upstream/               # tiny Go server: canned Messages JSON + SSE, records requests
  driver/                      # Go e2e tests (build tag e2e), run as a compose service
  claude/                      # real Claude Code smoke (Dockerfile + managed settings)
```

### Services (one compose network, so every hostname resolves the same for all parties)

| Service | Image | Role |
|---|---|---|
| `keycloak` | `quay.io/keycloak/keycloak:<pinned>` | `start-dev --import-realm`, health on management port. Issuer `http://keycloak:8080/realms/proem` |
| `redis` | `redis:7-alpine` | Device grants and sticky store |
| `mock-upstream` | built from `e2e/mock-upstream` | Stands in for `api.anthropic.com`. Exposes `GET /_requests` for assertions |
| `proem` | built from repo `Dockerfile` | Gateway on, native TLS (`--tls-cert/--tls-key`), `--public-url=https://proem:8443`, `clients.yaml` bind-mounted read-write so tests can edit it |
| `driver` | built from `e2e/driver` | Runs `go test -tags e2e ./...`; trusts the generated CA; exits non-zero on failure |
| `claude` | built from `e2e/claude` (profile `claude`) | Real Claude Code CLI, pinned version, for the client-compat smoke |

All services have healthchecks; `docker compose up --wait` blocks until Keycloak, Redis, the mock,
and proem are healthy.

### Keycloak realm fixture

- Realm `proem`. Confidential client `proem`, fixed test secret, standard flow on, redirect URI
  `https://proem:8443/oauth/callback`, PKCE S256 required.
- Users (password login, no required actions):
  - `alice@example.com`, email verified. In `clients.yaml` as client `alice`.
  - `bob@example.com`, email verified. Not in `clients.yaml`.
  - `dave@example.com`, email not verified. In `clients.yaml`.
- Admin credentials for the master realm are test-only env values, used by the driver to disable
  users and end sessions.

### Driver scenarios

The driver plays both parties. As **Claude Code** it does discovery, device authorization, and
polling exactly as the 2.1.197 client does. As the **browser** it uses an HTTP client with a cookie
jar: open `verification_uri_complete`, submit the `/device` form (CSRF token parsed from HTML),
follow the redirect to the Keycloak login page, post the login form, follow the redirect to
`/oauth/callback`. No headless browser is needed.

1. Discovery: metadata endpoints are same-origin; `gateway_protocol_version` present.
2. Happy path: alice logs in; poll returns `authorization_pending` before approval and the token
   pair after; a second exchange of the same device code fails (single use).
3. Runtime: `POST /v1/messages` (JSON and SSE) with the JWT succeeds. The mock upstream recorded the
   pool member's credential, never the JWT, and proem metrics label the request `client="alice"`.
4. Poll faster than `interval` returns `slow_down`.
5. Refresh: `grant_type=refresh_token` returns a new pair; the new JWT works.
6. Revocation via clients.yaml: driver removes alice's entry (file watch reload). Next request is
   401 and the next refresh is `invalid_grant`. Entry restored afterwards.
7. Revocation via IdP: driver disables alice through the Keycloak admin API and ends her sessions.
   Refresh returns `invalid_grant`. User re-enabled afterwards.
8. Denials: bob ends with `access_denied` and the "not registered" page; dave is rejected for
   `email_verified=false`.
9. Expiry: a grant left pending past its TTL returns `expired_token` (TTL shortened via test flag).
10. Legacy: an `sk-ant-oat01-` client in `clients.yaml` still works, with or without the gateway.
11. Hardening: tampered JWT, JWT signed with an unknown key, tampered refresh blob, and wrong
    `state` on the callback are all rejected.

### Real Claude Code smoke

The interactive `/login` (TLS trust prompt, browser) cannot run in CI. Instead the `claude`
service uses the official headless gateway mode with a token the driver obtained through the real
device flow:

```
CLAUDE_CODE_USE_GATEWAY=1
ANTHROPIC_BASE_URL=https://proem:8443
ANTHROPIC_AUTH_TOKEN=<JWT from driver>
NODE_EXTRA_CA_CERTS=/certs/ca.pem
claude -p "reply with ok"
```

The run must exit 0 and the mock upstream must have received the request attributed to alice.
This proves the real client accepts proem's JWT, TLS, and response shapes. The interactive
`/login` path stays a manual staging check.

### GitHub Actions

New workflow `.github/workflows/e2e.yml`, triggered on `pull_request` and pushes to `main`:

1. Checkout.
2. `e2e/certs/gen.sh` creates the CA and leaf certificate.
3. `docker compose -f e2e/docker-compose.yml up -d --build --wait`.
4. `docker compose -f e2e/docker-compose.yml run --rm driver` (protocol scenarios, also writes the
   JWT for step 5 to a shared volume).
5. `docker compose -f e2e/docker-compose.yml --profile claude run --rm claude`.
6. On failure: `docker compose logs` for all services uploaded as an artifact.
7. Always: `docker compose down -v`.

No repository secrets are used. Image versions (Keycloak, Redis, Claude Code) are pinned, and the
Claude Code version is bumped deliberately so protocol drift shows up as a failing PR.
The existing `ci.yml` (unit tests, coverage gate, smoke) is unchanged; e2e is a separate required
check. `make e2e` runs the same steps locally.

Google is not exercised in e2e (it needs a real Google account). Google behaviour (no refresh
rotation, `hd` claim, extra auth params) is covered by the fake-provider integration tests.

## Rollout

1. Release v0.5.0 (gateway off by default).
2. Staging deployment: add flags and Vault keys, add `email:` entries to `clients.yaml`.
3. IdP: register confidential client `proem` with redirect `{public-url}/oauth/callback`.
4. Pilot laptops: managed settings `forceLoginMethod: "gateway"`, `forceLoginGatewayUrl`.
5. Prod after staging sign-off.

## Assumptions and risks

- The public URL serves HTTPS with a certificate laptops trust, and is reachable from users'
  browsers (VPN/internal). Either an ingress terminates TLS or proem's native TLS is used.
  The client pins the leaf certificate, so certificate rotation forces users to re-trust.
- The protocol is from an undocumented Claude Code build (2.1.197). Future versions may change it.
  The `gateway_protocol_version: 1` field is advertised in metadata to match the built-in gateway.
- `forceLoginGatewayUrl` is honoured only from admin-controlled managed settings.

# NodeHoster REST API

All endpoints live under `/api` on the admin listener (default `https://<host>:8484`).
JSON in, JSON out. Types referenced below are the Go structs in `internal/model`
(field names are the `json` tags).

## Conventions

- **Auth**: browser sessions use the `nh_session` cookie (HttpOnly, SameSite=Strict)
  set by `POST /api/auth/login`. Automation uses `Authorization: Bearer nh_…` API tokens.
- **CSRF**: every non-GET request from the browser must send `X-Requested-With: NodeHoster`.
- **Errors**: non-2xx responses have body `{ "error": "message", "field": "bindings[0].host" }`
  (`field` only for validation errors, 422).
- **Secrets**: secret fields (env vars with `secret: true`, `runAs.password`, `deploy.git.token`,
  `deploy.webhookSecret`, DNS credentials, `acme.eabHmac`) are returned as
  `"__SECRET__"` when set. Sending `"__SECRET__"` back leaves the stored value unchanged.
- **Roles**: `viewer` read-only; `operator` may start/stop/restart/deploy; `admin` everything.
  403 when not permitted.

## Auth

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/auth/login` | `{username, password, totp?}` | `{user}`; 401 `{error, totpRequired: true}` when a TOTP code is needed |
| POST | `/api/auth/logout` | | 204 |
| GET | `/api/auth/me` | | `{user, mustChangePassword}` |
| POST | `/api/auth/password` | `{current, new}` | 204 |
| POST | `/api/auth/totp/setup` | | `{secret, url}` (otpauth:// URL for a QR code) |
| POST | `/api/auth/totp/enable` | `{code}` | 204 |
| POST | `/api/auth/totp/disable` | `{code}` | 204 |

## Server

| Method | Path | Response |
|---|---|---|
| GET | `/api/server/info` | `ServerInfo` |
| GET | `/api/server/metrics?minutes=60` | `MetricPoint[]` (whole server, 1 point/min) |
| GET | `/api/events?limit=100&siteId=` | `Event[]` newest first |
| GET | `/api/audit?limit=100&offset=0` | `AuditEntry[]` newest first |
| GET | `/api/stream` | **Server-Sent Events**: `event: status` data `SiteStatus[]` every 2s; `event: event` data `Event` as they happen |
| GET | `/api/backup` | JSON file download (sites, certificates metadata, settings) |
| POST | `/api/restore` | multipart `file` → 204 |

## Sites

`SiteView` = `Site` + `"status": SiteStatus`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/sites` | | `SiteView[]` |
| POST | `/api/sites` | `Site` (id ignored) | `SiteView` 201 |
| GET | `/api/sites/{id}` | | `SiteView` |
| PUT | `/api/sites/{id}` | `Site` | `SiteView` (applied live; node sites are recycled if process settings changed) |
| DELETE | `/api/sites/{id}?deleteFiles=true` | | 204 |
| POST | `/api/sites/{id}/start` | | `SiteStatus` |
| POST | `/api/sites/{id}/stop` | | `SiteStatus` |
| POST | `/api/sites/{id}/restart` | | `SiteStatus` (hard: stop then start) |
| POST | `/api/sites/{id}/recycle` | | `SiteStatus` (zero-downtime rolling restart) |
| GET | `/api/sites/{id}/status` | | `SiteStatus` |
| GET | `/api/sites/{id}/metrics?minutes=60` | | `MetricPoint[]` |
| GET | `/api/sites/{id}/logs?type=app&lines=500` | | `LogLine[]` (`type`: `app` \| `access`) |
| GET | `/api/sites/{id}/logs/stream?type=app` | | SSE `event: log` data `LogLine` |
| GET | `/api/sites/{id}/logs/download?type=app` | | text file |
| POST | `/api/sites/{id}/logs/clear` | | 204 |

### Deployments

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/sites/{id}/deployments` | | `Deployment[]` newest first |
| POST | `/api/sites/{id}/deploy/zip` | multipart `file` (.zip) | `Deployment` 202 |
| POST | `/api/sites/{id}/deploy/git` | `{branch?}` | `Deployment` 202 |
| POST | `/api/sites/{id}/deployments/{depId}/activate` | | `Deployment` (rollback) |
| GET | `/api/sites/{id}/deployments/{depId}/log` | | `text/plain` |
| GET | `/api/sites/{id}/deployments/{depId}/log/stream` | | SSE `event: log` data string, `event: done` data `Deployment` |

Webhook (no session): `POST /hooks/deploy/{siteId}` — GitHub/Gitea style
`X-Hub-Signature-256: sha256=<hmac of body with deploy.webhookSecret>`, or `?secret=`.

## Certificates

`CertificateView` = `Certificate` + `"usedBy": [{siteId, siteName, binding}]`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/certificates` | | `CertificateView[]` |
| GET | `/api/certificates/{id}` | | `CertificateView` |
| POST | `/api/certificates/acme` | `{name, domains[], acme: ACMEOptions, autoRenew}` | `CertificateView` 202 (status `pending`, issued asynchronously) |
| POST | `/api/certificates/import` | multipart: `file` (.pfx/.p12 or .pem/.crt), optional `keyFile`, `password`, `name` | `CertificateView` |
| POST | `/api/certificates/selfsigned` | `{name, domains[], validDays}` | `CertificateView` |
| POST | `/api/certificates/{id}/renew` | | `CertificateView` 202 |
| PUT | `/api/certificates/{id}` | `{name, autoRenew}` | `CertificateView` |
| DELETE | `/api/certificates/{id}` | | 204 (409 if in use) |
| POST | `/api/certificates/{id}/export` | `{format: "pfx"\|"pem", password?}` | file download (pfx, or zip of PEMs) |

## Node.js runtimes

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/node/versions` | | `{system: {version, path} \| null, installed: [{version, path, status, progress, error?, isDefault}]}` (`status`: installing \| installed \| error) |
| GET | `/api/node/available` | | `[{version, lts: string\|false, date, security}]` |
| POST | `/api/node/versions` | `{version}` | 202 |
| DELETE | `/api/node/versions/{version}` | | 204 (409 if used by a site) |

## Settings, users, tokens

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/settings` | | `Settings` |
| PUT | `/api/settings` | `Settings` | `Settings` |
| GET | `/api/settings/dns-catalog` | | `[{code, name, fields: [{key, label, secret, optional}]}]` |
| POST | `/api/settings/webhooks/test` | `WebhookTarget` | 204 |
| GET | `/api/settings/admin` | | `{listen, tls: "selfsigned"\|"certificate"\|"none", certificateId, restartRequired}` |
| PUT | `/api/settings/admin` | same | same (takes effect after service restart) |
| GET | `/api/users` | | `User[]` |
| POST | `/api/users` | `{username, password, role}` | `User` |
| PUT | `/api/users/{id}` | `{role?, disabled?, password?}` | `User` |
| DELETE | `/api/users/{id}` | | 204 |
| GET | `/api/tokens` | | `APIToken[]` (own) |
| POST | `/api/tokens` | `{name, expiresDays?}` | `{token: "nh_…", info: APIToken}` (token shown once) |
| DELETE | `/api/tokens/{id}` | | 204 |

## Prometheus

`GET /metrics` on the admin listener (requires a bearer token) exposes
`nodehoster_requests_total{site,code}`, `nodehoster_instance_memory_bytes`, etc.

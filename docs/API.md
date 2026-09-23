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
| PUT | `/api/users/{id}` | `{role?, disabled?, password?, resetTotp?}` | `User` (`resetTotp: true` turns two-factor off, for a lost authenticator; ends the user's sessions) |
| DELETE | `/api/users/{id}` | | 204 |
| GET | `/api/tokens` | | `APIToken[]` (own) |
| POST | `/api/tokens` | `{name, expiresDays?}` | `{token: "nh_…", info: APIToken}` (token shown once) |
| DELETE | `/api/tokens/{id}` | | 204 |

## URL rewrite and MIME types

Rewrite rules, outbound rules, rewrite maps and per-site MIME types are
part of a site (`routing.rewrites`, `routing.outboundRules`,
`routing.rewriteMaps`, `routing.mimeTypes`, `routing.unknownMimeTypes`)
and saved with `PUT /api/sites/{id}`. Server-wide MIME types are
`Settings.mime`.

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/rewrite/import` | `{format: "webconfig"\|"htaccess", text}` | `{rules, outboundRules, rewriteMaps, warnings}` — converted, not saved |
| GET | `/api/mime/defaults` | | `MimeMap[]` — the built-in table |

Inbound rules match a regular expression against the path including its
leading `/`. Targets, condition inputs and outbound values may use
`{R:n}` or `$n` (rule captures), `{C:n}` (captures of the last matched
condition), server variables (`{HTTP_HOST}`, `{QUERY_STRING}`, `{URL}`,
`{REQUEST_URI}`, `{REQUEST_METHOD}`, `{REMOTE_ADDR}`, `{HTTPS}`,
`{SERVER_PORT}`, `{REQUEST_FILENAME}`, `{CACHE_URL}`, any header as
`{HTTP_X_NAME}`; in outbound rules also `{RESPONSE_X_NAME}`), rewrite maps
(`{MapName:key}`) and `{ToLower:…}`, `{ToUpper:…}`, `{UrlEncode:…}`,
`{UrlDecode:…}`. A rewrite to an absolute `http(s)://` URL proxies the
request there.

## Mail (SMTP server)

The SMTP server's configuration is `Settings.mail`. Secrets follow the
usual convention: `smartHost.password` and `dkim[].privateKey` read as
`__SECRET__`; `users[].passwordHash` reads as `__SECRET__` and a user's
`password` is write-only (empty keeps the current one). A DKIM key saved
without `privateKey` gets a new RSA-2048 key; `dnsName` and `dnsRecord`
are the TXT record to publish.

| Method | Path | Role | Body | Response |
|---|---|---|---|---|
| GET | `/api/mail/status` | viewer | | `MailStatus` |
| GET | `/api/mail/queue?state=queued\|failed` | viewer | | `MailMessage[]`, newest first |
| POST | `/api/mail/queue/{id}/retry` | operator | | 204 — a failed message is queued again |
| POST | `/api/mail/queue/retry` | operator | | 204 — every queued message now |
| GET | `/api/mail/queue/{id}/eml` | admin | | `message/rfc822` |
| DELETE | `/api/mail/queue/{id}` | admin | | 204 |
| POST | `/api/mail/test` | admin | `{to, from?}` | 202 `MailMessage` |
| GET | `/api/mail/health?domain=…` | operator | | `MailHealth` — deliverability report (below) |

`GET /api/mail/health` checks this server (public address — `mail.publicIp`
or detected; HELO host name; reverse DNS, forward-confirmed; outbound
port 25; IP blocklists) and each sending domain (the DKIM key domains,
the allowed sender domains and every `domain` parameter): SPF evaluated
for the public address, each DKIM key's published record, DMARC, MX and
the domain blocklist. Each check is `{name, status: pass|warn|fail|info,
detail, record?, fix?, fixDns?}`; with `fixDns`, `fix` is the record to
publish there. Only DNS lookups are made, plus one connection to a
public mail server that is closed after its greeting.

Events: `mail.failed` (a message could not be delivered) and `mail.error`
(the server cannot listen).

## Prometheus

`GET /metrics` on the admin listener (requires a bearer token) exposes
`nodehoster_requests_total{site,code}`, `nodehoster_instance_memory_bytes`, etc.

## Local endpoints (desktop manager)

The server also listens on two local endpoints that do not depend on the
admin listener, its certificate or any NodeHoster account. They are how
NodeHoster Manager (`nodehoster-manager.exe`) works when the web console
does not. HTTP/1.1 over a named pipe on Windows, over a Unix socket in the
data directory elsewhere (`admin.sock`, `status.sock`).

**`\\.\pipe\NodeHoster.Admin`**: the API above, without authentication:
the pipe's security descriptor admits only `SYSTEM` and `BUILTIN\Administrators`
(so the client must run elevated). Requests act with the `admin` role and
are audited as `DOMAIN\user (desktop)` from `local`. Not served here: the
web UI, `/api/auth/*`, `/api/tokens`, webhooks and `/metrics`. One extra
endpoint: `GET /api/local/whoami` → `{account}`. Clients should check that the
process serving the pipe is elevated or LocalSystem before sending secrets
(`internal/localapi` does), since any user can create the pipe while the
service is stopped.

**`\\.\pipe\NodeHoster.Status`**: read-only, also open to interactive users
(for the notification-area icon):

| Method | Path | Response |
|---|---|---|
| GET | `/status` | `{version, startedAt, adminUrl?, adminError?, sites: [{id, name, type, autoStart, state, message?, instances, ready}]}` |
| GET | `/status/stream` | **Server-Sent Events**: `event: summary` (as `/status`) every 3 s; `event: notice` `{time, level, type, site, message}` for crashes, rapid-fail, failed health checks and deployments, certificate problems and unreachable upstreams |

`adminError` is set when the web console could not start (its port is in
use, or its certificate is missing): the server keeps running without it.

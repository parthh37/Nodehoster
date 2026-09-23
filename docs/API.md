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
  `deploy.webhookSecret`, DNS credentials, `acme.eabHmac`, `sso.clientSecret`) are returned as
  `"__SECRET__"` when set. Sending `"__SECRET__"` back leaves the stored value unchanged.
- **Roles**: `viewer` read-only; `operator` may start/stop/restart/deploy; `admin` everything.
  403 when not permitted. A user is either server-wide (one of those roles, on
  the server and every site) or site-scoped; see [Permissions](#permissions).

## Auth

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/auth/login` | `{username, password, totp?}` | `{user}`; 401 `{error, totpRequired: true}` when a TOTP code is needed |
| POST | `/api/auth/logout` | | 204 |
| GET | `/api/auth/me` | | `{user, mustChangePassword, access}` (`access`: the effective `{role, sites?}`, narrowed by the token when called with one) |
| POST | `/api/auth/password` | `{current, new}` | 204 |
| POST | `/api/auth/totp/setup` | | `{secret, url}` (otpauth:// URL for a QR code) |
| POST | `/api/auth/totp/enable` | `{code}` | 204 |
| POST | `/api/auth/totp/disable` | `{code}` | 204 |
| GET | `/api/auth/methods` | | `{password, sso, ssoLabel?}`: how the login page offers sign-in (public) |
| GET | `/api/auth/oidc/start?next=/path` | | 302 to the identity provider (browser navigation, public; see [Single sign-on](#single-sign-on)) |
| GET | `/api/auth/oidc/callback` | | the provider's redirect back: 303 into the console with the session cookie, or to `/login?sso_error=<code>` |

`POST /api/auth/login` answers 403 when single sign-on turned password
sign-in off. A user created by single sign-on has no password: password
sign-in fails for them and `POST /api/auth/password` is refused.

### Single sign-on

`Settings.sso` (`SSOSettings`) configures sign-in with OpenID Connect:
Microsoft Entra ID (issuer `https://login.microsoftonline.com/<tenant ID>/v2.0`;
`common`/`organizations` are refused) or any other provider.

| Field | |
|---|---|
| `enabled` | adds the sign-in button |
| `label` | button text; empty = "Sign in with Microsoft" for Entra ID, else "Sign in with SSO" |
| `issuer` | `https://` (plain HTTP only to this computer); discovery at `<issuer>/.well-known/openid-configuration` |
| `clientId`, `clientSecret` | the app registration; the secret is sealed and masked |
| `scopes` | requested besides `openid profile email` |
| `usernameClaim` | default `preferred_username`; when missing, `preferred_username`, `upn`, `email` (not an `email` with `email_verified: false`) |
| `disablePassword` | refuses password sign-in while `enabled` (break-glass: NodeHoster Manager, and `nodehoster reset-password`, which turns it back on) |
| `autoCreate` | create unknown users (default off: only existing users, matched case-insensitively) |
| `defaultRole` | role of created users when no rule matches (and of existing users, with a mapping); `""` = refuse |
| `roleClaim`, `roleMap` | `[{value, role}]`: with both set, the role is worked out at every SSO sign-in; the first rule whose value (case-insensitive) is in the claim wins; else a site-scoped user keeps their grants; else `defaultRole`; else refused. Rules give server roles only (`admin`, `operator`, `viewer`); the last enabled administrator is never demoted |

The flow is the authorization code flow with PKCE (S256), `state` and
`nonce`. They and the PKCE verifier stay on the server (in memory, 10
minutes, single use); the browser gets an HttpOnly, SameSite=Lax cookie
`nh_sso_<state prefix>` whose hash is stored with them, so a callback only
completes in the browser that started it. The ID token must be signed with
RS256/384/512, PS256/384/512 or ES256/384/512 by a key in the provider's
JWKS (cached; refetched when a token names an unknown key ID), with `iss`
the discovered issuer, `aud` containing `clientId` (and `azp` equal to it
when there are several audiences), `exp`/`iat` within 2 minutes of skew and
the flow's `nonce`. The session is the same as a password sign-in's; local
two-factor authentication is skipped (MFA is the provider's).

`next` must be a path on the console (`/sites/x`); anything else becomes
`/`. Failures are audited as `login.failed` with detail `sso: <reason>`
and counted with the password sign-in's throttle (five per address in 15
minutes); success is audited as `login` with detail `method: sso`, plus
`user.create` / `user.update` when provisioning created the user or changed
their role. `sso_error` codes: `off`, `locked`, `busy`, `provider`, `state`,
`idp`, `token`, `claims`, `unknown_user`, `no_role`, `disabled`, `error`.

## Permissions

Like IIS Manager permissions, a user either has a role on the whole server
or is allowed on selected sites only:

- **Server-wide**: `role` is `admin`, `operator` or `viewer`, and applies to
  the server and to every site.
- **Site-scoped**: `role` is `"sites"` and `sites` lists the grants,
  `[{siteId, role}]` with role `viewer` or `operator`, each site once. There
  is no site-level administrator: a site's configuration (application folder,
  run-as account, bindings, environment) could take over the server or
  another site's host names, so `PUT`/`DELETE /api/sites/{id}`, creating
  sites and everything server-wide need a server `admin`.

Validation (422): grants only with role `sites` and at least one of them;
each on an existing site; `admin` is not a grant role. Grants use site IDs:
renaming a site keeps them, deleting it removes them (from users and from
restricted tokens). Changing a user's role or grants is audited
(`user.update`, detail `access <before> -> <after>`) and applies to their
sessions, tokens and open `/api/stream` connections at once.

What a site-scoped caller gets:

| Endpoints | Result |
|---|---|
| `/api/sites/{id}/...` | authorized against the grant for that site: read routes need `viewer`, actions and deployments `operator`, `PUT`/`DELETE` a server `admin` (403). Sites without a grant answer **404**, like sites that do not exist |
| `GET /api/sites`, `/api/events`, `/api/stream`, `/metrics` | only the granted sites (their status, their events); server-wide events and certificate metrics are left out |
| `GET /api/server/info` | only `version`, `commit` and `hostname` |
| `GET /api/node/versions`, `/api/mime/defaults`, `/api/settings/dns-catalog` | allowed: catalogs the site pages show, nothing server-specific that matters |
| everything else (certificates, Node.js install, settings, mail, users, audit, backup, rewrite import, server metrics) | 403 |
| `/api/auth/*`, `/api/tokens` | their own account, as for anyone |

**Restricted API tokens**: `POST /api/tokens` takes an optional `role` (the
token's maximum role, not above the caller's) and `siteIds` (sites the caller
can access; empty = not restricted to sites). A token restricted to sites is
enforced exactly like a site-scoped user, with its owner's role on each
listed site (at most `operator`, so `role: "admin"` is refused with
`siteIds`). The effective access is always the intersection of the token's
restriction and its owner's current access: downgrading the user, or removing
a grant, downgrades the token too. A restricted token cannot use the account
endpoints (`/api/auth/password`, `/api/auth/totp/*`, `/api/tokens`): 403.

The desktop manager's local pipe always acts as `admin`.

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
| POST | `/api/sites/{id}/cache/purge` | `{path?}` | `{purged}` (operator; empties the response cache, or entries whose path starts with `path`) |

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
| GET | `/api/settings/sso/callback-url` | | `{redirectUrl}`: the redirect URI to register at the provider, from the console's address as the browser reached it (`X-Forwarded-Proto`/`-Host` are honored from this computer and `proxy.trustedProxies`) |
| POST | `/api/settings/sso/test` | `SSOSettings` (may be unsaved) | `{ok, issuer, authorizationEndpoint, tokenEndpoint, jwksUri, keys, redirectUrl, problems: []}`: fetches discovery and the signing keys |
| GET | `/api/users` | | `User[]` (`sites` set for site-scoped users) |
| POST | `/api/users` | `{username, password, role, sites?, sso?}` | `User` (`role: "sites"` with `sites: [{siteId, role}]` for a site-scoped user; `sso: true` creates a single-sign-on-only user without a password) |
| PUT | `/api/users/{id}` | `{role?, sites?, disabled?, password?, resetTotp?}` | `User` (`sites` replaces the grants; a server-wide `role` drops them; `resetTotp: true` turns two-factor off, for a lost authenticator; a `password` makes an SSO user (`sso: true`) an ordinary one; ends the user's sessions) |
| DELETE | `/api/users/{id}` | | 204 |
| GET | `/api/tokens` | | `APIToken[]` (own; `role` = maximum role or omitted, `siteIds` = sites or `null` for unrestricted) |
| POST | `/api/tokens` | `{name, expiresDays?, role?, siteIds?}` | `{token: "nh_…", info: APIToken}` (token shown once; see [Permissions](#permissions) for `role`/`siteIds`) |
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

## Session affinity

`routing.affinity` = `{enabled, cookieName, lifetimeSec}` (saved with
`PUT /api/sites/{id}`; node and proxy sites). Like ARR client affinity, the
first response through a backend sets a cookie (default name `NHAffinity`;
`HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` over HTTPS; `lifetimeSec` 0 =
browser session, otherwise `Max-Age`, renewed after half of it, at most
400 days) and later requests with it go to the same backend, whatever the
strategy: a node site's instance (by slot, so it survives a zero-downtime
recycle), its load-balanced server (and the instance when that is this
server), or a proxy site's upstream. The value is opaque and signed
(HMAC-SHA256 with a per-server key kept sealed in the database): it names
no address or port and cannot be forged or moved to another site. A
cookie that is invalid, or whose backend is unhealthy or gone, is ignored:
the strategy picks and a new cookie is issued. WebSocket upgrades follow it.
No cookie is set by static files, redirects, error pages, or when there is
only one backend. On a request another NodeHoster forwarded
(`X-NodeHoster-Hop`), the cookie is `<name>-hop`, so the front server's
cookie is never overwritten; use distinct names when chaining sites
behind each other in other ways.

## Compression and response cache

`routing.compression` (unchanged boolean) compresses responses with Brotli
(level 4) or gzip, whichever the client's `Accept-Encoding` prefers by
q-value (Brotli on a tie), for text-like types (`text/*`, JSON, JavaScript,
XML, SVG, `+json`/`+xml`, fonts other than woff) of 1 KB or more. Never for
`text/event-stream`, responses that already have a `Content-Encoding` or
`Content-Range`, `Cache-Control: no-transform`, HEAD, range requests or
WebSocket upgrades. Compressible responses always get
`Vary: Accept-Encoding`; a strong `ETag` becomes weak when compressed. For
static files (static sites and static-folder locations) a `file.br` or
`file.gz` next to `file` that is not older than it is sent as it is.

`routing.cache` = `{enabled, maxMemoryMB (64), maxObjectKB (1024),
defaultTtlSec, varyByQuery: "all"|"none"|"listed", queryParams[],
varyHeaders[], bypassPaths[]}` keeps responses of node and proxy sites
(their locations included) in memory, per site: each site has its own
budget, so a busy site cannot evict another's entries, and least recently
used entries are evicted. Static sites are not cached (their files come
from the disk cache, pre-compressed variants included). Only
GET and HEAD requests without `Range`; statuses 200, 203, 301, 404 and 410;
freshness from `s-maxage`, `max-age`, then `Expires` (minus `Age`), else
`defaultTtlSec` (0 = not cached). Not stored: `no-store`, `private`,
`no-cache`, `Vary: *`, `text/event-stream`, responses with `Set-Cookie`,
and answers to requests with `Authorization` or cookies — unless the
response says `public` (a stored `Set-Cookie` is never replayed). The
session affinity cookie counts as neither. Each `Vary` header (and each
of `varyHeaders`) selects a separate variant. Entries are uncompressed:
when the site compresses, the application is asked without
`Accept-Encoding` and each client gets its own encoding from the one
entry. Concurrent misses for a URL wait (up to 10 s) for the first one's
response; URLs whose responses are not cacheable stop waiting for 30 s.
Responses carry `X-Cache: HIT|MISS|BYPASS` and hits an `Age`; conditional
requests on a hit get 304; a request with `Cache-Control: no-cache` (a
browser reload) is fetched and refreshes the entry, one with `no-store`
bypasses the cache. `SiteStatus.cache` = `{entries, bytes, hits,
misses, hitRatio}` when enabled. The cache is emptied when the site's
configuration changes, on recycles and restarts and on deployment
activation. Purge paths match the request path after URL rewrite rules.

## Automatic IP banning

Like fail2ban (or IIS Dynamic IP Restrictions). Server-wide settings in
`Settings.ipBan` = `{enabled, authFailures, notFound, rateLimited,
trapPaths[], banMinutes, maxBanMinutes, allowList[], ipv6Prefix}`; each rule
is `{threshold, windowSec}` (threshold 0 = off; defaults 10 in 300 s, 50 in
60 s, 30 in 60 s). Off by default. What counts, per client address:

- `authFailures`: 401 answers (basic authentication or the application) to
  requests that carried an `Authorization` header or were not GET/HEAD (a
  submitted login form) — a browser merely asked to sign in, or an API call
  without a session, is not counted; failed web console logins. 403 is not
  counted (it is authorization, and what bans and IP restrictions answer).
- `notFound`: 404 answers, including the default page for unbound hosts.
- `rateLimited`: 429 answers (a site's rate limit or the application's).
- `trapPaths` (prefixes, case-insensitive; defaults `/wp-login.php`,
  `/xmlrpc.php`, `/wp-admin`, `/.env`, `/.git/`, `/phpmyadmin`, …): one
  request bans.

A ban lasts `banMinutes` (15), doubling for each further ban of the same
address within a week, up to `maxBanMinutes` (1440). IPv4 addresses are
banned one by one, IPv6 by `ipv6Prefix` (64). Loopback, `allowList` and
`proxy.trustedProxies` are never banned: bans apply to the client address
resolved through trusted proxies. Enforcement is the first thing a request
meets on every listener, before the site's pipeline: a banned client gets a
bare `403 Forbidden` with `Connection: close` (not a closed connection,
which behind a CDN would break a connection shared with other clients).
Sites opt out with `routing.banning` = `{exempt, allowTrapPaths}`: `exempt`
sites answer banned clients and never count; `allowTrapPaths` for sites
that do serve those paths (WordPress, PHP). Counters are in memory, capped
at 100,000 addresses (least recently seen forgotten); bans (and a week of
history for escalation) are saved in the database and survive restarts.
Each ban raises a `security.banned` event (warning; at most 10 a minute,
the rest summarized).

Banned addresses cannot reach the web console either. Loopback never is,
and the desktop manager's pipe is not subject to bans, so an administrator
locked out lifts the ban on the server (NodeHoster Manager › Banned IP
addresses, or a browser on the server itself).

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/bans` | | `Ban[]` in force, newest first (operator: they name client addresses) |
| POST | `/api/bans` | `{address, minutes, reason?}` | `Ban` 201 (admin; an IP or CIDR range, at most /8 or /32 wide; `minutes` 0 = until removed) |
| DELETE | `/api/bans/{address}` | | 204, 404 if not banned (admin; an exact address or range — escape its `/` as `%2F` — or any address inside a banned range) |

`Ban` = `{address, reason, manual, strikes, createdAt, expiresAt?,
createdBy?}`. Server-wide only: site-scoped callers get 403. Manual bans and
unbans are audited (`ban.add`, `ban.remove`).

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
A site-scoped (or site-restricted) token sees only its sites and no
certificate metrics.

## Local endpoints (desktop manager, command line)

The server also listens on two local endpoints that do not depend on the
admin listener, its certificate or any NodeHoster account. They are how
NodeHoster Manager (`nodehoster-manager.exe`), the management commands of
`nodehoster.exe` (`site list`, `deploy`, `logs`...; their `--json` output is
this API's JSON) and the NodeHoster PowerShell module work when the web
console does not. HTTP/1.1 over a named pipe on Windows, over a Unix socket in the
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

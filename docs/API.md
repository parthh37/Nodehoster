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
  `deploy.webhookSecret`, DNS credentials, `acme.eabHmac`, `sso.clientSecret`, the backup passphrase,
  backup destination credentials, the Seq API key and log-shipping headers marked
  `secret`) are returned as
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
sessions and tokens at once; open `/api/stream` connections and a site's
log, deployment log and task run log streams end or narrow within two
seconds (also when the account is disabled).

What a site-scoped caller gets:

| Endpoints | Result |
|---|---|
| `/api/sites/{id}/...` | authorized against the grant for that site: read routes need `viewer`, actions and deployments `operator`, `PUT`/`DELETE` a server `admin` (403). Sites without a grant answer **404**, like sites that do not exist |
| `GET /api/sites`, `/api/events`, `/api/stream`, `/metrics` | only the granted sites (their status, their events); server-wide events and certificate metrics are left out |
| `GET /api/server/info` | only `version`, `commit` and `hostname` |
| `GET /api/node/versions`, `/api/mime/defaults`, `/api/settings/dns-catalog` | allowed: catalogs the site pages show, nothing server-specific that matters |
| everything else (certificates, Node.js install, settings, mail, users, audit, backup and backups, log shipping, server log search, rewrite import, server metrics) | 403 |
| `/api/auth/*`, `/api/tokens` | their own account, as for anyone |

**Restricted API tokens**: `POST /api/tokens` takes an optional `role` (the
token's maximum role, not above the caller's) and `siteIds` (sites the caller
can access; empty = not restricted to sites). A token restricted to sites is
enforced exactly like a site-scoped user, with its owner's role on each
listed site (at most `operator`, so `role: "admin"` is refused with
`siteIds`). The effective access is always the intersection of the token's
restriction and its owner's current access: downgrading the user, or removing
a grant, downgrades the token too. A restricted token cannot use the account
endpoints (`/api/auth/password`, `/api/auth/totp/*`, `/api/tokens`), nor
`PUT /api/users/{id}` on its owner (an `admin`-restricted token): 403.

The desktop manager's local pipe always acts as `admin`.

## Server

| Method | Path | Response |
|---|---|---|
| GET | `/api/server/info` | `ServerInfo` |
| GET | `/api/server/metrics?minutes=60` | `MetricPoint[]` (whole server, 1 point/min) |
| GET | `/api/events?limit=100&siteId=` | `Event[]` newest first |
| GET | `/api/audit?limit=100&offset=0` | `AuditEntry[]` newest first |
| GET | `/api/stream` | **Server-Sent Events**: `event: status` data `SiteStatus[]` every 2s; `event: event` data `Event` as they happen |
| GET | `/api/backup` | JSON file download (sites, certificates metadata, settings); `?format=zip`: a backup archive as the backup settings make it (see [Backups](#backups)) |
| POST | `/api/restore` | a `.json` export or a `.zip` archive: multipart `file` (+ `passphrase` for an encrypted archive), or the raw file as the body with `X-Backup-Passphrase` → `RestoreResult` (was 204 before archives) |

## Sites

`SiteView` = `Site` + `"status": SiteStatus`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/sites` | | `SiteView[]` |
| POST | `/api/sites` | `Site` (id ignored) | `SiteView` 201 |
| GET | `/api/sites/{id}` | | `SiteView` |
| PUT | `/api/sites/{id}` | `Site` | `SiteView` (applied live; node and worker sites are recycled if process settings changed) |
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
| GET | `/api/sites/{id}/logs/search?q=&regex=&source=app\|access&stream=all\|stdout\|stderr\|system&since=&until=&limit=200&cursor=` | | `{lines: LogLine[], truncated, cursor?, scannedBytes}` (viewer on the site; see [Log search](#log-search)) |
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

### Background workers

`type: "worker"` is a managed Node.js process without HTTP (queue consumer,
bot, long-running script). It is configured by `node` like a node site
(script / npm script, instances, restart policy and rapid-fail protection,
recycling on memory / schedule / interval / file change, limits, run-as,
agent) and has deployments, releases and rollback, but:

- no `bindings` (422 `bindings`), no `PORT` variable, and no HTTP-only
  settings: `node.portMode: "fixed"`, `node.healthCheck.enabled`,
  `node.loadBalancer.enabled`, `node.recycle.maxRequests` and routing
  features (locations, rewrites, headers, IP restrictions, basic auth, rate
  limit, maintenance, error pages, HTTPS redirect, HSTS, MIME types) are
  refused with 422 on that field;
- an instance is `ready` once it has stayed up for 2 s (the settle period);
  one that exits sooner failed to start and counts for rapid-fail
  protection;
- recycle starts the new process, then stops the old one gracefully, so for
  a few seconds both run (fine for queue consumers; use restart for a
  singleton that cannot share its work);
- the proxy ignores workers, and a location of kind `site` cannot target one
  (422 `routing.locations[n].siteId`).

### Scheduled tasks

Node and worker sites have `tasks: ScheduledTask[]`, edited with the site
(`PUT /api/sites/{id}`, admin):

| Field | |
|---|---|
| `id` | generated when empty; keeps history across renames |
| `name` | 1-64 characters like a site name, unique in the site (case-insensitive) |
| `schedule` | 5-field cron in **server local time** (`*/15 * * * *`, `0 3 * * mon-fri`, `0 0 1 jan,jul *`; ranges, steps, lists, month and day names, `7` = Sunday; day of month and day of week both restricted = either matches), `@hourly` `@daily` `@weekly` `@monthly` `@yearly`, or `@every <duration>` (`@every 90m`, at least `1m`). Empty = only on demand |
| `script` / `npmScript` | what to run, in the site's active release (one is required) |
| `args` | arguments |
| `enabled` | disabled tasks are not scheduled but can be run on demand |
| `timeoutSec` | default 3600, at most 604800; the whole process tree is killed at the timeout |
| `overlap` | when a run is due while one is still going: `skip` (default; a `skipped` run is recorded), `queue` (runs right after it; at most one waiting), `allow` (concurrently, at most 10) |
| `env` | extra variables (`secret` supported, masked like the site's) |

A run uses the site's Node.js version, environment and secrets, run-as
identity and Job Object limits (on Windows each run has its own job), with
`NODEHOSTER_TASK=<name>` and `NODEHOSTER_TASK_RUN=<run id>` and no `PORT`.
Tasks run whether the site is started or stopped (disable a task to stop
it). Around DST changes a local time that does not exist does not run and a
repeated one runs once. Runs missed while the service was down are not
caught up; runs still in progress when the service stops are asked to stop
gracefully (agent / SIGTERM) within the site's shutdown timeout, and runs
found `running` at startup are marked `failed`. Deleting a site stops its
runs; deleting a task deletes its history. The last 50 runs of each task
are kept, each with a log of up to 10 MB in `logs\sites\<id>\tasks\`.

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/sites/{id}/tasks` | viewer | `TaskView[]`: `ScheduledTask` + `nextRunAt?`, `lastRun?` (in progress, else the last that was not skipped), `running` (run ids), `queued` |
| POST | `/api/sites/{id}/tasks/{task}/run` | operator | 202 `{run: TaskRun \| null, queued}` (`{task}` = id or name); 409 when the overlap policy does not allow a run now |
| POST | `/api/sites/{id}/runs/{run}/cancel` | operator | 202 `TaskRun`; 409 when it is not in progress |
| GET | `/api/sites/{id}/runs?task=&limit=50` | viewer | `TaskRun[]` newest first |
| GET | `/api/sites/{id}/runs/{run}/log?download=true` | viewer | `text/plain` (404 for skipped runs) |
| GET | `/api/sites/{id}/runs/{run}/log/stream` | viewer | SSE `event: log` data string, `event: done` data `TaskRun` |

`TaskRun`: `{id, siteId, taskId, taskName, trigger: schedule|manual, user?,
status: running|succeeded|failed|timeout|cancelled|skipped, startedAt,
finishedAt?, exitCode?, error?}`. Events: `task.failed` (error: non-zero
exit or could not start) and `task.timeout` (warning); audit: `task.run`,
`task.cancel`.

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

## Importing sites (IIS, iisnode, PM2)

Admin only. Nothing is executed and no password is imported: an uploaded
ecosystem file is read as data (see below), and application pool
identities only produce a note.

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/import/preview` | multipart `source` + `file` (+ `name`, `appRoot` for `webconfig`), or JSON `{source, text, filename?, name?, appRoot?}`, or `{source: "local-iis"}` | `ImportPreview`; 422 `{error, field}` when the file cannot be read |
| POST | `/api/import/apply` | `{source?, items: ImportApplyItem[], start}` | `{created: [{key, kind, siteId, name, warning?}], failed: [{key, name, error, field?}]}` |

Sources:

- `iis`: an uploaded `applicationHost.config`; `local-iis`: this server's
  (`%windir%\System32\inetsrv\config\applicationHost.config`, Windows only).
  Each site becomes a draft: bindings (http/https; other protocols, and https
  bindings without a specific host name, are left out; the IIS certificate
  stays in Windows, so https bindings get `certMode: "auto"`), the root
  physical path (`%SystemDrive%`-style variables expanded), `serverAutoStart`.
  What runs it is read from the `<location>` sections for the site and the
  `web.config` in its folder: an iisnode handler (or `httpPlatformHandler`
  starting node.exe) makes a `node` site, `httpRedirect` a `redirect` site, a
  single URL Rewrite rule forwarding everything to another server (ARR) a
  `proxy` site, anything else a `static` site with its default documents,
  directory browsing, MIME maps and rewrite rules. Virtual directories and
  static applications become `static` locations; an iisnode application
  below a site becomes its own `node` site without bindings, mounted with a
  location of kind `site` (`siteId: "import:<key>"` until applied). Sites
  running ASP.NET Core, PHP or other handlers are noted and not selected.
- `webconfig`: one iisnode application's `web.config`: the handler's script,
  `<iisnode>` (`nodeProcessCountPerApplication` → instances, 0 = CPU count;
  `nodeProcessCommandLine` → Node.js version from the path and options;
  `watchedFiles` → watch files; `gracefulShutdownTimeout`; `node_env`;
  others are listed as ignored), `<appSettings>` → variables (names looking
  like keys, secrets, passwords, tokens or connection strings, and URLs with
  a password, become secrets; `WEBSITE_NODE_DEFAULT_VERSION` → Node.js
  version) and URL Rewrite rules through the rule importer, without the
  iisnode rules that only route to the entry script.
- `pm2`: `ecosystem.config.js|.cjs|.json` or `pm2 jlist` / `pm2 prettylist`
  output. JavaScript is never run: a file that exports a plain literal
  (`module.exports = {...}`, `export default`, comments, trailing commas,
  bare keys, strings without `${}`) is parsed; anything computed
  (`process.env.X`, variables, `require`) is refused with a hint to import
  `pm2 jlist > apps.json` instead. Mapped: name, script (`npm`/`yarn run x`
  → npm script), cwd, args, node_args, interpreter (Node.js version from an
  nvm path; other interpreters are not selected), instances (`max`/0 = CPU
  count, -n = CPUs − n), env merged with env_production (`PORT` dropped: it
  is assigned), max_memory_restart, cron_restart (a daily `M H * * *` →
  recycle time), watch/ignore_watch, autorestart/max_restarts/min_uptime,
  kill_timeout, listen_timeout. `pm2 jlist` values that are PM2's defaults
  and the machine's own variables are left out. An app without a port whose
  name looks like a worker is proposed as a `worker`; one with
  `autorestart: false` and `cron_restart` as a scheduled task, of the app in
  the same folder when there is one.

`ImportPreview` is `{source, items, warnings}`; each `ImportItem` is `{key,
source, options: [{label, kind: site|task, site?, task?, taskSite?}],
choice, selected, notes: [{level: converted|approximated|skipped, text}],
conflicts}`. Conflicts (name taken, binding in use, invalid draft) are
checked like a save against the existing sites and the selected items
before; items with conflicts are not selected.

Apply takes the reviewed options (`ImportApplyItem` = `{key, kind, site?,
task?, taskSite?}`; `taskSite` is `import:<key>` or an existing node or
worker site id). It creates sites that others mount first, never overwrites
a site (a taken name fails that item), adds tasks to their site, and leaves
the sites stopped unless `start` is true. Each creation is audited
(`site.import`; a task added to a site: `site.update`).

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
response, until its headers show it will not be stored (an event stream,
`no-store`, over `maxObjectKB`); URLs whose responses are not cacheable
stop waiting for 30 s.
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
  requests that tried a password: `Authorization: Basic …`, or a submitted
  form (a POST of `application/x-www-form-urlencoded` or
  `multipart/form-data`) — a browser merely asked to sign in, an expired
  bearer token, an API call or heartbeat without a session, or an `OPTIONS`
  preflight is not counted; failed web console logins. 403 is not counted
  (it is authorization, and what bans and IP restrictions answer).
- `notFound`: 404 answers, including the default page for unbound hosts.
- `rateLimited`: 429 answers (a site's rate limit or the application's).
- `trapPaths` (prefixes, case-insensitive; defaults `/wp-login.php`,
  `/xmlrpc.php`, `/wp-admin`, `/.env`, `/.git/`, `/phpmyadmin`, …): one
  request bans.

A ban lasts `banMinutes` (15), doubling for each further ban of the same
address within a week, up to `maxBanMinutes` (1440). IPv4 addresses are
banned one by one, IPv6 by `ipv6Prefix` (64). Loopback, `allowList` and
`proxy.trustedProxies` are never banned: bans apply to the client address
resolved through trusted proxies (the right-most `X-Forwarded-For` entry
that is not a trusted proxy; `ip:port` and `[v6]:port` entries are read,
and an entry that cannot be read resolves to the proxy itself rather than
to anything the client wrote left of it). Enforcement is the first thing a request
meets on every listener, before the site's pipeline: a banned client gets a
bare `403 Forbidden` with `Connection: close` (not a closed connection,
which behind a CDN would break a connection shared with other clients).
Sites opt out with `routing.banning` = `{exempt, allowTrapPaths}`: `exempt`
sites answer banned clients and never count; `allowTrapPaths` for sites
that do serve those paths (WordPress, PHP). Counters are in memory, capped
at 100,000 addresses (least recently seen forgotten); bans (and a week of
history for escalation, at most 20,000 expired bans) are saved in the
database and survive restarts. At most 10,000 automatic bans are in force:
past that, the ones that expire soonest are lifted, a thousand at a time
(an IPv6 /48 alone is 65,536 /64s); manual bans are never lifted.
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

## Backups

Scheduled backups copy an archive to one or more destinations. Settings are
`Settings.backup` (`BackupSettings`), admin only:

| Field | |
|---|---|
| `enabled`, `time` (`"HH:MM"`, server local time), `weekdays` (0 = Sunday … 6; empty = every day) | schedule; a time missed while the service was stopped is not caught up |
| `keepLast`, `keepDays` | retention per destination, applied after each upload to this server's archives only; an archive is kept if either rule keeps it, the newest always; both 0 = keep everything |
| `includeCertificates` (default on), `includeShared`, `sharedSiteIds` (empty = all) | contents besides the configuration: certificate PEMs and keys, `sites/<id>/shared` folders |
| `passphrase` | secret; encrypts archives and makes them restorable on another server |
| `destinations[]` | `{id, name, type, enabled, folder \| s3 \| azure \| sftp}` |

Destination sections (secrets in *italics* read as `__SECRET__`):
`folder: {path}` (local or UNC); `s3: {endpoint?, region, bucket, prefix?,
accessKeyId, *secretAccessKey*, pathStyle}` (endpoint empty = AWS);
`azure: {account, container, prefix?, *sasToken*?, *accountKey*?, endpoint?}`;
`sftp: {host, port, username, *password*?, *privateKey*?, *passphrase*?,
directory, hostKey}` — `hostKey` (`SHA256:…`) is required and checked on
every connection.

**Archive**: `nodehoster-backup-<host>-<yyyyMMdd-HHmmss>.zip` (UTC) with
`manifest.json` (format, version, host, created, contents, SHA-256 of every
file), `backup.json` (the `GET /api/backup` export), `certs/<id>/cert.pem`,
`certs/<id>/key.pem.sealed` and `sites/<id>/shared/…`. Without a passphrase
secrets and keys stay sealed with this server's DPAPI-protected master key,
so the archive only restores on this machine. With one, the archive is a
zip of a public `manifest.json` and `payload.enc`: the archive above,
encrypted with AES-256-GCM in 64 KiB chunks under a scrypt-derived key (salt
and parameters in its header), holding `key.pem` in place of the sealed
key and `secrets.json` (every secret in plain text) so another server
re-seals them with its own key. S3 uploads are single requests (archives up
to 5 GiB); Azure uses blocks above 64 MiB.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/backups` | | `BackupStatus` `{enabled, running, runningSince?, runningWhat?, nextRun?, encrypted, hostname, history: BackupRun[]}` (last 50 runs, newest first) |
| POST | `/api/backups/run` | | 202 `BackupStatus`; 409 while a backup or restore runs; 503 while the service stops |
| POST | `/api/backups/test` | `BackupDestination` (masked secrets are taken from the saved destination with the same `id`) | `{ok, error?, hostKey?}` — lists, writes and deletes a small file. SFTP without a matching `hostKey` connects no further and returns the server's `hostKey` to confirm |
| GET | `/api/backups/shared-sizes` | | `[{siteId, siteName, bytes, files, partial}]` (bounded walk; `partial` = stopped counting) |
| GET | `/api/backups/destinations/{id}/files` | | `[{name, size, modified, host, created}]` — archives from every server, newest first |
| POST | `/api/backups/destinations/{id}/restore` | `{file, passphrase?}` | `RestoreResult` |

`BackupRun` = `{id, trigger: schedule|manual, startedAt, finishedAt, status:
success|partial|failed, error?, file, size, encrypted, contents[],
destinations: [{id, name, ok, error?, pruned}]}`.

`RestoreResult` = `{format: json|archive, hostname?, created?, encrypted,
sites, certificates, sharedSites[], warnings[]}`. A restore replaces the
settings and the sites in the backup (others are kept; a backup without
`settings.backup` keeps the current backup settings); secrets sealed with
another server's key are re-sealed from `secrets.json`; lacking that, they
keep this server's current value for the same setting (sites, destinations
and other list entries matched by ID, or name for entries without one; a
backup destination also by type, location and account) or are cleared, and
every such field is named in `warnings`; certificate files are written only for certificates this server
lacks or has no files for; each restored shared folder is unpacked beside
the current one, the site is stopped if running, the current folder is kept
as `shared.pre-restore` and the site is started again. Entry names are
checked before anything is written (only the layout above, no `..`,
absolute paths or links) and every file's checksum is verified. An
encrypted archive without `passphrase` (or with a wrong one) is a 422 with
`field: "passphrase"`.

Events: `backup.completed` (info), `backup.failed` (error: no destination,
or not every destination, received the archive).

## Log shipping

`Settings.logShipping.targets[]` (`LogTarget`), admin only:
`{id, name, type: syslog|seq|http, enabled, sources[], siteIds[], minLevel,
syslog | seq | http}`. `sources` is any of `server` (NodeHoster's log, from
`minLevel`: debug, info, warning, error), `app` (sites' stdout, stderr and
system lines), `access` (requests of sites with access logging on),
`event` and `audit`. `siteIds` (empty = all) limits the records that
belong to a site (app, access, site events); the server log, audit log and
server events are chosen by `sources` alone.

- `syslog: {address: "host:port", transport: udp|tcp|tls, facility (user,
  daemon, local0…local7; default local0), appName, hostname, caCert?,
  insecureSkipVerify}` — RFC 5424; MSGID is the source; site, siteId,
  instance, stream and access fields are structured data
  `[nodehoster@32473 …]`; severity from the level (stderr is warning, or
  error when the line looks like one; access 4xx warning, 5xx error). TCP
  and TLS use octet counting; UDP messages are cut at 8 KiB.
- `seq: {url, apiKey}` — CLEF (`application/vnd.serilog.clef`) posted to
  `{url}/api/events/raw?clef` with `X-Seq-ApiKey`; properties Site, SiteId,
  Instance, Stream, Source and, for requests, RequestMethod, RequestPath,
  StatusCode, Bytes, Elapsed, ClientIp, Host, UserAgent.
- `http: {url, format: json|ndjson, headers: [{name, value, secret}]}` —
  batches of records `{time, source, level, message, siteId?, site?,
  instance?, stream?, access?: {method, path, status, bytes, durationMs,
  clientIp, host, userAgent, referer}, attrs?}`, as a JSON array or one
  per line. A header's value is masked when `secret` is set; turning
  `secret` off requires sending the value again (422 on the value field
  when it is still the mask).

Shipping never blocks logging: each target has a queue of 10,000 records
or 16 MiB (the oldest are dropped when it is full, and counted), batches of
up to 500 records or 1 MiB sent at least every second, and failed batches
retried with exponential
backoff (0.5 s doubling to 30 s, six attempts; 4xx other than 408/429 is
not retried). The shipper's own errors go to the server log file only,
at most once a minute per target.
Long fields are cut, ending in `…[truncated]`: the message at 16 KiB, the
request's method, path, host, user agent and referer and each attribute at
4 KiB; beyond 64 attributes the rest are left out and `truncated: "true"`
is added.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/logshipping/status` | | `[{id, name, type, enabled, queued, sent, dropped, failed, lastError?, lastErrorAt?, lastSuccess?}]` |
| POST | `/api/logshipping/test` | `LogTarget` (masked secrets are taken from the saved target with the same `id`) | 204, or 502 `{error}` with the collector's answer |

## Log search

Searches read the current file backwards, then the rotated copies
(`app-<time>.log`, also `.gz`), newest first, stopping after 3 seconds or
256 MB read with `truncated: true`. `cursor` continues after the last line
returned (at the limit or the budget); a cursor survives a rotation.
Parameters: `q` (case-insensitive text, or an RE2 regular expression of at
most 512 characters with `regex=1`; matched against the message, not the
timestamp prefix), `since` / `until` (RFC 3339, or a duration back from
now such as `15m`, `24h`), `limit` (default 200, at most 1000), `cursor`.

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/sites/{id}/logs/search?source=app\|access&stream=…` | viewer on the site | `{lines: LogLine[], truncated, cursor?, scannedBytes}` |
| GET | `/api/server/logs/search?level=warning` | admin | same; `LogLine.s` is the line's level, `m` the whole line |

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

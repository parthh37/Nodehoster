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
  `deploy.webhookSecret`, `deploy.previews.statusToken`, DNS credentials, `acme.eabHmac`, `sso.clientSecret`, the backup passphrase,
  backup destination credentials, the Seq API key, log-shipping headers marked
  `secret` and the tokens of server connections) are returned as
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
seconds (also when the account is disabled, the API token they were
opened with is deleted or expires, or the session is signed out, revoked
or expires).

What a site-scoped caller gets:

| Endpoints | Result |
|---|---|
| `/api/sites/{id}/...` | authorized against the grant for that site: read routes need `viewer`, actions and deployments `operator`, `PUT`/`DELETE` a server `admin` (403). Sites without a grant answer **404**, like sites that do not exist |
| `GET /api/sites`, `/api/events`, `/api/stream`, `/metrics` | only the granted sites (their status, their events); server-wide events and certificate metrics are left out |
| `GET /api/server/info` | only `version`, `commit` and `hostname` |
| `GET /api/node/versions`, `/api/runtimes`, `/api/mime/defaults`, `/api/settings/dns-catalog` | allowed: catalogs the site pages show, nothing server-specific that matters |
| everything else (certificates, Node.js and runtime installs, settings, mail, users, audit, backup and backups, updates, log shipping, secret stores, server log search, rewrite import, server metrics) | 403 |
| `/api/auth/*`, `/api/tokens` | their own account, as for anyone |
| `/api/servers/*` | 403: a connection is the whole of another server |

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

A site's [preview deployments](#preview-deployments) come with it: a grant
on a site (or a token restricted to it) gives the same role on each of its
previews, which appear in `GET /api/sites` and answer `/api/sites/{previewId}/...`
for that caller.

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

`metrics`, `logs`, `logs/stream`, `logs/download` and `logs/search` take
`?slot=production|<name>` for a [deployment slot](#deployment-slots): its
metrics, its access log, or only the application lines its instances wrote
(`LogLine.slot`; without `slot`, every slot's lines).

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
A push to the site's branch (or to any branch when none is set) deploys it:
202 `Deployment`; a push to another branch answers 200 `{status: "ignored",
reason}`. Pull request (merge request) events and branch deletions are for
[preview deployments](#preview-deployments) and never deploy the site itself.

`?slot=<name>` on `deploy/zip`, `deploy/git` (or `slot` in its body),
`deployments/{depId}/activate` and the webhook URL targets a
[deployment slot](#deployment-slots) instead of production (422 `slot` for
a slot the site does not have). `Deployment.slot` records it;
`GET .../deployments?slot=production|<name>` lists one slot's history. Any
successful release can be activated into any slot.

### Runtimes

Node and worker sites run with the runtime in `node.runtime` (the object
keeps its name whatever the runtime; absent on sites saved before runtimes
existed, which are Node.js, and set to `"node"` on the next write):

| Field | |
|---|---|
| `runtime` | `node` (default) \| `bun` \| `deno` \| `python` \| `dotnet` \| `custom` (422 `node.runtime` otherwise) |
| `runtimeVersion` | `bun`, `deno`: an installed version (`1.1.30`); `python`: a version (`3.12`, the newest 3.12.x found) or the full path of `python.exe`; `dotnet`: the full path of `dotnet.exe`; `""` = the server default (`settings.runtimes`). Ignored for `node` (it has `nodeVersion`) and `custom` |
| `script` | the entry: a script (node, bun, deno, python), the app's `.dll` (run by `dotnet`) or a self-contained `.exe` (dotnet), the program (custom: a full path, relative to the application folder, or on PATH) |
| `npmScript` | a package script: `npm run` (node), `bun run` (bun), `deno task` (deno); refused for the others (422 `node.npmScript`, also `tasks[n].npmScript`) |
| `nodeArgs` | the runtime's own arguments: node or bun flags, `deno run` flags (permissions: Deno grants nothing by default; the consoles start a Deno site with `--allow-net --allow-env --allow-read`, the API adds none), Python interpreter options, `dotnet` host options |
| `python` | `{module?, server?: "uvicorn"\|"hypercorn"\|"waitress", app?: "main:app", venv}`: python sites set exactly one of `script`, `python.module` or `python.server` with `python.app` (422 `node.script`, `node.python.*`); a worker cannot run a server. `venv` (default `.venv`, relative to the application folder) is used when it exists |
| `agentEnabled` | honored for `node` and `bun` entry scripts only |

Every instance gets `PORT`; `dotnet` sites also `ASPNETCORE_URLS=http://127.0.0.1:<port>`
(overriding the site's variables), and Python servers are started with
`--host 127.0.0.1 --port` (uvicorn), `--bind 127.0.0.1:<port>` (Hypercorn)
or `--listen=127.0.0.1:<port>` (Waitress). Python processes get
`PYTHONUNBUFFERED=1` and `PYTHONUTF8=1` (and `VIRTUAL_ENV`), Deno processes
the site's `DENO_DIR`. Processes without an agent are stopped with a console
Ctrl+Break on Windows (SIGTERM elsewhere), then killed after
`shutdownTimeoutSec`. `InstanceStatus` gains `runtime`, `runtimeVersion`
(what started the process, every runtime) and `agent` (an agent reports
`heapUsedBytes` / `eventLoopLagMs`: Node.js, Bun).

Deployments default `deploy.installCommand` per runtime when it is empty
(`npm ci --omit=dev`, `bun install --production`, `deno install`,
`python -m pip install -r requirements.txt`, none for dotnet and custom); a
python release gets its own virtual environment (`python -m venv`) before
the install command, which runs with it first on `PATH`.

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/runtimes` | any signed-in user (a catalog) | `{bun: Managed, deno: Managed, python: Interpreter[], dotnet: {host, runtimes: [{name, version, path}]} \| null, defaults: {bun?, deno?, python?, dotnet?}}`; `Managed` = `{system: {version, path} \| null, installed: [{version, path, status, progress, error?, isDefault}]}` like `/api/node/versions`; `Interpreter` = `{version, path, source: py\|path\|folder, isDefault}`. Detection is cached for a minute |
| POST | `/api/runtimes/refresh` | admin | the same, detected again now |
| GET | `/api/runtimes/{bun\|deno}/available` | viewer | `[{version, date}]` stable releases for this platform with a published SHA-256, newest first (GitHub; cached an hour) |
| POST | `/api/runtimes/{bun\|deno}/versions` | admin | `{version}` → 202; downloaded and verified in the background (events `runtime.installed` / `runtime.failed`; audit `runtime.install`) |
| DELETE | `/api/runtimes/{bun\|deno}/versions/{version}` | admin | 204; 409 if a site pins it or it is the server default (audit `runtime.remove`) |

Python and .NET are found, never installed (404 for `/api/runtimes/python/...`).
Server defaults are `settings.runtimes` (`PUT /api/settings`; 422
`runtimes.<runtime>` for a value a site could not use).

### Background workers

`type: "worker"` is a managed process without HTTP (queue consumer,
bot, long-running script), in any runtime. It is configured by `node` like a node site
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

A run uses the site's runtime and version (a python site's virtual
environment; never its ASGI/WSGI server), environment and secrets, run-as
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

### Preview deployments

A node or static site deployed from git can get a temporary site per pull
request (GitLab: merge request) or per branch: `deploy.previews`
(`PreviewConfig`), edited with the site (`PUT /api/sites/{id}`, admin):

| Field | |
|---|---|
| `enabled` | needs `deploy.git.repo`, the production branch `deploy.git.branch` (never previewed) and `deploy.webhookSecret` (422 on that field otherwise) |
| `hostPattern` | `pr-{number}.preview.example.com`, `{branch}.preview.example.com`: placeholders in the first label only. `{branch}` is the branch made DNS-safe (lower case, `-` for anything else, at most 63 characters with a hash of the full name when shortened); a name another site uses gets a hash of the preview's key appended. A branch preview under a pattern without `{branch}` takes the branch as its first label |
| `pullRequests` | opened, reopened, pushed to (`synchronize`; GitLab `update` with new commits): deployed; closed or merged: deleted |
| `branches` | globs of branches whose pushes get a preview (`*` within a path segment, `**` across, `?`); a push deleting the branch (or a `delete` event) deletes it |
| `allowForks` | default false: pull requests from forks are ignored (their code would run on the server). When allowed they are fetched through the base repository's pull request ref (`refs/pull/N/head`, GitLab `refs/merge-requests/N/head`) |
| `maxPreviews` | default 10, at most 100. A new preview beyond it **evicts** the preview pushed to least recently (`preview.deleted`, reason `evicted`) |
| `expireDays` | delete previews without a push for that many days (checked hourly); 0 = never |
| `protocol`, `ip`, `port` | the preview's one binding (default http, port 80/443) |
| `certMode` | https: `auto` (a Let's Encrypt certificate per preview host over HTTP-01, deleted with the preview), `certificate` + `certificateId` (a certificate from the store that covers `*.<suffix>`), `wildcard` + `dnsProviderId` (the store's certificate for `*.<suffix>`, else one requested through DNS-01 and kept for the next previews) |
| `env` | overrides of the site's variables (`secret` supported, masked like the site's); `PREVIEW=1`, `PREVIEW_BRANCH`, `PREVIEW_PR` (the number, empty for a branch) and `PREVIEW_URL` are always set and cannot be overridden |
| `basicAuth`, `allowIps` | when enabled / not empty, replace the site's basic authentication / IP allow list in previews |
| `reportStatus`, `statusToken` | set a commit status (`nodehoster/preview`: pending, then success with the preview's URL or failure) on GitHub (and Enterprise: `https://<host>/api/v3`), GitLab or Gitea (at the root of their host). The API address comes from `deploy.git.repo`, never from the webhook; `statusToken` (secret) defaults to `deploy.git.token` |

A preview is a site with `previewOf: <parentId>` and `preview: PreviewInfo`
(`{key, kind: pr|branch, number?, branch, ref, commit?, title?, author?,
prUrl?, fork?, provider?, host, url, lastPush, ready?}`), both maintained by
the server (ignored in `POST`/`PUT /api/sites`). Its configuration is the
parent's, made again at every deployment, with: its binding, one instance
on an automatic port, no load balancing, maintenance mode and HTTPS redirect
off, `keepReleases` 1, no scheduled tasks, no webhook of its own, and an
absolute application path replaced by its release. Shared paths are its own
(`sites\<previewId>\shared`), never the parent's. It starts after its first
successful deployment (and cannot be started before: 400). Operations on one
preview are serialised; pushes that arrive during a deployment collapse
into one more deployment of the latest head. Deleting a preview removes its
site, releases, logs and automatic certificate; deleting the parent
deletes its previews.

| Method | Path | Role | Body | Response |
|---|---|---|---|---|
| GET | `/api/sites/{id}/previews` | viewer | | `PreviewView[]`, pushed to most recently first: `{id, name, preview: PreviewInfo, state: pending\|deploying\|ready\|failed\|deleting, siteState, lastDeployment?, createdAt}` |
| POST | `/api/sites/{id}/previews` | operator | `{branch}` | 202 `{action, key, reason}`: deploys the branch as a preview (created if needed), whatever `branches` says; 422 for the production branch or an invalid name |
| POST | `/api/sites/{id}/previews/{previewId}/redeploy` | operator | | 202 `PreviewView` |
| DELETE | `/api/sites/{id}/previews/{previewId}` | operator | | 202 `PreviewView` (deleted in the background) |

Webhook answers for these deliveries: 202 `{status: "accepted", action:
deploy|delete, preview: <key>, reason}`, or 200 `{status: "ignored", reason}`
(previews off, a fork, a pull request event that changes no code, closing
one without a preview, an unacceptable branch name). The host is told by
`X-GitHub-Event`, `X-Gitlab-Event` or `X-Gitea-Event` (also Gogs and
Forgejo); a delivery without one is a push as before. Events on the parent
site: `preview.created` (first deployment succeeded; message with the URL),
`preview.updated`, `preview.deleted` (with the reason: closed, merged,
branch deleted, expired, evicted, deleted by a user), `preview.failed`.
Audit: `preview.deploy` / `preview.delete` by `webhook`, `preview.create`,
`preview.redeploy`, `preview.delete`. Deployments of previews have
`source: "preview"`.

### Deployment slots

Like Azure App Service deployment slots: node and worker sites have
`slots: DeploymentSlot[]` (at most 4 besides production, edited with the
site, admin), each running its own release on its own instances, with its
own bindings, so a release can be tested (staging.example.com) before it
goes live, then swapped in without a cold start.

| Field | |
|---|---|
| `name` | 1-32 lower-case letters, digits or `-`, not `production`, unique |
| `env` | the slot's own variables (`secret` supported, masked like the site's): they replace production's of the same name, or are added |
| `instances` | 0 (default) = as many as production |
| `autoSwap` | after a successful deployment to the slot, swap it into production (audited as `auto-swap`) |
| `warmup` | `{paths: ["/"], statuses: "200-399", timeoutSec: 120}`: `statuses` are ranges and codes (`200-299,401`), `timeoutSec` 5-1800 for the whole warm-up |
| `activeRelease` | the release the slot runs; NodeHoster's to manage (ignored on write, like `activeRelease`) |

Settings: a slot runs with production's configuration except its **slot
settings**, which stay with the slot on a swap: its `env` and instance
count, production's variables marked `slotSetting: true` (`node.env[n]`:
they stay in production and slots do not get them — the database URL, say)
and bindings: `Binding.slot` names the slot a binding routes to (`""` =
production; 422 `bindings[n].slot` for a slot that does not exist). Bindings
never move on a swap, like Azure's custom domains. Deployments to a slot
are built with the slot's variables; shared paths (`deploy.sharedPaths`)
are shared by every slot. A slot needs automatic ports (422
`node.portMode` with a fixed port) and is only served by this server's
instances, never load balanced to other servers. Scheduled tasks run in
production only, from production's release. A slot starts when something
is deployed to it (and with the service, for sites that start
automatically); it is started, stopped and recycled on its own. Its
events, logs (`LogLine.slot`, `[staging 0 stdout]` in `app.log`), access
log (`logs\sites\<id>\<slot>-access.log`), metrics and traffic are its own;
rapid-fail protection, recycling, health checks and file watching apply to
each slot's instances separately.

A **swap** exchanges a slot's release with production's:

1. *preparing*: the slot's instances are restarted with production's
   settings, sticky ones included (a rolling recycle, only if something
   differs), and scaled to production's instance count — like Azure
   applying the target slot's settings to the source slot first. A stopped
   slot is started.
2. *warming*: every warm-up path is requested on every instance (with
   production's host name, `X-Forwarded-Proto` and
   `User-Agent: NodeHoster-Warmup`, redirects not followed) until it answers
   an accepted status, retrying every second; a worker site has nothing to
   warm up.
3. *swapping*: the releases change places in the stored configuration
   (a restart comes back with the swap done), then production's traffic
   moves onto the warm instances in one step. Requests in flight on the old
   production instances finish there; those instances become the slot and
   are recycled onto the slot's settings. The response caches of both are
   emptied. Session affinity keeps clients on the same instance number;
   what a process kept in memory does not survive, as with a recycle.

A failure before step 3 (an instance that does not start, a warm-up that
times out, the service stopping) changes nothing: the slot goes back to its
own settings (and is stopped again if the swap started it). Swapping again
is the rollback. While a swap runs, the site's configuration, deployments,
rollbacks, start/stop/restart/recycle and deletion answer 409, and a
deployment in progress makes a swap answer 409. Production must be running
(or stopped by rapid-fail protection); the slot must have a release (or be
running: a slot without one runs production's application folder, as after a
swap from a site that had never been deployed).

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/sites/{id}/slots` | viewer | `SlotsView`: `{slots: SlotStatus[], swap?: SwapProgress, lastSwap?: SwapResult}`, production first |
| GET | `/api/sites/{id}/slots/{slot}/swap` | viewer | `SwapPreview`: `{slot, productionRelease, slotRelease, warmup, changes: string[], warnings: string[], blockers: string[]}` |
| POST | `/api/sites/{id}/slots/{slot}/swap` | operator | 202 `SwapProgress`; the swap goes on in the background. 422 `slot` with the first blocker, 409 while a deployment or swap runs |
| POST | `/api/sites/{id}/slots/{slot}/start` | operator | `SlotsView` (a slot without a release runs production's application folder) |
| POST | `/api/sites/{id}/slots/{slot}/stop` | operator | `SlotsView` |
| POST | `/api/sites/{id}/slots/{slot}/recycle` | operator | `SlotsView` |

`{slot}` = `production` acts on the site itself for start, stop and recycle.
`SlotStatus`: `{name, release?, status: SiteStatus, bindings, autoSwap?}`.
`SwapProgress`: `{slot, phase: preparing|warming|swapping, message?, user?,
auto?, startedAt}`. `SwapResult` (the last swap, kept until the service
restarts): `{slot, succeeded, message, user?, auto?, startedAt, finishedAt,
productionRelease?, slotRelease?}` (releases after the swap). Events:
`slot.swapped` (info) and `slot.swap_failed` (error), also on the status
pipe; audit: `site.slot.swap`, `site.slot.start|stop|recycle`, and
deployments and rollbacks to a slot as `site.deploy` / `site.rollback` with
`to <slot>` in the detail.

## Certificates

`CertificateView` = `Certificate` + `"usedBy": [{siteId, siteName, binding}]` +
`"ocsp": OCSPStatus` (absent while the certificate is not issued; see
[OCSP stapling](#ocsp-stapling)).

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/certificates` | | `CertificateView[]` |
| GET | `/api/certificates/{id}` | | `CertificateView` |
| POST | `/api/certificates/acme` | `{name, domains[], acme: ACMEOptions, autoRenew}` | `CertificateView` 202 (status `pending`, issued asynchronously) |
| POST | `/api/certificates/import` | multipart: `file` (.pfx/.p12 or .pem/.crt), optional `keyFile`, `password`, `name` | `CertificateView` |
| POST | `/api/certificates/selfsigned` | `{name, domains[], validDays}` | `CertificateView` |
| POST | `/api/certificates/{id}/renew` | | `CertificateView` 202 |
| POST | `/api/certificates/{id}/ocsp` | | `CertificateView` (operator; asks the OCSP responder now, audited `cert.ocsp`) |
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
| GET | `/api/tls` | | `{minVersion, http2, http3, http3Listeners: []}` (viewer; see [HTTP/3](#http3)) |
| PUT | `/api/tls` | `{minVersion, http2, http3}` | same (admin; only the TLS settings change, audited `settings.tls`) |
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
  running ASP.NET Core, PHP or other handlers are noted and not selected; so
  are those that only look like they run code through IIS's server-wide
  handlers (an application pool with a .NET CLR version — IIS's default is
  v4.0 —, `<system.web>` `compilation`/`httpRuntime`/`machineKey`,
  `<connectionStrings>`, a managed handler, `.aspx`/`.php`/`.asp`… default
  documents or pages, `bin\*.dll`, `App_Data`, `App_Code` in the folder
  when it is readable); such applications below a site are left out rather
  than becoming static locations. Imported sites that serve files get
  `routing.unknownMimeTypes: "deny"`, as IIS refuses extensions without a
  MIME map, and static files never include `web.config`.
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
(`site.import`; a task added to a site: `site.update`). Passwords are never
imported, so Node.js and worker drafts have no Run as: they would run as
the NodeHoster service account (LocalSystem) where IIS used a low-privilege
pool identity and PM2 the user who started it. Their preview carries an
`approximated` note saying so, and each such created site a `warning` in
the result (after "could not be started", when that happened too); `start`
still starts them, as it would a site created by hand.

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

## Client certificates (mutual TLS)

Like IIS "SSL Settings" › Client certificates. An `https` binding may have
`clientCert` = `{mode, caPem, allowedSubjects[], allowedFingerprints[],
requirePaths[]}` (absent = ignore, as before):

- `mode`: `ignore` (default) | `accept` (clients may present a certificate)
  | `require` (the TLS handshake fails without a certificate issued by the
  CAs; like IIS "Require").
- `caPem`: the trusted issuing CAs, PEM (at least one certificate, nothing
  else, at most 100 and 256 KB). CA certificates are public and stored as
  they are. A self-signed client certificate may be listed to trust exactly
  that certificate. Required unless `mode` is `ignore` (422
  `bindings[i].clientCert.caPem`).
- `allowedSubjects`, `allowedFingerprints` (optional): when either is set,
  a verified certificate must also match one entry: the subject common name,
  the whole subject DN (as in `X-Client-Cert-Subject`) or a DNS, email or
  URI subject alternative name, ignoring case; or its SHA-256 fingerprint
  (hex; colons and spaces are removed, stored upper-case).
- `requirePaths` (with `accept`): path prefixes (`/admin` covers `/admin`
  and `/admin/...`) answered 403 without a valid certificate. TLS 1.3 has no
  renegotiation, so a per-path requirement is a certificate accepted at the
  TLS level and enforced per request.

Bindings sharing an IP address and port have their own policies: the
handshake asks for a certificate as the binding its SNI name selects says
(a binding without a host name is the default, also for clients that send
no SNI). Each request is checked against the binding its Host header
selects; if that is another policy (a connection reused for another host,
or a Host unlike the SNI name), the answer is `421 Misdirected Request`,
which browsers retry on a new connection. With `accept`, a certificate
that does not verify never fails the handshake. Verification (chain to
`caPem`, client-authentication usage, validity, allow lists) is cached per
certificate for 5 minutes.

The application receives, on every request of such a binding:

| Header | Value |
|---|---|
| `X-Client-Verify` | `SUCCESS`, `NONE` (no certificate) or `FAILED:<reason>` (`unknown issuer`, `certificate expired or not yet valid`, `certificate not for client authentication`, `certificate not allowed`, `certificate invalid`) |
| `X-Client-Cert` | the certificate, URL-escaped PEM (like nginx `$ssl_client_escaped_cert`; `decodeURIComponent` reads it) |
| `X-Client-Cert-Subject` | subject DN, RFC 2253 (`CN=device-1,O=Example`) |
| `X-Client-Cert-Fingerprint` | SHA-256, upper-case hex (as the certificate store shows fingerprints) |

The last three only on `SUCCESS`. Copies of all four sent by clients are
removed from every request, on every binding. Responses to requests with a
verified certificate are treated like responses to requests with
credentials by the response cache (stored only when marked `public`).
Refusals: `403` ("A client certificate is required." / "... not accepted
here (reason)", the site's custom 403 page if it has one).

## OCSP stapling

Certificates whose leaf names an OCSP responder (and whose file includes
the issuer's certificate) get their OCSP response fetched and stapled to
TLS handshakes (HTTP/1.1, HTTP/2 and HTTP/3). Responses are verified
(signed by the issuer or a responder it delegated to with the OCSP Signing
usage, about this certificate, not dated in the future, not past
`nextUpdate`), saved as `data/certs/<id>/ocsp.der` so that a restart
staples at once without the responder, refreshed halfway to `nextUpdate`,
retried after 1, 2, 4... minutes (at most an hour) on failure, and dropped
two minutes before `nextUpdate` if no fresh one came. Only `good` answers
are stapled. Let's Encrypt ended OCSP in 2025: its certificates name no
responder and show `state: "none"` (nothing to staple), as do self-signed
ones.

`OCSPStatus` = `{state, responder?, mustStaple?, stapled, thisUpdate?,
nextUpdate?, revokedAt?, revocationReason?, lastCheck?, nextCheck?,
lastError?}`; `state`: `none` | `pending` | `good` | `revoked` | `unknown` |
`error`. Events: `cert.revoked` (error; an ACME certificate with automatic renewal
is requested again at once, with a new key) and `cert.stapling` (warning: a
Must-Staple certificate has no valid response to staple). Both reach
webhooks and the status icon.

## HTTP/3

`Settings.tls.http3` (off by default; also `PUT /api/tls`) opens a UDP
(QUIC) listener on the same address and port as every HTTPS listener.
HTTP/1.1 and HTTP/2 responses on those ports carry `Alt-Svc: h3=":443";
ma=86400`; requests over HTTP/3 go through the same pipeline, with the same
SNI certificate choice, client certificates and OCSP staples (QUIC always
uses TLS 1.3; 0-RTT is off, because early data can be replayed). A binding
to a specific address on a port whose listener takes all addresses is not
advertised: over QUIC on Windows the address a request arrived on is not
known, so another binding could answer. `http3Listeners` lists the open
UDP listeners (`udp :443`) and failures (`FAILED udp :443: ...`, also a
`server.listen` event); HTTPS keeps working when a UDP port cannot be
opened. Access logs show the protocol (`HTTP/3.0`), log shipping has
`access.protocol`, and Prometheus counts requests per protocol. Setup's
Windows Firewall rule allows the program, UDP included; firewalls in front
of the server must allow UDP on the HTTPS ports.

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

`ipBan.wafBlocks` (default 5 in 60 s) counts requests a site's web
application firewall blocked (below); detections in detect mode never count.

## Web application firewall

Per site, `routing.waf` = `{mode, paranoiaLevel?, anomalyThreshold?,
inspectBodyKB?, exclusions?[]}`:

- `mode`: `off` | `detect` (log what would be blocked, block nothing) |
  `block`. Absent (sites saved before the firewall existed) is off. A site
  created without one gets the server's defaults, `Settings.waf` =
  `{defaultMode, defaultParanoiaLevel, defaultAnomalyThreshold,
  eventRetentionDays}` (defaults `detect`, 1, 5, 30), except redirect sites
  and background workers, which start off (a worker cannot have it on).
- Rules (IDs in the OWASP CRS ranges: 913 scanners, 920 protocol, 930 path
  traversal/LFI, 931 RFI, 932 command injection, 933 PHP, 934 Node.js, 941
  XSS, 942 SQL/NoSQL injection, 944 Java) each belong to a paranoia level
  (1-3, default 1) and add their severity to the request's anomaly score:
  critical 5, error 4, warning 3, notice 2. A request whose score reaches
  `anomalyThreshold` (default 5: one critical match) is blocked (or
  detected). Each rule counts once per request; inspection stops once the
  threshold is reached.
- Inspected, after decoding (repeated URL decoding, `%uXXXX`, HTML
  entities, JavaScript escapes, overlong UTF-8, full-width forms,
  lowercase, NUL removal; SQL comments for the SQL rules): the path, query
  string arguments (names and values, parsed leniently), cookies,
  `User-Agent` and `Referer` (injection rules on these from paranoia level
  2), every other header but `Authorization` for Log4Shell, Shellshock and
  OGNL, uploaded file names, and bodies up to `inspectBodyKB` (default 128,
  at most 4096) that are `application/x-www-form-urlencoded`, JSON (keys
  as argument names, dotted: `post.body`), `multipart/form-data` (text
  fields; file contents are not inspected), text (`text/*`, GraphQL) or,
  from paranoia level 2, XML. The rest of a body, and binary or compressed
  bodies, stream to the site uninspected; what was read is replayed to it
  first, so it receives every byte.
- `exclusions[]` = `{path?, ruleIds?[], categories?[], args?[], cookies?[],
  headers?[], comment?}`: under `path` (a prefix; absent = the whole site),
  with `args`/`cookies`/`headers` (names, case-insensitive, a trailing `*`
  matches a prefix) those are not inspected by the listed rules and
  categories (all rules if none); without names the listed rules and
  categories are off; with nothing listed the firewall is off under
  `path`. Categories: `sqli`, `xss`, `lfi`, `rfi`, `rce`, `nodejs`, `php`,
  `java`, `scanner`, `protocol`.

It runs after IP restrictions, maintenance mode, rate limiting, basic
authentication and the body size limit, and before URL rewriting (it sees
what the client sent). A blocked request gets the site's 403 error page,
or the built-in one showing the request ID, and an `X-Request-Id` header.
Blocks count towards automatic IP banning (`ipBan.wafBlocks`) unless the
site is exempt (`routing.banning.exempt`). A mounted site (a location of
kind `site`) is covered by the firewall of the site it is mounted in.

Blocked and detected requests are saved as `WAFEvent` = `{seq, id, time,
siteId, action: blocked|detected, clientIp, method, host, path (no query
string), userAgent?, score, threshold, paranoiaLevel, matches[]}` with
`WAFMatch` = `{ruleId, category, severity, score, message, in:
path|arg|argName|cookie|header|file|body|request|query, name?, snippet?}`;
`snippet` is the matched text (decoded, at most 120 bytes, control
characters escaped), `[redacted]` for arguments and cookies whose names
look like passwords, tokens or session IDs. Events are kept
`eventRetentionDays`, at most 100,000; at most 200 a second are saved (the
rest are counted, `nodehoster_waf_events_dropped_total`). Blocks raise
`security.waf` events (warning; at most 10 a minute, the rest summarized).

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/waf/rules` | any signed-in user | `WAFRuleInfo[]` = `{id, category, severity, score, paranoiaLevel, message}` |
| GET | `/api/waf/events?siteId=&action=&ip=&rule=&category=&requestId=&since=&before=&limit=` | viewer (site-scoped: their sites) | `WAFEvent[]`, newest first; `before` is a `seq` for the next page, `limit` ≤ 1000 (100) |
| GET | `/api/sites/{id}/waf/events?…` | viewer on the site | the same, for one site |
| GET | `/api/sites/{id}/waf` | viewer on the site | `{config: WAFConfig, stats: {inspected, blocked, detected, matches: {category: n}}}`; counters since the service started |
| PUT | `/api/sites/{id}/waf` | admin | body `WAFConfig`; replaces the site's firewall, the rest of the site unchanged; audited `waf.update` |
| POST | `/api/sites/{id}/waf/exclusions` | admin | body `WAFExclusion`; 201 `{config, stats}`, 409 if the site already has it; audited `waf.exclusion.add` |

`/metrics` adds, for sites with the firewall on,
`nodehoster_waf_inspected_total{site,type}`,
`nodehoster_waf_requests_total{site,type,action="blocked|detected"}` and
`nodehoster_waf_rule_matches_total{site,type,category}`.

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
and parameters in its header; r = 8, p = 1 and N up to 2^20 are accepted, at
most 1 GiB of memory), holding `key.pem` in place of the sealed
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

## Updates

The server checks the signed release feed (`latest.json` and
`latest.json.sig`, an Ed25519 signature of the manifest's exact bytes) and
installs newer releases with setup. The settings are `settings.updates`
`{auto, time: "HH:MM", weekdays: [0-6]}` (also in `GET/PUT /api/settings`).
Admin only: installing runs setup as SYSTEM.

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/api/updates` | | `UpdateStatus` |
| PUT | `/api/updates` | `{auto, time, weekdays}` | `UpdateStatus`; 422 with `field` `updates.time` or `updates.weekdays` |
| POST | `/api/updates/check` | | `UpdateStatus` after reading the feed; 409 when this installation cannot update itself (development build, portable copy) or a check/install is running; 502 when the feed cannot be read or its signature does not verify |
| POST | `/api/updates/install` | | 202 `UpdateStatus` (checks first, then downloads and starts setup in the background; the service stops shortly after); 409 as above, or when there is nothing newer |

`UpdateStatus` = `{auto, time, weekdays, current, state:
idle|checking|downloading|installing, supported, reason?, available?:
{version, published, size, notes, manual, failed}, lastCheck?, lastError?,
nextCheck?, nextInstall?, lastResult?: {from, to, trigger:
schedule|manual, startedAt, finishedAt?, ok, exitCode, error?, log?}}`.
`manual`: the update policy leaves the release to an administrator even
with `auto` on; `failed`: installing it failed before, so it is not retried
unattended. `nextInstall` is set when the available release will install
itself.

Events: `update.available` (info, once per version), `update.installing`
(info), `update.installed` (info, from the service that starts after the
update), `update.failed` (error).

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

## Secret stores

`Settings.secretStores[]` (`SecretStore`), admin only: external secret
managers that environment variables and git tokens take their values from.
`{id, name, type: vault|infisical|bitwarden, url, caCert?, cacheTtlSec,
watchIntervalSec, vault | infisical | bitwarden}`:

- `name` is what references use (letters, digits, `.`, `_`, `-`; unique,
  not case-sensitive). A store that a site references cannot be removed or
  renamed, and its type must understand the references (422 on
  `secretStores`, naming the site and the variable).
- `url`: Vault/OpenBao's address (required); for Infisical and Bitwarden
  `""` is their cloud, else a self-hosted server's base URL.
- `caCert`: PEM certificates trusted for this store besides the system's.
  TLS verification cannot be turned off.
- `cacheTtlSec` (default 300, 10–86400): how long a value read is reused.
  `watchIntervalSec` (0 = off, else 60–86400): how often the secrets that
  running sites' instances started with are read again; a site whose value
  changed is recycled (rolling, no downtime) with a `secret.rotated` event.
- `vault: {auth: token|approle, token, roleId, secretId, authMount
  (default approle), namespace, mount (default secret), kvVersion: 1|2
  (default 2)}` — `token` and `secretId` are secrets. AppRole tokens are
  renewed at half their TTL and replaced by a new login when renewal is
  capped by the max TTL or refused; a renewable token given directly is
  renewed too.
- `infisical: {clientId, clientSecret, projectId, environment}` — a
  machine identity's Universal Auth credentials (`clientSecret` secret);
  `environment` is the slug (`prod`).
- `bitwarden: {accessToken, region: us|eu, apiUrl?, identityUrl?}` — a
  Secrets Manager machine account's access token (secret; format
  `0.<id>.<secret>:<key>`, checked when saved). Without `url`, `region`
  picks `api.bitwarden.com`/`identity.bitwarden.com` or the `.eu` hosts;
  with `url`, `<url>/api` and `<url>/identity`. Vaultwarden does not
  implement Secrets Manager.

Credentials are sealed with the master key, masked as `__SECRET__` in
responses (send the mask back to keep them), carried by configuration
backups like other secrets (portable with a passphrase; restoring onto a
server that has a store of the same name and type keeps that store's
credentials when the archive's cannot be read).

References: `EnvVar.from: {store, ref}` (site and task variables; the
variable then has no `value` and `secret` is false; `NODE_OPTIONS` cannot
be one) and `deploy.git.tokenFrom: {store, ref}` (then `deploy.git.token`
must be empty). Text form, as NodeHoster Manager and the command line show
it: `secretref:<store>/<ref>`. `ref` is, per store type:

| Store | `ref` | Example |
|---|---|---|
| vault | `<path>#<key>`, path relative to the mount | `app/prod#DB_PASSWORD` |
| infisical | secret name, optionally in a folder | `DB_PASSWORD`, `/backend/DB_PASSWORD` |
| bitwarden | secret ID (UUID) | `3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10` |

Saving a site checks that each store exists and each `ref` has its
store's syntax (422 on `node.env[i].from.store`, `….from.ref`,
`tasks[i].env[j].from.ref`, `deploy.git.tokenFrom.ref`).

Values are read when an instance starts (every start, restart and
recycle), a task runs and a deployment builds (install and build commands,
git clone), kept in memory only, and never returned by any endpoint. A
value younger than the store's `cacheTtlSec` is reused. When the store
cannot be read, the last value read is used (event `secret.stale`,
warning, at most every 10 minutes per store); a secret the store says does
not exist, or one never read, fails the start (event `secret.failed`,
error, with the variable and the store's explanation; at most every 10
minutes per site and kind of start). A recycle that fails this way keeps
the running instances.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/secret-stores` | | `[{name, type, cached, references, lastSuccess?, lastError?, lastErrorAt?, tokenExpires?}]` (admin) |
| POST | `/api/secret-stores/test` | `{store: SecretStore, ref?}` (masked credentials are the saved store's with the same `id`) | `{ok, detail?, error?}` (admin, audited `secretstore.test`): signs in (Vault: token lookup; Infisical: login and a value-less listing of the environment; Bitwarden: login and decryption of the organization key) and reads `ref` if given, reporting its length only |
| POST | `/api/secret-stores/resolve` | `{store, ref}` | `{ok, detail?, error?}` (admin, audited `secretstore.resolve`): reads the reference from the saved store now; the value is never returned |
| POST | `/api/sites/{id}/secrets/check` | | `[{field, variable?, task?, ref: {store, ref}, ok, error?}]` (operator on the site): reads every reference of the site now |

## Log search

Searches read the current file backwards, then the rotated copies
(`app-<time>.log`, also `.gz`), newest first, stopping after 3 seconds or
256 MB read with `truncated: true`. `cursor` continues after the last line
returned (at the limit or the budget); a cursor survives a rotation.
A `.gz` copy can only be read from its start, so a page that runs out of
budget inside one stops before its lines and the next page reads that
copy again, from the start, with a budget of its own (a page that starts
at a `.gz` copy always reads it through).
Parameters: `q` (case-insensitive text, or an RE2 regular expression of at
most 512 characters with `regex=1`; matched against the message, not the
timestamp prefix), `since` / `until` (RFC 3339, or a duration back from
now such as `15m`, `24h`), `limit` (default 200, at most 1000), `cursor`.

| Method | Path | Role | Response |
|---|---|---|---|
| GET | `/api/sites/{id}/logs/search?source=app\|access&stream=…` | viewer on the site | `{lines: LogLine[], truncated, cursor?, scannedBytes}` |
| GET | `/api/server/logs/search?level=warning` | admin | same; `LogLine.s` is the line's level, `m` the whole line |

## Resource alerts

Like Azure Monitor metric alerts (or a Prometheus rule with `for:`): a
rule fires when a metric stays past its limit for `forMinutes`, and
resolves once it has been back within the limit for the recovery period.
Rules are evaluated every 15 s from the metrics NodeHoster already keeps;
nothing is added to the request path but one counter per request (a
response time histogram for the 95th percentile).

**Rules.** `Settings.alerts` (admin) = `{enabled, siteRules[], serverRules[],
recoveryMinutes, emailTo[]}`; off by default, with a starting set of rules
(settings saved before alerts existed get it too). A rule is
`{id, metric, threshold, forMinutes, severity: warning|critical,
windowMinutes?, minRequests?, repeatHours?, disabled?}`; an `id` left out is
made from the metric (`cpu`, `cpu-2`…; `site-cpu` on a site). Rule IDs are
unique across both lists. `recoveryMinutes` (1–60, default 2) is the
hysteresis that keeps a value hovering at the limit from firing again and
again. `forMinutes` 0 fires on the first evaluation past the limit;
`repeatHours` (0–168) sends a reminder while it fires.

| Metric | Of | Value |
|---|---|---|
| `cpu` | site | CPU of all instances, % of one core (as the site's Overview shows it) |
| `instanceCpu` | site | the busiest instance's CPU, % of one core |
| `memory` | site | memory of all instances, MB |
| `memoryPercent` | site | the largest instance, % of its memory limit (the lower of `limits.memoryLimitMB` and `recycle.memoryLimitMB`; sites with neither are skipped) |
| `eventLoopLag` | site | the worst instance's event-loop lag, ms (sites with `agentEnabled`) |
| `errorRate` | site | 5xx answers, % of the requests of the last `windowMinutes` (1–30, default 5) |
| `latency` | site | average response time over the window, ms |
| `latencyP95` | site | 95th percentile response time over the window, ms, estimated from a histogram (buckets 5, 10, 25, 50, 100, 250, 500, 750 ms, 1, 1.5, 2, 3, 5, 10, 30, 60 s) |
| `instancesDown` | site | configured instances not ready and healthy (threshold 0: any) |
| `serverCpu` | server | machine CPU, % |
| `serverMemory` | server | machine memory in use, % |
| `diskFree` | server | free space on the emptiest drive holding the data directory, the sites folder or a site's folder, % (fires **below** the threshold) |

The rate metrics count a window with fewer than `minRequests` (default
20) requests as within the limit: one failure out of one request is not a
100% error rate. Node.js metrics apply to node and worker sites, request
metrics to sites that answer HTTP (not workers); a server-wide rule a site
cannot be measured on is skipped for it. Certificate expiry is not a rule:
`certExpiryWarnDays` and `cert.expiring` cover it.

**Per site.** `Site.alerts` = `{disabled?, rules[]?}`: `disabled` opts the
site out of site alerts altogether; a rule with the `id` of a server-wide
site rule replaces it for this site (`disabled: true` turns it off here);
other rules are the site's own. Saved with the site (admin), validated
against its type.

**What counts as sustained.** Each evaluation finds a rule past its limit,
within it, or with nothing to measure. A condition fires once every
evaluation for `forMinutes` found it past the limit, except those with
nothing to measure; a gap of more than a minute between evaluations (the
service was stopped or stalled) starts the period over. A stopped (or
stopping) site is within every limit, so its alerts resolve; a starting
site has nothing to measure: a pending condition neither advances nor
resets, a firing alert counts it as clear. A failed site (rapid-fail
protection gave up) only has instances down.

**Across a restart.** Firing alerts are stored and come back firing,
without being notified again; they resolve (notified) once their condition
has been clear for the recovery period, or at the first evaluation when
their rule, their site or alerts as a whole are gone ("site deleted",
"rule removed or turned off", "alerts turned off"). Pending conditions are
not stored: their period starts over.

**Notifications.** `alert.firing` (level `warning`, or `error` for critical
alerts; reminders too) and `alert.resolved` (`info`) events, which reach the
event log, webhooks (Slack, Teams, Discord, generic), log shipping and the
status icon (critical alerts not silenced turn it amber). The message names
the value, how long and the limit: `CPU 93% (instance 1) for 10 min (limit
90%)`, `Resolved after 25 min: CPU 40% (instance 1) (limit 90%)`; the event's
site gives the rest (`[api.example.com] …` in chat). An evaluation delivers
at most 10 notifications, critical ones first; one more summarizes the
others. With `emailTo`, each evaluation's notifications are also one
e-mail, queued on the built-in SMTP server (delivered directly or through
its smart host, whether or not it listens) from `nodehoster@<mail host
name>`.

**Silences.** A silenced alert sends no notification and no reminder, and
its resolution is only notified if its firing was. Silencing for some
minutes also covers the rule's next alerts on the same site until the time
is up (a flapping condition stays quiet); `minutes: 0` acknowledges the
alert: silent until it resolves. Lifting the silence of an alert that
fired unnotified notifies it at the next evaluation.

| Method | Path | Role | Body | Response |
|---|---|---|---|---|
| GET | `/api/alerts?siteId=` | viewer (filtered) | | `{enabled, firing: Alert[], pending: Alert[]}` — critical first, then oldest |
| GET | `/api/alerts/history?siteId=&server=1&limit=100` | viewer (filtered) | | `Alert[]` that fired, newest first (at most 1000; `server=1`: server alerts only) |
| POST | `/api/alerts/{id}/silence` | operator on the alert's site (server alerts: server operator) | `{minutes, note?}` | `Alert` (0–43200 minutes; 409 when it has resolved) |
| DELETE | `/api/alerts/{id}/silence` | same | | `Alert` |
| GET | `/api/sites/{id}/alert-rules` | viewer on the site | | `{enabled, defaults: AlertRule[], recoveryMinutes}` — the server-wide site rules, for the site's Alerts tab |

`Alert` = `{id, ruleId, siteId?, siteName?, metric, severity, threshold,
forMinutes, state: pending|firing|resolved, value, peak, detail?, message,
since, firedAt?, resolvedAt?, resolveNote?, notified, lastNotifiedAt?,
silence?: {until?, by, at, note?}}`. Site-scoped callers see their sites'
alerts only, never server alerts (`siteId` of a site they cannot see: 404).
History is kept as long as events (`logRetentionDays`), at most 5,000
resolved alerts. Silences are audited (`alert.silence`, `alert.unsilence`).

## Prometheus

`GET /metrics` on the admin listener (requires a bearer token) exposes
`nodehoster_requests_total{site,code}`,
`nodehoster_requests_by_protocol_total{site,protocol}` (`HTTP/1.1`, `HTTP/2`,
`HTTP/3`), `nodehoster_instance_memory_bytes`, etc.,
and `nodehoster_alert_firing{rule,metric,severity,site,silenced} 1` for each
resource alert firing (`site=""` for the server's).
A site-scoped (or site-restricted) token sees only its sites (and their
alerts) and no certificate metrics.

## Server connections (multi-server)

Like IIS Manager's "Connect to a Server": a server keeps connections to
other NodeHoster servers, and the web console operates any of them through
this one. A connection is `ServerConnection` = `{id, name, url, token,
fingerprint?, minRole, createdAt, updatedAt}`:

- `url`: the remote web console's base URL (`https://web02:8484`; a path
  prefix is kept, a trailing `/api` dropped). `https://` is required, except
  to this computer (`http://127.0.0.1…`, `localhost`).
- `token`: an API token created on the remote server (secret: sealed,
  read as `__SECRET__`, travels in configuration backups like every other
  secret). Sending the mask back keeps it, unless the URL now points to
  another host (scheme, host or port changed): then it must be entered
  again (422 on `token`), so a stored token is never sent to a server it
  was not made for. Use a dedicated token, restricted to the role (and
  sites) this server's users need there.
- `fingerprint`: pins the remote certificate (SHA-256 of the leaf, 64 hex
  digits; colons, spaces and a `SHA256:` prefix are accepted). Without it
  the certificate must verify against the trusted roots for the host name.
  With it, that certificate and no other is accepted. Verification is
  never simply switched off.
- `minRole`: the least role a user of this server needs to see and use the
  connection: `admin` (default), `operator` or `viewer`. Site-scoped users
  and site-restricted tokens never can.

| Method | Path | Role | Body | Response |
|---|---|---|---|---|
| GET | `/api/servers` | viewer | | `ServerView[]` (`ServerConnection` + `health`), only those the caller's role may use |
| GET | `/api/servers/{id}` | viewer | | `ServerView`; 404 for one the caller may not use |
| POST | `/api/servers/{id}/check` | viewer | | `ServerView` after checking it now |
| POST | `/api/servers` | admin | `ServerConnection` | 201 `ServerView` (audited `server.add`) |
| PUT | `/api/servers/{id}` | admin | `ServerConnection` | `ServerView` (audited `server.update`) |
| DELETE | `/api/servers/{id}` | admin | | 204 (audited `server.remove`) |
| POST | `/api/servers/test` | admin | `{id?, url, token, fingerprint?}` | `ServerTestResult` |
| any | `/api/servers/{id}/proxy/<path>` | viewer (and `minRole`) | as the remote endpoint | the remote `/api/<path>`'s answer |

`health` (`ServerHealth`) is refreshed every 30 s (8 servers at a time, 10 s
each), and right after a connection is added or changed: `{reachable,
error?, checkedAt?, latencyMs, since?, version?, commit?, hostname?, os?,
cpuPercent, cpuCount, memTotal, memUsed, sites, running, degraded, failed,
stopped, user?, role?, roleLimits}` (the sites the token can see, by state;
who the token is there; whether the server applies
`X-NodeHoster-Role-Limit`, known once `user` is set). `checkedAt` absent:
not checked yet. A server that answers but refuses the token is not
reachable. After two failed checks in a row a `remote.down` event (warning)
is raised, and `remote.up` (info) when it answers again, once each.

`POST /api/servers/test` is for setting a connection up (trust on first
use): `ServerTestResult` = `{certificate?: {fingerprint, subject, issuer,
dnsNames, notBefore, notAfter, verified, verifyError?}, trusted, health}`.
The certificate is read without trusting it; the token is only sent when
the connection is `trusted` as configured (the certificate verifies, or
matches `fingerprint`), and `health` is then what a check found. With `id`
and a masked token, the stored token is used (same host only).

**The proxy.** `/api/servers/{id}/proxy/<path>?<query>` is forwarded to
`<url>/api/<path>?<query>` with the connection's token, streaming both
ways: event streams (`/stream`, log tails) stay open, and uploads (a zip
deployment, a restore) are passed through, up to 8 GiB. Only these request
headers travel: `Accept`, `Accept-Language`, `Content-Type`,
`Last-Event-ID`, `Range`, `If-None-Match`, `If-Modified-Since`,
`X-Backup-Passphrase`, plus `Authorization: Bearer <token>` and
`X-NodeHoster-Role-Limit` (below); the caller's cookies, `Authorization`,
CSRF and forwarding headers never do. Only `Content-Type`,
`Content-Length`, `Content-Disposition`, `Content-Range`, `Accept-Ranges`,
`Last-Modified`, `ETag` and `X-Accel-Buffering` come back (never
`Set-Cookie`); this server's own security and caching headers apply, with
`Content-Security-Policy: sandbox; default-src 'none'` and
`X-Content-Type-Options: nosniff` instead of the console's policy, so
nothing a remote server sends runs as a page of this server. Rules:

- The path stays under the remote `/api`: segments `.` and `..`, a
  backslash or NUL (also percent-encoded) and empty segments are refused
  (400). Escapes such as `%2F` inside a segment travel as they are.
- The remote account endpoints are refused (403): `auth/*` except
  `auth/me`, and `tokens`. Nobody mints tokens or changes the account
  behind a connection.
- The CSRF rule applies to the local request as usual. A caller whose role
  is `viewer` only reads (`GET`/`HEAD`; 403 otherwise).
- The caller's role travels as `X-NodeHoster-Role-Limit`: a server of this
  version or later narrows the request's access to at most that role, so
  a local operator cannot act as an administrator there even with an
  administrator's token (`auth/me` shows the capped access). Any client
  may send the header to narrow its own access; an unknown role is 400.
  The server answers `X-NodeHoster-Role-Limit-Applied: <role>` when it
  applied it, and refuses a limited request to the account endpoints
  (`auth/*` other than `auth/me`, `tokens`, and a change to the token's
  own user through `/users/{id}`) with 403.
- An older remote server ignores the header, which would give the token's
  full rights to anyone: only administrators may use it. For others the
  proxy answers 403 (`… is too old for role limits`) when the last check
  found the server does not echo the limit (a connection not checked yet
  is checked first; 502 when that fails), and 502 when a successful
  answer does not carry the echo.
- Only the media types the API answers pass as they are:
  `application/json`, `text/event-stream`, `text/plain`,
  `application/octet-stream`, `application/zip`, `application/gzip`,
  `application/x-pkcs12`, `message/rfc822`. Any other (or none, with a
  body) becomes `application/octet-stream`, and every type but JSON, event
  streams and plain text is sent with `Content-Disposition: attachment`.
- The remote server's 401 becomes a 502 of this server (the token was
  revoked or expired), so that the console does not take it for its own
  session ending; redirects are not followed (502). Other answers,
  including 404 `no such endpoint` from an older version, pass through.
- Timeouts: 10 s to connect and for the TLS handshake, 5 minutes for the
  answer's headers, and a response other than an event stream ends when
  the remote server sends nothing for 2 minutes (or past 16 GiB). An event
  stream ends within 2 s of the caller losing the right to use the
  connection (role changed, connection removed or restricted, session or
  token ended).
- Every proxied request other than `GET`/`HEAD` is in this server's audit
  log: `server.proxy`, the connection's name as target and `METHOD
  /api/<path> (status)` as detail. The remote server audits it too, as the
  token's user.

The connections are kept apart from `Settings` (saving settings never
touches them) and are part of configuration backups (`servers` in the
export; a backup from before connections existed leaves this server's in
place when restored).

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
| GET | `/status` | `{version, startedAt, adminUrl?, adminError?, sites: [{id, name, type, autoStart, state, message?, instances, ready}], alerts?: [{id, siteId?, site?, severity, message, silenced?}]}` (`alerts`: resource alerts firing) |
| GET | `/status/stream` | **Server-Sent Events**: `event: summary` (as `/status`) every 3 s; `event: notice` `{time, level, type, site, message}` for crashes, rapid-fail, failed health checks and deployments, certificate problems, unreachable upstreams and resource alerts firing or resolved |

`adminError` is set when the web console could not start (its port is in
use, or its certificate is missing): the server keeps running without it.

What the status pipe tells every interactive user of the computer is what
the icon shows: this server's sites and their state, its resource alerts,
its web console's URL. A connected server going down (`remote.down`) is a
notice naming the server only (`Server web02 is unreachable`), without its
URL or the error, which the web console shows those who may use the
connection. Nothing about the connections or their tokens travels there.

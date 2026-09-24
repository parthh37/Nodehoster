# NodeHoster

An IIS-style application server for **Node.js on Windows**. Sites and
bindings like IIS, application-pool–style process management, a built-in
reverse proxy and automatic HTTPS with Let's Encrypt — one self-contained
`nodehoster.exe` running as a Windows service, administered from a web
console or from **NodeHoster Manager**, a desktop console in the style of
IIS Manager, with a status icon in the notification area.

## Features

**Sites & bindings**
- Site types: **Node.js application**, **background worker** (a Node.js process without HTTP), **reverse proxy**, **static site**, **redirect**
- IIS bindings: protocol, IP address (or all unassigned), port, host name; wildcard host names
- IIS precedence: specific IP › all addresses, exact host › `*.wildcard` › empty host
- SNI: any number of HTTPS sites on one IP:port, each with its own certificate
- Conflict detection (duplicate bindings, http/https on the same port)
- Locations (IIS applications / virtual directories): mount another site, a URL or a folder under a path

**Node.js process management (application pools)**
- Multiple instances per site, load-balanced (least connections)
- `PORT` assigned automatically from a configurable range (or a fixed port)
- Restart policy (always / on-failure / never) with exponential backoff
- **Rapid-fail protection**: too many crashes in a window pauses the site, then restarts it automatically (5 min, doubling up to 1 h per repeat); or stops it until started manually
- **Zero-downtime recycle**: new instance → ready → traffic moves → old one drains → stops
- Recycling on memory limit, periodic interval, schedule (HH:MM), request count, file changes
- Health checks (IIS "ping", on by default for new sites): instances that time out or answer 5xx are replaced
- The service itself restarts on failure (Windows service recovery), and auto-start sites come back with it
- **Windows Job Objects**: every process tree is killed with its site — no orphaned `node.exe`, even if NodeHoster crashes; optional hard CPU % and memory caps
- **Run as user** (application pool identity) via `LogonUser`
- Graceful shutdown on Windows through an injected agent (Windows has no SIGTERM): apps get `SIGTERM`/`SIGINT`/pm2 `shutdown`, or servers are closed after in-flight requests finish
- Per-instance CPU, memory, heap, event-loop lag, requests; stdout/stderr captured to rotating logs with live tail
- **Background workers**: queue consumers (BullMQ…), bots and long-running scripts supervised like web apps (no port; running once up for 2 s; rapid-fail protection, recycling, Job Objects, secrets, logs and metrics included)
- **Scheduled tasks** per site, like cron inside the site's sandbox: 5-field cron, `@daily`, `@every 15m` in server local time (DST-safe: a skipped hour does not run, a repeated one runs once), overlap policy (skip / queue / allow), timeout that kills the process tree, run now / cancel, history with per-run logs, `task.failed` / `task.timeout` notifications
- **Node.js version manager**: install any version from nodejs.org (SHA-256 verified), pin per site
- Environment variables with **secrets encrypted at rest** (AES-256-GCM, master key protected by DPAPI)

**Reverse proxy & request pipeline**
- HTTP/1.1, HTTP/2, WebSockets, SSE/streaming, `X-Forwarded-*` headers, trusted proxies
- Upstream load balancing: round-robin (weighted), least connections, IP hash, random; active and passive health checks
- **Session affinity** like ARR's client affinity: a signed, opaque cookie keeps each client on its instance, server or upstream (works behind CDNs/NAT, survives zero-downtime recycles, WebSockets included); moved and re-issued when the backend goes down
- HTTPS redirect, HSTS, **Brotli and gzip compression** (negotiated by q-value, pre-compressed `.br`/`.gz` static files), request body limit, upstream timeout
- **Response cache** like IIS output caching: in memory per site, following Cache-Control/Expires/Vary, query string and header variants, request coalescing, LRU by memory, `X-Cache` header, purge from the console
- **URL Rewrite** like the IIS module: ordered rules with conditions (headers, query string, server variables, file/directory exists), match all/any, negation, `{R:1}` / `{C:1}` back-references, rewrite maps and `{ToLower:…}`-style functions; rewrite, redirect, block, custom response
- Rewrite to an absolute URL proxies the request there (like URL Rewrite with ARR); **outbound rules** rewrite response headers (`Location`), URLs in HTML tags or any text in a body
- **Import** rules from an IIS `web.config` or an Apache `.htaccess` (mod_rewrite, `Redirect`, `RedirectMatch`); what cannot be converted is listed
- **MIME types** like IIS: a built-in table (the Windows registry is never consulted, so `.js` is never served as `text/plain`), server-wide and per-site mappings, and unknown extensions either served as `application/octet-stream` or refused with 404
- Request/response header rules, **IP & domain restrictions** (CIDR allow/deny)
- Basic authentication, per-client **rate limiting**
- **Automatic IP banning** like fail2ban: failed sign-ins (sites and web console), 404 scans, rate-limit rejections and trap paths (`/wp-login.php`, `/.env`…) ban the client address across all sites, escalating for repeat offenders; IPv6 by /64, allow list, trusted proxies respected, per-site opt-out, bans survive restarts; unban from the web console or NodeHoster Manager
- **Maintenance mode** (like `app_offline.htm`) with IP bypass, custom error pages
- Access logs in combined format; default page for unbound host names

**SMTP server (built in, send-only)**
- NodeHoster's own SMTP server, part of `nodehoster.exe`: no Windows or IIS SMTP service is needed
- Applications send through `127.0.0.1:25` (nodemailer, `System.Net.Mail`…) or drop `.eml` files in a **pickup folder**
- Connection and relay restrictions by IP/CIDR, optional SMTP authentication (PLAIN/LOGIN, bcrypt-hashed users), STARTTLS with a certificate from the store, allowed sender domains, size and recipient limits
- Delivers **directly** to the recipients' mail servers (MX lookup, opportunistic TLS) by default; a **smart host** (SendGrid, Amazon SES, Microsoft 365…) is optional
- **DKIM signing** per domain: keys generated for you, with the DNS record to publish
- **Deliverability check**: SPF (fully evaluated against this server's address), DKIM (published key matches), DMARC, MX, reverse DNS, HELO host name, outbound port 25 and IP/domain blacklists, with the exact DNS record to publish for each problem
- On-disk queue that survives restarts, retries with back-off until the message expires, undeliverable mail kept for inspection; queue view with retry, delete and download; notifications for failed mail
- Never accepts mail for local mailboxes: it is a relay for your applications, not a mail server

**Certificates**
- **Let's Encrypt / ZeroSSL / any ACME CA**, HTTP-01 and DNS-01 (wildcards)
- Automatic certificates per binding ("certMode: auto") with renewal at ⅔ of lifetime
- DNS providers: Cloudflare, Route 53, Azure DNS, **Windows DNS Server (RFC 2136 / GSS-TSIG)**, DigitalOcean, GoDaddy, Namecheap, Hetzner, OVH, Gandi, Porkbun, Linode, Vultr, DNSimple, NameSilo, IONOS, netcup, ClouDNS, Duck DNS, HTTP request, external program
- Import PFX/PEM, create self-signed, export PFX/PEM, expiry warnings

**Deployments**
- Upload a `.zip` or deploy from **git** (token auth never exposed in the process list)
- Install/build commands, shared paths (`.env`, `uploads`) persisted across releases
- Releases kept side by side; **one-click rollback**; activation is a zero-downtime recycle
- Push-to-deploy webhooks (GitHub, GitLab, Gitea signatures)
- **Import sites** from IIS (`applicationHost.config`, or this server's IIS: iisnode apps, bindings, virtual directories, URL Rewrite, ARR proxies, redirects), an iisnode `web.config` or PM2 (`ecosystem.config.js`, `pm2 jlist`), reviewed before anything is created

**Administration**
- **NodeHoster Manager**: native desktop console laid out like IIS Manager (connections tree, lists, actions pane), over a local named pipe that needs no password, port or certificate — it keeps working when the web console does not
- **Status icon** in the notification area: green/amber/red service and site health, notifications for crashes, rapid-fail, certificate problems and resource alerts, start/stop the service
- Web console (React) with live status over Server-Sent Events
- **Command line and PowerShell**: `nodehoster site|deploy|rollback|logs|events|task|cert|backup ...` (tables, or `--json` for scripts) and a `NodeHoster` PowerShell module (`Get-NHSite`, `Publish-NHSite`, `Undo-NHDeployment`...) over the local admin pipe
- Users with roles (admin / operator / viewer), **TOTP two-factor**, API tokens
- **Per-site permissions** like IIS Manager's: users allowed as viewer or operator on selected sites only, and API tokens restricted to a role and some sites (a CI token that can only deploy one site)
- **Single sign-on** to the web console with **Microsoft Entra ID** or any OpenID Connect provider (authorization code + PKCE): existing users by default, optional user creation and group/app-role → role mapping; MFA stays with the provider; password sign-in can be turned off (break-glass: NodeHoster Manager and `nodehoster reset-password`, which turns it back on)
- Audit log, event log, webhook notifications (Slack, Teams, Discord, generic)
- Metrics history and a Prometheus `/metrics` endpoint
- **Resource alerts** like Azure Monitor metric alerts: a rule fires when a metric stays past its limit for N minutes ("api.example.com: CPU 93% for 10 min (limit 90%)") and resolves after a recovery period, so a value hovering at the limit does not flap. Site CPU (total or busiest instance), memory (MB, or % of the site's memory limit), Node.js event-loop lag, 5xx error rate and average or p95 response time over a window (with a minimum request count), instances down; the server's CPU, memory and free disk space on the drives holding the data and the sites. Server-wide rules with per-site overrides and opt-out, warning or critical, reminders while firing; stopped or starting sites raise none. Notified through webhooks (Slack, Teams, Discord), the event log, the status icon (amber for critical alerts) and optionally e-mail through the built-in SMTP server; silence for a while or acknowledge until resolved; firing alerts survive a restart without notifying twice; history, a dashboard banner, an Alerts page in both consoles and `nodehoster_alert_firing` in Prometheus
- **Log shipping** to syslog (RFC 5424 over UDP, TCP or TLS), Seq (CLEF) or any HTTP collector (JSON or NDJSON batches): server log, sites' output and access logs, events and audit log, per-site filters; bounded queues that drop the oldest records rather than ever slowing a site, retries with back-off, delivery counters
- **Log search** across current and rotated (also gzipped) log files: text or regular expressions, stream and time range, newest first
- Backup & restore of the whole configuration
- **Automatic updates**: new releases are announced in the consoles, the event log and the status icon, and installed at a maintenance time you choose (setup asks whether to turn this on; Settings → Updates, NodeHoster Manager or `nodehoster update auto on|off` change it). Every release manifest is signed (Ed25519) and the setup it names is checked against it before it runs; a failed update restarts the previous version and is not retried unattended
- **Scheduled backups** to a folder or network share, S3-compatible storage (AWS, R2, B2, MinIO, Wasabi), Azure Blob Storage or SFTP (host key verified): configuration, certificates and keys, optionally the sites' shared folders; retention per destination; optional passphrase encryption (AES-256-GCM) that makes an archive restorable on a replacement server; restore from a file or straight from a destination

## Install

Download `NodeHoster-<version>-setup.exe` (release files are hosted on S3;
the GitHub release page links to them) and run it. The installer registers
the **NodeHoster** service (automatic, delayed start, restart on failure),
opens the firewall for the program, starts it and checks that it is running.
Upgrades install over the top; sites and data are kept. A first
installation also asks whether NodeHoster may install its own updates (see
[Updates](#updates)).

Setup then checks what sites need besides NodeHoster and installs what is
missing (the **Install what is missing** task, on by default):

- **Node.js**: if the server has no default Node.js version and no `node` on
  the PATH, the current LTS release is downloaded from nodejs.org (SHA-256
  verified), installed like any version on the Node.js page, and made the
  server default. A default version that was removed from disk is installed
  again.
- **Git** (for deployments from a repository): if there is no `git` on the
  PATH or in `C:\Program Files\Git`, MinGit, Git for Windows' edition for
  programs that run git, is downloaded from its GitHub release (SHA-256
  verified) into `C:\ProgramData\NodeHoster\git`, for NodeHoster only.

A server that already has them downloads nothing. If a download fails (no
internet), NodeHoster is still installed and running and setup says so; run
`nodehoster deps install` later from an elevated prompt. `nodehoster deps`
shows what is installed.

Unattended install:

```
NodeHoster-1.2.3-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /TASKS="addtopath,firewall,deps"
```

Exit code `0` means installed and running, `1` installed but the service did
not start (see `C:\ProgramData\NodeHoster\logs\nodehoster.log`), `7` not
installed because a newer version is (add `/ALLOWDOWNGRADE` to install the
older one anyway; interactive setups ask instead). Add `autoupdate` to
`/TASKS` to turn automatic updates on; leave `deps` out to install no
Node.js or Git. A dependency that could not be installed does not change the
exit code (the log says what failed; `nodehoster deps` exits with `1` while
one is missing).

Uninstall from **Settings → Apps**, or unattended (keeps the data):

```
"C:\Program Files\NodeHoster\unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
```

Uninstalling removes the service, the firewall rule, the PATH entry and the
status icon; an interactive uninstall asks whether to delete the data too.

The setup is not code-signed yet, so Windows SmartScreen may say "Windows
protected your PC": choose **More info → Run anyway**, after checking the file
against `SHA256SUMS.txt` from the same release.

Open **https://localhost:8484** (the console uses a self-signed certificate
until you pick one in Settings → Admin console). Sign in as `admin` with the
password from `C:\ProgramData\NodeHoster\initial-admin-password.txt`; you will
be asked to change it.

Portable use: unzip `nodehoster.exe` anywhere and run, from an elevated prompt,

```
nodehoster service install
nodehoster service start
```

### Updates

The service checks the release feed a couple of minutes after it starts and
every six hours, and announces a newer version once (event
`update.available`, which webhooks and the status icon show). With
**automatic updates** on, it installs it at the time you set (03:00 by
default, optionally on some weekdays only): it downloads the setup, checks
its size and SHA-256 against the release manifest, whose signature must
verify with the key built into NodeHoster, and runs it unattended. Setup
restarts the service, so sites are offline for a few seconds; the status
icons that were open come back. Otherwise install from **Settings →
Updates**, **NodeHoster Manager → Updates** or `nodehoster update install`.

If setup fails, the previous version is started again, the failure is
reported (`update.failed`, with setup's log in
`C:\ProgramData\NodeHoster\logs\update-<version>.log`) and that version is not
retried automatically. A portable `nodehoster.exe` and development builds
never update themselves. Upgrades never change the setting.

### NodeHoster Manager

**Start → NodeHoster Manager** opens the desktop console (it asks for
administrator rights, like IIS Manager). The left pane lists the server,
its sites (with their state on their icons), certificates, SMTP e-mail,
Node.js versions, web console users, banned addresses, alerts, the event and
audit logs and backups; the middle pane shows the selected one (the server's
home is a dashboard of the service, CPU, memory and disk); the right pane
has its actions: start/stop/restart/recycle a site, deploy a `.zip` to it,
edit its bindings, environment, URL Rewrite rules, MIME types and basic
settings, browse it, follow its log live (pause, filter, save), see a
deployment's output and roll back a release, run or cancel a scheduled
task, purge its response cache, install Node.js versions, reset a web
console user's password or two-factor authentication, ban and unban
addresses, silence or acknowledge an alert, manage the mail queue, change where the web console listens,
back up to a file, run a scheduled backup now and see its history, restore
from a backup, and start or stop the service.

Every list can be searched (Ctrl+F) and sorted by clicking a column, and
has the actions of its rows on a right-click; Delete removes, Enter opens,
F5 refreshes, Ctrl+N adds a site, Ctrl+1…9 go to a section. The window
remembers its size, its panes and its lists' columns.

It talks to the service over `\\.\pipe\NodeHoster.Admin`, which Windows only
opens to elevated Administrators: there is no NodeHoster login, and nothing
about the web console (its port, certificate or accounts) can lock you out.
If the web console cannot start (for example its port is taken), the
service now keeps hosting sites and reports the problem; fix it from the
manager (Tools → Web console settings) and restart the service.

The **status icon** (`nodehoster-manager.exe --tray`) starts at sign-in for
every user (installer task; each user can turn it off from its menu). It
runs unelevated and reads a read-only status pipe; its color is the overall
health, its menu lists the sites (and the critical alerts firing) and opens
the manager, and it notifies about crashes, rapid-fail protection, failed
deployments, certificates and resource alerts. A critical alert that nobody
silenced turns it amber.

### Command line

```
nodehoster run                     run in the foreground
nodehoster service install|uninstall|start|stop|status
nodehoster reset-password [user]   recover access (also turns password sign-in back on
                                   if single sign-on turned it off)
nodehoster version
nodehoster --data D:\NodeHoster run   use another data directory
```

Like `appcmd.exe` for IIS, `nodehoster` also manages the running service,
over the same local admin pipe as NodeHoster Manager (so from an elevated
prompt, with no password). `<site>` is a site's name or ID:

```
nodehoster site list | show <site> | start|stop|restart|recycle <site>
nodehoster deploy <site> --zip app.zip       upload a release, showing the log until it finishes
nodehoster deploy <site> --git [--branch x]  deploy from the site's repository
nodehoster releases <site>                   deployments; * marks the active release
nodehoster rollback <site> [<release-id>]    default: the previous successful release
nodehoster logs <site> [-n 100] [-f] [--access]
nodehoster events [-n 50] [--site x]
nodehoster task list <site>                  scheduled tasks, next run, last result
nodehoster task run <site> <task> [--no-wait]  run now, showing its output until it ends
nodehoster task runs <site> [<task>] [-n 20] | task cancel <site> <run-id>
nodehoster alert list [--site x] | alert history [-n 50] [--site x]
nodehoster alert silence <alert-id> [--minutes 60] [--note ...] | alert ack <alert-id> | alert unsilence <alert-id>
nodehoster cert list | cert renew <id|name|domain>
nodehoster backup <file>                     .zip: the full archive (encrypted with the backup
                                             passphrase, if set); any other name: the configuration (JSON)
nodehoster backup run | backup history [-n 10]  back up to the destinations now; recent backups
nodehoster restore <file> [--yes] [--passphrase-file <file>]
nodehoster update                            installed and newest version, automatic update settings
nodehoster update check | update install [--yes]
nodehoster update auto on|off [--time 03:00] [--days 0,6|all]
nodehoster deps                              Node.js and Git: installed or missing (exit code 1 if missing)
nodehoster deps install [node] [git]         install what is missing (setup runs this)
```

`backup run` and `backup history` are commands: to save a backup in a file
named `run` or `history`, give a path (`nodehoster backup .\run`). An
encrypted archive's passphrase is read from `--passphrase-file` (its first
line) or the `NODEHOSTER_BACKUP_PASSPHRASE` environment variable, never
from the command line, which other users can see in the process list.

`--json` prints the API's JSON instead of tables (while following, one JSON
object per line; a deployment's or task run's log goes to stderr). Exit
codes: 0 done, 1 failed (the service refused, a deployment, task run or
backup failed, it is not running or access was denied), 2 wrong usage.
`nodehoster <command> --help` lists a command's flags.

**PowerShell**: setup installs the `NodeHoster` module for Windows
PowerShell 5.1 and PowerShell 7, which wraps these commands and returns
objects: `Get-NHSite`, `Start-NHSite`, `Stop-NHSite`, `Restart-NHSite
[-Recycle]`, `Invoke-NHRecycle`, `Publish-NHSite -ZipPath|-Git`,
`Get-NHRelease`, `Undo-NHDeployment`, `Get-NHLog [-Follow]`, `Get-NHEvent`,
`Get-NHCertificate`, `Get-NHTask`, `Start-NHTask [-NoWait]`, `Get-NHTaskRun`,
`Start-NHBackup`, `Get-NHAlert [-Pending] [-History]`, `Set-NHAlertSilence
[-Minutes]`, `Clear-NHAlertSilence`. They take site names from the pipeline:

```powershell
Get-NHSite | Where-Object State -eq 'failed' | Start-NHSite
Publish-NHSite shop -ZipPath .\build\shop.zip
Get-NHLog shop -Tail 50 | Where-Object Stream -eq 'stderr'
Get-Help Publish-NHSite -Examples
```

## Hosting a Node.js app

1. **Node.js** page → install an LTS version (or use the `node` on PATH).
2. **Sites → New site → Node.js application**: app folder, entry script
   (`server.js`) or an npm script (`start`), instances.
3. Add bindings, e.g. `http *:80 app.example.com` and `https *:443 app.example.com`
   with certificate **Auto (Let's Encrypt)**. DNS must point at the server and
   port 80 must be reachable for the HTTP-01 challenge.
4. Your app must listen on `process.env.PORT`.

Environment set for every instance: `PORT`, `NODE_ENV=production` (unless
overridden), `NODEHOSTER_SITE`, `NODEHOSTER_INSTANCE`, `NODE_APP_INSTANCE`.

A process that does not serve HTTP (a BullMQ consumer, a Discord bot) is a
**Background worker** site instead: no bindings and no `PORT`; it counts as
running once it has stayed up for 2 seconds. Recurring jobs (a nightly
report, a clean-up every 15 minutes) are **Tasks** of a Node.js or worker
site: each run starts the script in the site's current release with its
Node.js version, variables and identity, plus `NODEHOSTER_TASK=<name>`; a
deployment during a run does not delete the release it runs in.

### Migrating from IIS/iisnode or PM2

**Sites → Import sites** in the web console (or **Import from IIS…** on
the Sites page of NodeHoster Manager, which reads this server's IIS) proposes
a site per IIS site, iisnode application or PM2 app, with notes on what was
converted, approximated or left out. Nothing is created until you confirm;
imported sites are created stopped. IIS is not changed: stop its sites
before starting the NodeHoster ones, as both cannot listen on the same port.
HTTPS bindings get Let's Encrypt certificates (IIS certificates stay in the
Windows store; import the PFX to reuse one), and application pool passwords
are never imported. PM2 ecosystem files are read, never run: if yours
computes values, import `pm2 jlist > apps.json` instead.

## Data directory

`C:\ProgramData\NodeHoster`

| Path | Contents |
|---|---|
| `nodehoster.db` | configuration and history (SQLite) |
| `nodehoster.json` | admin console listener (edit, then restart the service) |
| `master.key` | secret encryption key, DPAPI-protected (machine-bound) |
| `certs\` | certificates and keys |
| `node\` | installed Node.js versions |
| `sites\<id>\releases\` | deployed releases; `shared\` persisted files |
| `logs\` | server log; `logs\sites\<id>\` app and access logs, `tasks\` scheduled task runs |
| `tmp\` | work files, including archives while a backup or restore runs |

`master.key` cannot be copied to another machine, so a copy of this folder
does not carry the secrets to a new server. Use **Settings → Backups** with a
passphrase instead: the archive holds the configuration, certificates and
(optionally) shared folders, and restores on any NodeHoster server. Releases
and Node.js versions are not backed up: redeploy after restoring. A backup
without a passphrase restores on this machine only.

Only SYSTEM and Administrators can open the data directory. At every start
the server gives it a protected DACL, so it does not inherit the read access
(and the right to create files) that `%ProgramData%` gives every local user.
File modes and DPAPI do not protect against local users: any process on the
machine can unprotect `master.key`, so its file permissions are what keep it
private, along with the database, `certs\`, `acme\`, `admin-key.pem`, the
`mail\` spool, the logs and `initial-admin-password.txt`.

Sites that run as another account (the site's **Run as** identity, like an
application pool identity) read two folders that hold nothing secret:
`node\` and `run\` (the agent script) are readable, but not writable, by
local users. When such a site starts, its account is given Modify access
to the site's own folder, `sites\<id>\` (releases, `shared\`, npm cache),
and to no other site. NodeHoster manages that folder's explicit
permissions: changing or turning off the identity removes the previous
account's access. An application folder outside the data directory is the
administrator's to share with that account. Applications' output is
written to `logs\sites\<id>\` by the service, so that folder stays closed.

Run unelevated (for development), the server also admits its own account,
so it does not lock itself out of a data folder it created.

## Development

Requirements: Go 1.26+, Node.js 22+.

```
cd web && npm ci && npm run build && cd ..
go test ./...
(cd web && npm test)
go run ./cmd/nodehoster --data ./.devdata run
```

The Go code builds without the web build too (the console is then a page
saying how to build it). The UI dev server (`cd web && npm run dev`) proxies API calls to
`https://localhost:8484`. NodeHoster Manager is Windows-only
(`GOOS=windows go build ./cmd/nodehoster-manager` cross-compiles it); its
manifest and icons are committed `.syso` files, regenerated with
`go generate ./cmd/nodehoster-manager` (which also writes the installer's
`installer/nodehoster.ico`). Its icons are drawn in Go
(`internal/desktop/icons.go`) and stored as icon resources in every size a
display's scaling needs; `go run ./internal/mkicon -sheet icons.png` (in
`cmd/nodehoster-manager`) draws them all on one page for review; `nodehoster.exe`'s icon and version resources
are regenerated with `go generate ./cmd/nodehoster`. The server builds and runs on macOS and Linux too
(no job objects or DPAPI there), which is convenient for development.

CI runs on GitHub-hosted runners (`.github/workflows/build.yml`). Go tests on
Windows (including integration tests of job objects, the agent pipe and the
process manager) and Linux (with the race detector) and the web console
tests run in parallel; a packaging job then builds the binary, smoke-tests it,
builds the installer and tests it (`installer/test.ps1`: install, upgrade,
refused and forced downgrade, uninstall, checking the machine after each),
and builds a portable zip. Tags `vX.Y.Z` upload the release
to `s3://<bucket>/nodehoster/releases/<version>/` (never overwritten) and
create a GitHub release that links to it; nothing is stored on GitHub. The
upload tool is `tools/s3publish`; its configuration is described at the top
of the workflow.

## Architecture

```
cmd/nodehoster        entry point, CLI, admin listener
cmd/nodehoster-manager desktop manager and status icon (Win32, walk)
internal/core         composition root; site lifecycle
internal/procmgr      process supervisor (+ agent/ injected into apps)
internal/proxy        listeners, binding match, request pipeline, load balancing
internal/certs        ACME (lego), import/export, renewal
internal/alerts       resource alert rules: evaluation, firing/resolving state machine
internal/deploy       zip/git deployments and releases
internal/nodeversions Node.js runtime installer
internal/deps         Git lookup and MinGit installer (nodehoster deps)
internal/api          REST API (docs/API.md) and embedded web UI
internal/localapi     local pipes for the desktop programs (client; server in localserver)
internal/desktop      desktop presentation logic: health, formatting, icons
internal/auth         users, sessions, tokens, TOTP
internal/store        SQLite persistence
internal/secrets      AES-GCM + DPAPI
internal/service      Windows service integration
web/                  React admin console
installer/            Inno Setup script
```

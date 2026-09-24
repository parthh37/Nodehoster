# NodeHoster

An IIS-style application server for **Node.js on Windows**. Sites and
bindings like IIS, application-pool–style process management, a built-in
reverse proxy and automatic HTTPS with Let's Encrypt — one self-contained
`nodehoster.exe` running as a Windows service, administered from a web
console or from **NodeHoster Manager**, a desktop console in the style of
IIS Manager, with a status icon in the notification area.

## Features

**Sites & bindings**
- Site types: **application** (Node.js, Bun, Deno, Python, .NET or a custom command), **background worker** (the same without HTTP), **reverse proxy**, **static site**, **redirect**
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
- Graceful shutdown on Windows through an injected agent (Windows has no SIGTERM): apps get `SIGTERM`/`SIGINT`/pm2 `shutdown`, or servers are closed after in-flight requests finish; other runtimes get a console Ctrl+Break, which ASP.NET Core, uvicorn and Hypercorn shut down gracefully on
- Per-instance CPU, memory and requests for every runtime, heap and event-loop lag where the agent runs (Node.js, Bun); stdout/stderr captured to rotating logs with live tail
- **Background workers**: queue consumers (BullMQ…), bots and long-running scripts supervised like web apps (no port; running once up for 2 s; rapid-fail protection, recycling, Job Objects, secrets, logs and metrics included)
- **Scheduled tasks** per site, like cron inside the site's sandbox: 5-field cron, `@daily`, `@every 15m` in server local time (DST-safe: a skipped hour does not run, a repeated one runs once), overlap policy (skip / queue / allow), timeout that kills the process tree, run now / cancel, history with per-run logs, `task.failed` / `task.timeout` notifications
- **Node.js version manager**: install any version from nodejs.org (SHA-256 verified), pin per site
- **Other runtimes** under the same process manager, like IIS hosting more than ASP.NET: **Bun**, **Deno**, **Python** (a script, `python -m` a module, or an ASGI/WSGI app on uvicorn, Hypercorn or Waitress), **.NET** (ASP.NET Core on Kestrel: `dotnet app.dll` or a self-contained `.exe`, like the ASP.NET Core Module's out-of-process hosting) and any **custom command** that listens on `PORT`. Instances, restarts, rapid-fail protection, recycling, Job Objects, run-as identity, secrets, logs, CPU/memory, deployments and scheduled tasks work the same for all of them — see [Runtimes](#runtimes)
- **Bun and Deno versions** installed side by side from their GitHub releases (SHA-256 verified), pinned per site with a server default; **Python interpreters and .NET runtimes** found where they are installed (py launcher, PATH, standard folders) and picked per site
- Environment variables with **secrets encrypted at rest** (AES-256-GCM, master key protected by DPAPI)
- **Secret stores** like Azure App Service's Key Vault references: a variable (or a git deploy token) can come from **HashiCorp Vault / OpenBao** (KV v1/v2, token or AppRole with automatic renewal, namespaces), **Infisical** (cloud or self-hosted, Universal Auth) or **Bitwarden Secrets Manager** (cloud US/EU or self-hosted; pure Go, no SDK to install). Read at every instance start, recycle, task run and build, cached in memory for a few minutes, never written anywhere; the last known value keeps sites starting while a store is down; optional zero-downtime recycle when a secret changes

**Reverse proxy & request pipeline**
- HTTP/1.1, HTTP/2, WebSockets, SSE/streaming, `X-Forwarded-*` headers, trusted proxies
- **HTTP/3 (QUIC)**, off by default: one switch opens a UDP listener next to every HTTPS listener and advertises it with `Alt-Svc`; same sites, certificates and client certificate rules, 0-RTT off, graceful shutdown; access logs and metrics show the protocol. Allow UDP on the HTTPS ports in firewalls and cloud security groups in front of the server (setup's Windows Firewall rule already covers it)
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
- **Automatic IP banning** like fail2ban: failed sign-ins (sites and web console), 404 scans, rate-limit rejections, requests the firewall blocked and trap paths (`/wp-login.php`, `/.env`…) ban the client address across all sites, escalating for repeat offenders; IPv6 by /64, allow list, trusted proxies respected, per-site opt-out, bans survive restarts; unban from the web console or NodeHoster Manager
- **Web application firewall** like Azure Application Gateway's WAF, per site: SQL and NoSQL injection, cross-site scripting, path traversal, remote file inclusion, command injection (Unix and Windows), Node.js attacks (prototype pollution, template injection, `child_process`), PHP and Java (Log4Shell) payloads, scanners and protocol abuse, in the path, query string, cookies, headers and form/JSON/multipart bodies, after undoing double URL encoding, `%u`, HTML entities and full-width tricks. OWASP-CRS-style anomaly scoring with paranoia levels 1–3; **detect** mode logs what would be blocked, **block** answers 403 with a request ID; new sites start in detect, existing sites stay off until turned on. Exclusions by rule, category, path, argument, cookie or header, created from a blocked request in one click; blocks count towards IP banning. Pure Go (RE2, linear time), a few microseconds for an ordinary request, nothing at all for a site with it off
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
- **Client certificates (mutual TLS)** per HTTPS binding, like IIS "Require SSL" + client certificates: ignore, accept or require, trusted CA bundle, optional allow list of subjects or SHA-256 fingerprints, per-path requirement (`/admin` only); bindings sharing an IP and port each keep their own policy (chosen by SNI, 421 for mismatched requests); the application gets `X-Client-Verify`, `X-Client-Cert` (URL-escaped PEM, like nginx), `-Subject` and `-Fingerprint`, and client-supplied copies are always stripped
- **OCSP stapling** for certificates with a responder: responses verified, cached on disk (restarts staple without the CA), refreshed halfway to expiry with back-off, never stapled once expired; `cert.revoked` and Must-Staple warnings; status per certificate in both consoles. Let's Encrypt ended OCSP in 2025, so its certificates simply show "no responder"

**Deployments**
- Upload a `.zip` or deploy from **git** (token auth never exposed in the process list)
- Install/build commands, shared paths (`.env`, `uploads`) persisted across releases
- Releases kept side by side; **one-click rollback**; activation is a zero-downtime recycle
- **Deployment slots** like Azure App Service's: a `staging` slot runs its own release on its own instances and bindings (`staging.example.com`) with production's configuration, except slot settings (its own variables, production's variables marked sticky, instance count, bindings). Deploy to it (zip, git, webhook, CLI), try it, then **swap**: its instances restart with production's settings, warm-up paths are requested on every instance until they answer (200-399 by default), and production's traffic moves onto them at once — no cold start, in-flight requests finish on the old instances, which become the slot. Swapping again is the rollback; a failed warm-up changes nothing. Optional auto-swap after a deployment, `slot.swapped` / `slot.swap_failed` notifications, per-slot logs and metrics
- Push-to-deploy webhooks (GitHub, GitLab, Gitea signatures)
- **Preview deployments** like Azure Static Web Apps' pull request environments: every pull request (GitLab merge request) or matching branch (`feature/*`) gets a temporary site of its own at `pr-42.preview.example.com` or `feature-login.preview.example.com`, cloned from its site with one instance, its own shared folder and variable overrides (a separate `DATABASE_URL`) plus `PREVIEW`, `PREVIEW_BRANCH`, `PREVIEW_PR`, `PREVIEW_URL`; redeployed on every push and deleted with its releases, logs and certificate when the pull request is closed or merged, the branch deleted or after N days without a push. Forks are never built unless allowed; optional basic auth or IP allow list; per-host Let's Encrypt, a wildcard certificate from the store or one obtained through DNS-01; commit status with the preview's link on GitHub, GitLab and Gitea; `preview.*` notifications
- **Import sites** from IIS (`applicationHost.config`, or this server's IIS: iisnode apps, bindings, virtual directories, URL Rewrite, ARR proxies, redirects), an iisnode `web.config` or PM2 (`ecosystem.config.js`, `pm2 jlist`), reviewed before anything is created

**Administration**
- **NodeHoster Manager**: native desktop console laid out like IIS Manager (connections tree, lists, actions pane), over a local named pipe that needs no password, port or certificate — it keeps working when the web console does not
- **Status icon** in the notification area: green/amber/red service and site health, notifications for crashes, rapid-fail, certificate problems and resource alerts, start/stop the service
- Web console (React) with live status over Server-Sent Events
- **Multi-server management** like IIS Manager's "Connect to a Server": connect the web console to other NodeHoster servers (their web console URL and an API token created there, encrypted at rest, with a self-signed certificate pinned by its SHA-256 fingerprint after you compare it on first connect) and switch between them from the top bar — every page then operates that server through this one, live status, log tails and zip uploads included. A **Servers** page shows each one's reachability, version, sites running/failed, CPU and memory, checked every 30 s, with `remote.down` / `remote.up` notifications. Administrators choose which roles may use each connection; users never get more there than their role here (viewers only read), and every change made through a connection is in the local audit log. NodeHoster Manager (**Connect to a server…**), the command line (`nodehoster --server web02 site list`) and the PowerShell module (`Connect-NHServer`) connect the same way, with the token saved for your Windows account only (DPAPI)
- **Command line and PowerShell**: `nodehoster site|deploy|rollback|logs|events|task|waf|cert|backup ...` (tables, or `--json` for scripts) and a `NodeHoster` PowerShell module (`Get-NHSite`, `Publish-NHSite`, `Undo-NHDeployment`...) over the local admin pipe
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
opens the firewall for the program (every port it binds, TCP and UDP, so
HTTP/3 needs no extra rule on the server itself), starts it and checks that
it is running. Upgrades install over the top; sites and data are kept. A first
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
Node.js versions, web console users, banned addresses, the web application
firewall, alerts, the event and audit logs and backups; the middle pane shows the selected one (the server's
home is a dashboard of the service, CPU, memory and disk); the right pane
has its actions: start/stop/restart/recycle a site, deploy a `.zip` to it,
edit its bindings, environment, URL Rewrite rules, MIME types and basic
settings, browse it, follow its log live (pause, filter, save), see a
deployment's output and roll back a release, swap a deployment slot into
production (with a preview of what the swap does), run or cancel a scheduled
task, purge its response cache, install Node.js versions, reset a web
console user's password or two-factor authentication, ban and unban
addresses, silence or acknowledge an alert, switch a site's firewall between off, detect and block, see the
requests it blocked and exclude the rules behind a false positive, manage the mail queue, change where the web console listens,
back up to a file, run a scheduled backup now and see its history, restore
from a backup, and start or stop the service.

**Connect to a server…** (File menu, or the tool bar) adds another
NodeHoster server to the connections tree: its web console URL and an API
token created there (Account → API tokens). A certificate that is not
trusted (the self-signed one admin listeners get by default) is shown with
its SHA-256 fingerprint to compare with the server's before it is pinned.
The token is saved for your Windows account, protected by DPAPI, and the
command line shares these connections. Selecting the server's node shows
the same pages for it, over HTTPS instead of the pipe, with what the
token's role allows there: sites (start, stop, recycle, deploy a `.zip`,
live logs, settings), certificates, Node.js, mail, users, bans, events,
backups and updates. What needs the server's own computer — starting and
stopping its Windows service, changing where its web console listens, and
opening its data folder, log files or site folders — is disabled for remote
servers (use NodeHoster Manager on that server). File → Remove connection
forgets it.

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
deployments, slot swaps, certificates and resource alerts. A critical alert
that nobody silenced turns it amber.

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
nodehoster deploy|releases|rollback|logs <site> --slot staging   the same for a deployment slot
nodehoster slot list <site>                  slots: state, release, bindings, last swap
nodehoster slot swap <site> [<slot>] [--yes] [--no-wait]  warm up the slot and swap it into production
nodehoster slot start|stop|recycle <site> <slot>
nodehoster logs <site> [-n 100] [-f] [--access]
nodehoster events [-n 50] [--site x]
nodehoster task list <site>                  scheduled tasks, next run, last result
nodehoster task run <site> <task> [--no-wait]  run now, showing its output until it ends
nodehoster task runs <site> [<task>] [-n 20] | task cancel <site> <run-id>
nodehoster preview list <site>               preview deployments: pull request or branch, state, address
nodehoster preview deploy <site> <branch>    deploy a branch as a preview now
nodehoster preview redeploy|delete <site> <preview> [--yes]   <preview>: ID, PR number, host or branch
nodehoster alert list [--site x] | alert history [-n 50] [--site x]
nodehoster alert silence <alert-id> [--minutes 60] [--note ...] | alert ack <alert-id> | alert unsilence <alert-id>
nodehoster cert list | cert renew <id|name|domain>
nodehoster cert ocsp <id|name|domain>        ask the certificate's OCSP responder now
nodehoster tls                               TLS settings and the HTTP/3 (UDP) listeners
nodehoster tls set [--http3 on|off] [--http2 on|off] [--min-version 1.2|1.3]
nodehoster waf list                          each site's firewall mode, paranoia, exclusions, blocks
nodehoster waf mode <site> off|detect|block [--paranoia 1-3] [--threshold 5]
nodehoster waf events [<site>] [-n 50] [--action blocked] [--ip x] [--rule 942100] [--id <request-id>]
nodehoster waf exclude <site> [--path /admin/] [--rule 942100] [--category sqli] [--arg content] [--cookie x] [--header x]
nodehoster waf rules [--category sqli]
nodehoster backup <file>                     .zip: the full archive (encrypted with the backup
                                             passphrase, if set); any other name: the configuration (JSON)
nodehoster backup run | backup history [-n 10]  back up to the destinations now; recent backups
nodehoster restore <file> [--yes] [--passphrase-file <file>]
nodehoster update                            installed and newest version, automatic update settings
nodehoster update check | update install [--yes]
nodehoster update auto on|off [--time 03:00] [--days 0,6|all]
nodehoster deps                              Node.js, Git and the runtimes sites use: installed or missing
                                             (exit code 1 if missing)
nodehoster deps install [node] [git] [bun] [deno]  install what is missing (setup runs this for node and git)
nodehoster server add <name> <url> [--fingerprint <sha256>]  save a connection to another server
nodehoster server list | server test <name> | server remove <name>
nodehoster --server <name|url> [--token <token>] <command>   run a command on that server
nodehoster secrets list                      secret stores: references, values in memory, last read and error
nodehoster secrets test <store> [--ref <secret>]  sign in (and read a reference) without showing any value
nodehoster secrets check <site>              read every secret store reference of a site now
nodehoster runtime list                      Bun and Deno versions, Python interpreters, .NET runtimes
nodehoster runtime install bun|deno [version] [--default]  default: the newest release
nodehoster runtime remove bun|deno <version>
```

`--server` runs a command against another server's web console over HTTPS
instead of the local pipe (no elevation needed): a connection saved with
`nodehoster server add` (it asks for the token, and shows a certificate
that is not trusted with its fingerprint to confirm; the token is saved for
your Windows account with DPAPI, shared with NodeHoster Manager), or a URL
with the token in `--token` or, better, `NODEHOSTER_TOKEN`
(`NODEHOSTER_FINGERPRINT` pins a self-signed certificate). The token's role
on that server applies. `deps` and `server` work on this computer only.

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
`Get-NHCertificate`, `Update-NHCertificateOcsp`, `Get-NHTask`, `Start-NHTask
[-NoWait]`, `Get-NHTaskRun`, `Start-NHBackup`, `Get-NHTlsSetting`,
`Set-NHTlsSetting [-Http3] [-Http2] [-MinVersion]`, `Get-NHPreview`,
`Publish-NHPreview -Branch|-Preview`, `Remove-NHPreview`, `Get-NHAlert [-Pending]
[-History]`, `Set-NHAlertSilence [-Minutes]`, `Clear-NHAlertSilence`, `Get-NHSecretStore [-Test]`, `Test-NHSecretReference`, `Get-NHSlot`, `Switch-NHSlot` (and `-Slot` on
`Publish-NHSite`, `Get-NHRelease`, `Undo-NHDeployment`, `Get-NHLog`), `Get-NHRuntime`, `Install-NHRuntime bun|deno [-Version]
[-Default]`, `Get-NHWafEvent`, `Set-NHWafMode`, `Add-NHWafExclusion`. They take site names from the pipeline. `Connect-NHServer
<name|url> [-Token]` makes them target another server until
`Disconnect-NHServer`; `Get-NHServer` lists the saved connections:

```powershell
Get-NHSite | Where-Object State -eq 'failed' | Start-NHSite
Publish-NHSite shop -ZipPath .\build\shop.zip
Publish-NHSite shop -ZipPath .\build\shop.zip -Slot staging; Switch-NHSlot shop -Confirm:$false
Get-NHLog shop -Tail 50 | Where-Object Stream -eq 'stderr'
Get-Help Publish-NHSite -Examples
```

## Hosting a Node.js app

1. **Node.js** page → install an LTS version (or use the `node` on PATH).
2. **Sites → New site → Application**, runtime Node.js: app folder, entry script
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

To try a release before it goes live, add a **staging slot** (the site's
Slots tab): give it a binding (`staging.example.com`), mark production's
variables that must not reach it as slot settings (the database URL) and
give it its own values, then deploy to it (`nodehoster deploy shop --zip
app.zip --slot staging`). **Swap** puts it into production without a cold
start; swap again to go back. Scheduled tasks run in production only, and a
slot needs automatic ports (not a fixed port).

### Preview deployments

On a site deployed from git (**Deployments** tab: repository, production
branch and webhook secret), the **Previews** tab turns them on:

1. Pick a host pattern such as `pr-{number}.preview.example.com` and point a
   wildcard DNS record (`*.preview.example.com`) at the server. For HTTPS,
   prefer a wildcard certificate (from the store, or obtained through DNS-01
   with one of the DNS providers): a per-host Let's Encrypt certificate needs
   port 80 and counts against the CA's weekly limits.
2. Subscribe the site's push webhook to pull request events and branch
   deletions as well as pushes (GitHub: *Pull requests*, *Branch or tag
   deletion*; GitLab: *Merge request events*; Gitea: *Pull Request*,
   *Delete*). Signatures are verified exactly as for pushes.
3. Optionally: variables that differ in previews (a staging database),
   basic auth or an IP allow list so they are not public, and a token to
   report a commit status whose link opens the preview.

A preview is a site of its own (`shop pr-42`), listed under its site in the
console and in `nodehoster preview list`. Its configuration is made from
its site's at every deployment: one instance, one release kept, its own
shared folder (never the site's `uploads` or `.env`), no scheduled tasks
(they would run against the same data twice). Whoever may operate the site
may redeploy and delete its previews. Pull requests from forks are ignored
unless allowed, since their code would run on the server; at most
`maxPreviews` exist, a new one evicting the preview pushed to least
recently.

## Runtimes

An application or background worker site runs with one runtime, chosen in
the new-site wizard or the site's settings (`node.runtime` in the API; the
configuration object keeps the name `node` whatever the runtime, and a site
saved before runtimes existed is Node.js). Everything about supervising the
processes is the same for all of them; what differs is how they start:

| Runtime | Starts | Version |
|---|---|---|
| **Node.js** | `node <script>` or `npm run <script>` | installed on the Node.js page, pinned per site |
| **Bun** | `bun <script>` or `bun run <script>` | installed on the Runtimes page (or `bun` on PATH), pinned per site |
| **Deno** | `deno run <flags> <script>` or `deno task <name>` | installed on the Runtimes page (or `deno` on PATH), pinned per site |
| **Python** | `python <script>`, `python -m <module>`, or `python -m uvicorn\|hypercorn\|waitress <module:app>` | an interpreter found on the server: a version (`3.12`) or a `python.exe` |
| **.NET** | `dotnet <app.dll>` or `<app.exe>` (self-contained) | the `dotnet.exe` found on the server; the app's runtimeconfig picks the framework |
| **Custom command** | any program with its arguments | — |

- **Deno** grants a program nothing it is not told to: the consoles start a
  Deno site with `--allow-net --allow-env --allow-read` (edit them under
  Arguments); a task in `deno.json` sets its own.
- **Port**: every instance gets `PORT`. .NET sites also get
  `ASPNETCORE_URLS=http://127.0.0.1:<port>` (a site variable cannot move
  it; `ASPNETCORE_ENVIRONMENT` passes through as the site sets it), and the
  Python servers are started with `127.0.0.1` and the port. An instance is
  ready once it listens on its port, as for Node.js.
- **Stopping**: Node.js and Bun (entry scripts) stop through the agent.
  The others get a console **Ctrl+Break** (Windows has no SIGTERM; `SIGTERM`
  elsewhere): ASP.NET Core's generic host, uvicorn and Hypercorn shut down
  gracefully on it, Deno and Bun run their `SIGBREAK` listeners, and
  anything still running after the shutdown timeout is killed with its Job
  Object.
- **Metrics**: CPU and memory of the whole process tree for every runtime;
  heap and event-loop lag only where the agent reports them (Node.js, and
  Bun's heap), so the consoles show those columns only then.
- **Deployments** run the runtime's install command in each new release:
  `npm ci --omit=dev`, `bun install --production`, `deno install` (into the
  site's own `DENO_DIR`, which its processes use too), or for Python
  `python -m pip install -r requirements.txt` inside a **virtual
  environment created in the release** (`.venv` by default; one that came
  with the upload is replaced). A virtual environment per release means a
  rollback gets the packages it was deployed with and a running release is
  never changed under it; pip's download cache is shared by the site's
  releases, like npm's. .NET has no install step; set the build command to
  `dotnet publish -c Release -o publish` and the application to
  `publish\MyApp.dll`, or deploy a ready-built app. A step is skipped when
  the release has nothing for it (`package.json`, `deno.json`,
  `requirements.txt`/`pyproject.toml`).
- **Scheduled tasks** and **background workers** run with the site's
  runtime: a Python site's task is `python <script>` in its virtual
  environment; package scripts are for Node.js, Bun and Deno.
- **Python and .NET are not installed by NodeHoster**: install Python for
  all users from python.org (the service cannot see per-user installs) and
  the ASP.NET Core Hosting Bundle from dotnet.microsoft.com; the Runtimes
  page shows what was found and links there when nothing was.
  `nodehoster deps` lists every runtime a site uses as required.

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

### Secret stores

Instead of pasting a database password into a site, keep it in the secret
manager you already run and give the variable a **reference**: in the
variable editor choose **From secret store** (the vault icon), pick the
store and name the secret; **Test** asks the server to read it and says
whether it resolves, without ever showing the value. NodeHoster Manager and
the command line show a reference as `secretref:<store>/<secret>`, and
typing that as a variable's value (or pasting it in a `.env`) makes one. A
git deploy token can be a reference too (**Deployment settings → Read the
access token from a secret store**).

Add stores in **Settings → Secret stores** (administrators). Credentials
are encrypted at rest like other secrets and travel in passphrase-protected
backups. Each store has a **cache time** (default 5 minutes: a recycle of
eight instances asks the store once) and, optionally, a **watch interval**:
every running site whose secret changed is recycled without downtime.
Values live only in memory. If a store is unreachable when a process
starts, the last value read is used and a `secret.stale` event says so; a
secret never read (after a service restart, say) or deleted from the store
fails the start with a `secret.failed` event naming the variable, and a
recycle that fails keeps the running instances. Self-hosted servers with a
private CA: paste the CA certificate in the store; certificate checks
cannot be turned off.

- **HashiCorp Vault / OpenBao**: the address, the KV mount (`secret`) and
  version (2 unless it is a v1 engine), and either a token or AppRole
  (role ID + secret ID; NodeHoster renews its token at half its TTL and
  signs in again at the max TTL). A token given directly should be a
  periodic token; NodeHoster renews it. The policy needs `read` on
  `<mount>/data/<path>` (KV v2) or `<mount>/<path>` (v1). References are
  `<path>#<key>`: `app/prod#DB_PASSWORD`. Enterprise and OpenBao
  namespaces are supported.
- **Infisical**: create a machine identity with **Universal Auth**, give it
  (at least viewer) access to the project, and enter its client ID and
  secret, the project ID and the environment slug (`prod`). Leave the URL
  empty for Infisical Cloud (US), or enter `https://eu.infisical.com` or
  your self-hosted server. References are a secret name, optionally in a
  folder: `DB_PASSWORD`, `/backend/DB_PASSWORD`. Secret references and
  imports inside Infisical are expanded.
- **Bitwarden Secrets Manager**: create a machine account with read access
  to the projects, and an access token for it; choose the US or EU cloud,
  or enter the URL of your self-hosted Bitwarden server (NodeHoster uses
  its `/api` and `/identity`). References are secret IDs (the UUID shown in
  the web app). **Vaultwarden does not implement Secrets Manager**; it
  cannot be used here.

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
internal/proxy        listeners (TCP, QUIC), binding match, mTLS, request pipeline, load balancing
internal/waf          web application firewall: rules, normalisation, inspection, events
internal/certs        ACME (lego), import/export, renewal, OCSP stapling
internal/alerts       resource alert rules: evaluation, firing/resolving state machine
internal/deploy       zip/git deployments and releases
internal/preview      preview deployments: webhook events, names, commit statuses (lifecycle in core)
internal/nodeversions Node.js runtime installer
internal/runtimes     Bun/Deno installer, Python and .NET detection
internal/deps         Git lookup and MinGit installer (nodehoster deps)
internal/api          REST API (docs/API.md) and embedded web UI
internal/localapi     local pipes for the desktop programs (client; server in localserver)
internal/remote       other servers' web console API: TLS pinning, health checks, saved connections
internal/desktop      desktop presentation logic: health, formatting, icons
internal/auth         users, sessions, tokens, TOTP
internal/store        SQLite persistence
internal/secrets      AES-GCM + DPAPI
internal/secretstore  Vault/OpenBao, Infisical and Bitwarden Secrets Manager clients, value cache
internal/service      Windows service integration
web/                  React admin console
installer/            Inno Setup script
```

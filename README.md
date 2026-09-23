# NodeHoster

An IIS-style application server for **Node.js on Windows**. Sites and
bindings like IIS, application-pool–style process management, a built-in
reverse proxy and automatic HTTPS with Let's Encrypt — one self-contained
`nodehoster.exe` running as a Windows service, administered from a web console.

## Features

**Sites & bindings**
- Site types: **Node.js application**, **reverse proxy**, **static site**, **redirect**
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
- **Node.js version manager**: install any version from nodejs.org (SHA-256 verified), pin per site
- Environment variables with **secrets encrypted at rest** (AES-256-GCM, master key protected by DPAPI)

**Reverse proxy & request pipeline**
- HTTP/1.1, HTTP/2, WebSockets, SSE/streaming, `X-Forwarded-*` headers, trusted proxies
- Upstream load balancing: round-robin (weighted), least connections, IP hash, random; active and passive health checks
- HTTPS redirect, HSTS, gzip compression, request body limit, upstream timeout
- **URL Rewrite** rules (regex, `$1` substitutions): rewrite, redirect, block, custom response
- Request/response header rules, **IP & domain restrictions** (CIDR allow/deny)
- Basic authentication, per-client **rate limiting**
- **Maintenance mode** (like `app_offline.htm`) with IP bypass, custom error pages
- Access logs in combined format; default page for unbound host names

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

**Administration**
- Web console (React) with live status over Server-Sent Events
- Users with roles (admin / operator / viewer), **TOTP two-factor**, API tokens
- Audit log, event log, webhook notifications (Slack, Teams, Discord, generic)
- Metrics history and a Prometheus `/metrics` endpoint
- Backup & restore of the whole configuration

## Install

Download `NodeHoster-<version>-setup.exe` (release files are hosted on S3;
the GitHub release page links to them) and run it. The installer registers
the **NodeHoster** service (automatic, delayed start, restart on failure),
opens the firewall for the program, starts it and checks that it is running.
Upgrades install over the top; sites and data are kept.

Unattended install:

```
NodeHoster-1.2.3-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /TASKS="addtopath,firewall"
```

Exit code `0` means installed and running, `1` installed but the service did
not start (see `C:\ProgramData\NodeHoster\logs\nodehoster.log`).

Open **https://localhost:8484** (the console uses a self-signed certificate
until you pick one in Settings → Admin console). Sign in as `admin` with the
password from `C:\ProgramData\NodeHoster\initial-admin-password.txt`; you will
be asked to change it.

Portable use: unzip `nodehoster.exe` anywhere and run, from an elevated prompt,

```
nodehoster service install
nodehoster service start
```

### Command line

```
nodehoster run                     run in the foreground
nodehoster service install|uninstall|start|stop|status
nodehoster reset-password [user]   recover access
nodehoster version
nodehoster --data D:\NodeHoster run   use another data directory
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
| `logs\` | server log; `logs\sites\<id>\` app and access logs |

## Development

Requirements: Go 1.26+, Node.js 22+.

```
cd web && npm ci && npm run build && cd ..
go test ./...
(cd web && npm test)
go run ./cmd/nodehoster --data ./.devdata run
```

The UI dev server (`cd web && npm run dev`) proxies API calls to
`https://localhost:8484`. The server builds and runs on macOS and Linux too
(no job objects or DPAPI there), which is convenient for development.

CI runs on GitHub-hosted runners (`.github/workflows/build.yml`). Go tests on
Windows (including integration tests of job objects, the agent pipe and the
process manager) and Linux (with the race detector) and the web console
tests run in parallel; a packaging job then builds the binary, smoke-tests it
and builds the installer and a portable zip. Tags `vX.Y.Z` upload the release
to `s3://<bucket>/nodehoster/releases/<version>/` (never overwritten) and
create a GitHub release that links to it; nothing is stored on GitHub. The
upload tool is `tools/s3publish`; its configuration is described at the top
of the workflow.

## Architecture

```
cmd/nodehoster        entry point, CLI, admin listener
internal/core         composition root; site lifecycle
internal/procmgr      process supervisor (+ agent/ injected into apps)
internal/proxy        listeners, binding match, request pipeline, load balancing
internal/certs        ACME (lego), import/export, renewal
internal/deploy       zip/git deployments and releases
internal/nodeversions Node.js runtime installer
internal/api          REST API (docs/API.md) and embedded web UI
internal/auth         users, sessions, tokens, TOTP
internal/store        SQLite persistence
internal/secrets      AES-GCM + DPAPI
internal/service      Windows service integration
web/                  React admin console
installer/            Inno Setup script
```

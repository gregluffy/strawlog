# Straw Log

Straw Log manages logs on your own servers. It finds every log on a machine (Docker containers,
files in bind mounts and named volumes, `/var/log`, nginx, journald), shows what is using disk,
lets you tail and search them, and cleans them up with retention policies. Each server runs it
as a small container.

- **Finds logs automatically.** Groups them by app (compose project or container name), including logs in bind mounts and named volumes.
- **Disk view.** Size per app and source, growth over 24h, filesystem usage.
- **Health checks.** Containers with no `max-size`, files that never rotate, a large journal, apps logging mostly DEBUG, full disks. Each check comes with the fix.
- **Live tail and search.** Searches the files where they are, with no index and no extra copy on disk. Plain-text search reads about 300 MB/s on one core.
- **Retention.** Delete or gzip old rotations, archive and truncate oversized active files, vacuum journald. Every policy has a dry-run preview, and **each agent enforces its own policies**, so cleanup happens even when your dashboard is off.
- **One hub, many agents,** over pinned mutual TLS. Works on a LAN, over a VPN, or across the internet.
- **Notifications** to Discord, Microsoft Teams or any JSON webhook. You pick which cases go where, and there's a test button.
- **Users and roles:** viewer, operator, admin. Enforced by the API, not just hidden in the UI.

## How it fits together

```
 your laptop / home server                         servers, VMs, Proxmox guests
┌──────────────────────────────┐                  ┌──────────────────────────────┐
│ strawlog hub  :8080 (UI/API) │  mTLS 1.3, both  │ strawlog agent :7443         │
│  ├─ local node (optional)    │ ───────────────▶ │  ├─ discovery / search / tail│
│  └─ pinned agent fingerprints│  certs pinned    │  ├─ retention scheduler      │
└──────────────────────────────┘                  │  └─ docker-socket-proxy (RO) │
       ▲ browser (session cookie)                 └──────────────────────────────┘
```

The **hub connects to the agents**, never the other way round. Your hub can sit behind NAT on a laptop,
and servers only need port 7443 open, and only to the hub. Or nothing extra at all: the hub can reach an
agent through the server's existing SSH (see [Through SSH](#through-ssh-no-extra-port)).

The image is the same for both roles: `strawlog hub` or `strawlog agent`.

## Quick start

### 1. Everything on one machine

```bash
cd deploy/standalone
docker compose up -d --build
docker compose logs strawlog | grep -A2 SETUP     # one-time setup token
open http://localhost:8080
```

### 2. Dashboard on your machine, agents on servers

On your machine (it can also show its own logs: use the standalone compose instead):

```bash
cd deploy/hub && docker compose up -d --build
```

On each server or VM:

```bash
# copy deploy/agent/docker-compose.yml, set STRAWLOG_ALLOW_FROM to the hub IP / LAN range
docker compose up -d
docker compose logs strawlog-agent | grep -A1 "PAIRING CODE"
```

In the dashboard go to **Nodes → Add agent**, enter `server-ip:7443` and paste the code. Done.

> Building on every server isn't needed: `docker build -t registry.lan/strawlog:0.1.0 .`, push it, and
> set `STRAWLOG_IMAGE=registry.lan/strawlog:0.1.0` (or `docker save | ssh host docker load`).

### Through SSH (no extra port)

If the server already has SSH, the agent port doesn't need to be opened at all. The hub logs in over
SSH with a password or a private key (like an SSH tunnel in a database client) and reaches the agent
from inside the server. The agent's own mTLS still runs inside the tunnel.

1. In the agent compose file, use the "Via SSH" port line, `"127.0.0.1:7443:7443"`: only the server itself can
   connect to it, so no firewall rule is needed. Keep the `ports:` entry (the tunnel needs that fixed local
   address) and leave `STRAWLOG_ALLOW_FROM` at its default.
2. **Nodes → Add agent → Via SSH**: SSH host, username, password or private key, and the pairing code.
   The agent address defaults to `127.0.0.1:7443`.

The server's SSH host key is remembered on the first connect and checked on every connect after that.
The SSH server must allow TCP forwarding (OpenSSH does by default; `AllowTcpForwarding no` blocks it).
A dedicated login is a good idea; to stop it from doing anything but reach the agent, put this in
`/etc/ssh/sshd_config` on the server:

```
Match User strawlog
    AllowTcpForwarding local
    PermitOpen 127.0.0.1:7443
    X11Forwarding no
    PermitTTY no
    ForceCommand /bin/false
```

### Proxmox / home LAN

- Run the agent in each VM or LXC that runs Docker (Docker-in-LXC works the same way).
- Set `STRAWLOG_ALLOW_FROM=192.168.1.0/24`, or better, just the hub's IP.
- To reach the hub from other LAN devices, publish `8080:8080` and set `STRAWLOG_TLS=auto` (a self-signed HTTPS certificate is created), or put it behind your reverse proxy and set `STRAWLOG_SECURE_COOKIES=true`.
- For agents on the internet, put them behind WireGuard or Tailscale if you can, and bind the agent port to that interface: `"100.x.y.z:7443:7443"`.

## What it can see (mounts)

Straw Log reads the host under `/host` inside the container. The compose files mount:

| Mount | Why |
|---|---|
| `/var/log → /host/var/log` | syslog, nginx, apt/pacman, **journald** (`/var/log/journal`) |
| `/var/lib/docker/containers → /host/…` | container log sizes, fast search of `json-file` logs |
| `/var/lib/docker/volumes → /host/…` | logs written into **named volumes** |
| your stack dirs, e.g. `/srv → /host/srv` | logs written into **bind mounts** (`./logs:/app/logs`) |
| Docker API via `docker-socket-proxy` | container list, labels, mounts, stdout/stderr (read-only, GET only) |

Discovery inspects every container's mounts. For each bind or volume mount, it maps the host path
under `/host` and looks for log files (`*.log`, rotations, `logs/` directories). So **add the parent
directory of your compose projects** as a read-only mount and their bind-mounted logs show up under the right app.

If your Docker data-root isn't `/var/lib/docker`, mount it at the same path under `/host`.

### Container labels (optional)

```yaml
labels:
  strawlog.app: billing             # group under this app name
  strawlog.paths: /app/logs/*.log   # container paths to treat as logs (must be on a mount)
  strawlog.ignore: "true"           # hide this container
```

## Retention policies

A policy matches sources by app, kind (`docker`/`file`/`journal`), specific source, or path glob, and can:

| Rule | Files | Docker json-file | journald |
|---|---|---|---|
| Max active size (MB) | truncate in place (optionally keep a `.gz` archive first) | truncate in place | – |
| Max total size (MB) | delete oldest rotations, then truncate | truncate | `--vacuum-size` |
| Keep N rotated | delete older rotations | delete `.N` files | – |
| Delete older than N days | delete rotations | delete rotations | `--vacuum-time` |
| Gzip rotated | gzip rotations older than 1h | – | – |

The active file is **never deleted**, only truncated through the same inode, so running apps keep
writing to it. Symlinks and non-regular files are refused.

Every action is checked three times before it runs:

1. `STRAWLOG_ALLOW_WRITE=true` must be set on that node.
2. The path must be inside `STRAWLOG_WRITE_PATHS`.
3. The directory must actually be writable (mounted `:rw`).

If any check fails, the preview tells you which one.

Each policy has its own schedule: repeat every 15m–12h, or daily, weekly (pick a weekday) or
monthly (pick a day; 29–31 fall back to the month's last day) at a fixed time. Times are the node's
local time, so set `TZ` on the container if it shouldn't be UTC. Disabled policies keep their
schedule but only run when you press ▶ or *Apply all now*. Schedules run on the node itself and
survive restarts. The last 50 runs are kept and shown in the UI.

> Truncating at a size limit cleans up after the fact. The real fix for Docker is a limit at the source,
> and the Health page gives you the snippet:
> `logging: { driver: json-file, options: { max-size: "10m", max-file: "3" } }`.

## Notifications

Under **Notifications** (admins only), add any number of channels:

| Type | Where to get the URL |
|---|---|
| **Discord** | Server settings → Integrations → Webhooks → New Webhook → Copy Webhook URL |
| **Microsoft Teams** | In the channel: ⋯ → Workflows → *Post to a channel when a webhook request is received*. Old *Incoming Webhook* connector URLs also work, since messages are sent as Adaptive Cards. |
| **Generic JSON** | Any HTTP endpoint (n8n, Node-RED, your own service). It receives `{"source":"strawlog","events":[…]}` |

For each channel, choose the cases (and optionally which nodes) to be notified about:

| Case | When |
|---|---|
| Node offline / back online | an agent fails 2 checks in a row / answers again |
| New critical problem / new warning / resolved | the Health checks change (disk ≥ 90 %, unbounded container logs, never-rotated files, DEBUG logging, fast growth…) |
| Retention freed space / action failed | a scheduled or manual policy run did something, or failed to |
| Failed logins | 5+ failed dashboard logins in 5 minutes (at most once per 30 min) |
| User accounts changed | a user was created, deleted, disabled or changed role |

- **Send test** sends a sample message to the channel, saved or not, and shows the exact error if delivery fails.
- **Checks:** the hub checks all nodes every `STRAWLOG_NOTIFY_INTERVAL` (default 5 min) and only sends **changes**. The first check of a node only records what's already wrong, so a new channel isn't flooded. Events from one check are grouped into one message.
- **Delivery:** retried on 429 and 5xx, Discord's `Retry-After` is respected, and redirects are not followed.
- **Webhook URLs are secrets:** they're stored only in the hub's `/data`, never sent back to the browser (only a short hint), and masked in error messages.
- **Discord mentions are disabled,** so log text can't `@everyone`.
- **`STRAWLOG_PUBLIC_URL`:** set it to add an "Open Straw Log" link to every message.

> Notifications come from the **hub**. If the hub is your laptop and it's off, nothing is sent; the agents
> keep enforcing retention regardless. For always-on alerting, run the hub on a machine that stays up (a small VM is enough).

## Users and roles

| Role | Can |
|---|---|
| **viewer** | overview, sources, live logs, search, health, retention previews (dry runs) |
| **operator** | viewer + create, edit and run retention policies |
| **admin** | operator + add or remove nodes, manage users and notification channels |

- **First run:** the first admin is created at first-run setup (with the token from the logs), or from `STRAWLOG_ADMIN_USER` + `STRAWLOG_ADMIN_PASSWORD` if no users exist yet.
- **Upgrades:** an older single-password `hub.json` is migrated to a user named `admin`.
- **Changes take effect immediately:** disabling, deleting, demoting or resetting a user ends their sessions, because each request looks the user up again.
- **At least one enabled admin** always remains.
- **Login protection:** rate limited per IP and per username. Unknown usernames take as long to reject as wrong passwords.
- **Audit log:** operator and admin changes are written to the hub log (`level=INFO msg=audit user=… path=…`).

## Configuration

| Variable | Default | Role | |
|---|---|---|---|
| `STRAWLOG_LISTEN` | `:8080` / `:7443` | both | listen address |
| `STRAWLOG_DATA` | `/data` | both | state directory (keys, pairing, policies, history) |
| `STRAWLOG_HOST_ROOT` | `/host` (image) | both | where host paths are mounted; empty when running natively |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | both | `tcp://docker-proxy:2375` in the compose files |
| `STRAWLOG_DOCKER` | `true` | both | disable Docker integration |
| `STRAWLOG_EXTRA_PATHS` | | both | `app=/host/path/*.log,/opt/x/logs` (host paths, globs allowed) |
| `STRAWLOG_ALLOW_WRITE` | `false` | both | allow retention actions on this node |
| `STRAWLOG_WRITE_PATHS` | `/var/log,/var/lib/docker/containers,/var/lib/docker/volumes` | both | host path prefixes retention may touch |
| `STRAWLOG_SAMPLE_INTERVAL` | `15m` | both | size history sampling |
| `STRAWLOG_RETENTION_INTERVAL` | `1h` | both | default repeat interval for policies saved without a schedule |
| `STRAWLOG_ALLOW_FROM` | (all) | agent | IPs/CIDRs allowed to connect; others are dropped before TLS |
| `STRAWLOG_LOCAL` | `true` | hub | also show this machine |
| `STRAWLOG_LOCAL_NAME` | hostname | hub | display name of the local node |
| `STRAWLOG_ADMIN_USER` | `admin` | hub | username for the bootstrap admin |
| `STRAWLOG_ADMIN_PASSWORD` | | hub | creates the first admin if there are no users yet (otherwise first-run setup with a token) |
| `STRAWLOG_PUBLIC_URL` | | hub | dashboard URL used for links in notifications |
| `STRAWLOG_NOTIFY_INTERVAL` | `5m` | hub | how often the hub checks nodes for notification events |
| `STRAWLOG_TLS` | `off` | hub | `auto` (self-signed) or `/path/cert.pem:/path/key.pem` |
| `STRAWLOG_SECURE_COOKIES` | `false` | hub | set when a TLS reverse proxy is in front |
| `STRAWLOG_DEBUG` | `false` | both | debug logging |

## CLI

```bash
docker exec strawlog-agent strawlog agent pair       # new one-time pairing code
docker exec strawlog-agent strawlog agent hubs       # list paired hubs
docker exec strawlog-agent strawlog agent unpair     # revoke all hubs (or: unpair <fp-prefix>)
docker exec -it strawlog strawlog hub set-password greg   # reset a user's password (creates an admin if missing)
```

## Security model

**Hub ↔ agent**
- TLS 1.3 only, with mutual certificates (self-signed ECDSA P-256), each side pinned by SHA-256 fingerprint.
- The pairing code `slp1.<agent-fingerprint>.<token>` carries the agent's fingerprint, so the first connection is already pinned and there is no trust-on-first-use window.
- Tokens are 128-bit, single use, valid 24h, stored hashed, and rate limited.
- After pairing, the agent answers only the pinned hub certificate(s). Anything else gets a 403, or is dropped before the handshake if it's outside `STRAWLOG_ALLOW_FROM`.

**Agent least privilege**
- The container is read-only, with `no-new-privileges`, `cap_drop: ALL` + `DAC_READ_SEARCH` only, plus memory, PID and CPU limits.
- Docker is reached only through `docker-socket-proxy` with `CONTAINERS=1, POST=0`, on an internal network: list, inspect and logs only. Never exec, create or start.
- Host mounts are `:ro` unless you choose to allow cleanup.

**Dashboard**
- Users with viewer, operator and admin roles, checked server-side on every request.
- PBKDF2-SHA256 (600k iterations) password hashes.
- Random session tokens, kept server side as hashes, in an HttpOnly, SameSite=Strict cookie (`__Host-` prefixed under TLS).
- Every state-changing request needs a custom header and a same-origin check (CSRF).
- Login rate limiting per IP and per username, strict CSP, `X-Frame-Options: DENY`.
- The first-run setup needs a token printed in the logs, so nobody else on the LAN can claim it first.
- Bound to `127.0.0.1` by default.

**Notes**
- Keep the hub's `/data` volume private. It holds the client key that agents trust, the webhook URLs, and the SSH passwords / private keys of nodes added via SSH (in `hub.json`, mode 0600). Those credentials are never sent to the browser.
- Anyone with access to the Docker socket on a host is root there anyway, so the socket proxy limits what *Straw Log* can do, not what the host's admins can.

## Efficiency

- Static Go binary; the only dependency outside the standard library is `golang.org/x/crypto/ssh`. Idle memory is about 12–20 MB. The image is about 155 MB, mostly Debian plus journalctl.
- Discovery is cached (60s) and walks are depth- and entry-limited.
- Search streams line by line with bounded memory: a ring of the newest N matches, and lines capped at 16 KB.
- Agent responses are gzipped. Live tails are batched (150–200 ms flushes) over HTTP/2.
- Size history is kept 8 days per source in a gzipped JSON file.

## Development

```bash
cd web && npm install && npm run dev        # UI on :5173, proxies /api to :8080
STRAWLOG_DATA=./.devdata STRAWLOG_HOST_ROOT= go run ./cmd/strawlog hub
go test ./...
docker build -t strawlog:dev .
```

Layout: `internal/core` (discovery, read, search, retention, health), `internal/agent` (mTLS server, pairing),
`internal/hub` (auth, node registry, API, embedded UI), `web/` (Svelte 5 dashboard).

## Known limits / next steps

- Plain files have no reliable timestamps, so "since" filters only Docker and journald.
- Regex search is about 10× slower than plain text (Go RE2). A literal-prefix prefilter would help.
- Logs inside a container's own writable layer (not on a mount) aren't discovered. Mount a volume for them.
- Notifications are sent by the hub, so they stop while the hub is down. Agent-side alerting could come later.
- Not yet built: saved searches, SSO (OIDC), prebuilt multi-arch images on a registry.

## License

GPL-3.0. See [LICENSE](LICENSE).

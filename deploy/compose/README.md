# Agent Fleet — on-prem deployment (Docker Compose)

Run a self-hosted Agent Fleet: your team logs in with Google, each member gets an
isolated container running Claude Code, driven from the browser. **One company =
one deployment** on your own infrastructure, with your own Claude seats.

This directory is the whole deployment surface:

| File | Purpose |
|------|---------|
| `docker-compose.yml` | `control-plane` (CP) + `caddy` (auto-TLS) services |
| `Caddyfile` | reverse proxy + Let's Encrypt (ACME); self-signed fallback |
| `.env.example` | all configuration (copy to `.env`) |
| `backup.sh` / `restore.sh` | data backup & disaster recovery |
| `load-images.sh` | air-gapped image import (`docker save`/`load`) |
| `release.sh` | build a versioned release bundle (maintainers) |

## Prerequisites

- A Linux host with **Docker** (Engine + `docker compose`).
- A **public domain** with a DNS A/AAAA record pointing at this host (for TLS).
  Internal-only? Use the `tls internal` fallback in `Caddyfile`.
- A **Google OAuth 2.0** Web client (for login).
- Your team's **Claude** login (each user logs in with their own seat from the
  Console after first launch — bring your own; company Team/Enterprise seats
  recommended over personal Pro/Max).

## Quick start

```bash
cd deploy/compose
cp .env.example .env
# 1) generate secrets and put them in .env:
head -c 32 /dev/urandom | base64   # -> AF_MASTER_KEY   (store a copy in a vault!)
head -c 32 /dev/urandom | base64   # -> AF_COOKIE_SECRET
# 2) set DOCKER_GID to the host docker group:
getent group docker | cut -d: -f3  # -> DOCKER_GID
# 3) fill in PUBLIC_DOMAIN / PUBLIC_BASE_URL / GOOGLE_OAUTH_* /
#    AF_OAUTH_ALLOWED_DOMAINS / SUPER_ADMIN_EMAILS / DATA_DIR in .env
mkdir -p "$(grep -E '^DATA_DIR=' .env | cut -d= -f2)"

docker compose up -d --build     # or plain `up -d` if pulling prebuilt images
docker compose logs -f control-plane
```

Then open `https://<PUBLIC_DOMAIN>`, sign in with a `SUPER_ADMIN_EMAILS` account,
and launch a workspace.

### Google OAuth setup

In Google Cloud Console → *APIs & Services* → *Credentials* → create an **OAuth
client ID** (Web application). Add an **Authorized redirect URI**:

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

Copy the client ID/secret into `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET`.

## First administrator, tenants, members

- The emails in `SUPER_ADMIN_EMAILS` become **super_admin** on first login.
- A super_admin sees a ⚙ **Admin** panel in the Console: create tenants, add
  members, set quotas / idle-stop timeouts, view usage, force-stop.
- Single-tenant is the default: everyone lands in the built-in `default` tenant
  with zero friction. Create additional tenants only if you need hard separation
  (e.g. departments) — each membership gets a fully isolated workspace.
- `AF_PROVISION=auto` (default) auto-admits any allow-listed login into the
  default tenant. Set `AF_PROVISION=invite` to require an admin to add people.

## Backup & restore

Back up regularly (cron the script):

```bash
deploy/compose/backup.sh              # -> ./backups/agent-fleet-<ts>.tar.gz
OUT_DIR=/mnt/backups KEEP=14 deploy/compose/backup.sh
```

`backup.sh` briefly stops the CP + Caddy for a consistent SQLite snapshot (user
workspaces stay up — they are not compose services), then tars `${DATA_DIR}`
(DB + homes + `secrets.enc` + wrapped DEKs + Caddy certs; the re-provisionable
`shared/jvm` is excluded).

> ⚠️ **`AF_MASTER_KEY` is NOT in the backup** (it lives in `.env`). Store it in a
> **separate vault**. Losing it makes every backup undecryptable (crypto-shred).
> The archive itself contains plaintext Claude state — protect it.

Restore (clean host or after data loss):

```bash
# 1) recreate .env with the SAME AF_MASTER_KEY (from your vault)
# 2) restore the archive:
deploy/compose/restore.sh /mnt/backups/agent-fleet-<ts>.tar.gz
# 3) bring it up; Start each workspace from the Console to rehydrate containers
docker compose up -d
```

The parent path may differ from the source (e.g. `/srv` → `/mnt`): the CP
re-roots each workspace onto the current `DATA_DIR` at start. Keep the `DATA_DIR`
**basename** equal to the archive's top-level dir (restore.sh checks this).

## Upgrade

```bash
# edit .env: VERSION=<new tag>
docker compose pull            # or rebuild: docker compose build
docker compose up -d
```

Schema migrations are embedded in the CP and applied automatically on start
(forward-compatible). **Downgrades are not supported** — snapshot with `backup.sh`
before upgrading. Read the release notes for any breaking changes.

## Air-gapped install

On a networked machine, build/pull the images and export them:

```bash
docker save agent-fleet/control-plane:$VERSION agent-fleet/workspace:$VERSION \
  | gzip > agent-fleet-images-$VERSION.tar.gz
```

On the target host:

```bash
deploy/compose/load-images.sh agent-fleet-images-<version>.tar.gz
docker compose up -d
```

The workspace image installs Claude at container start by default (always latest).
For fully offline hosts set `CLAUDE_INSTALL=0` (via `WS_ENV`) and use an image
with Claude baked in.

## DooD: the three constraints (read if "it starts but silently doesn't work")

The CP is containerized but drives the **host** Docker daemon (docker-out-of-docker)
via the mounted socket. Three things must line up; the compose file already
encodes them, but if you customize it, keep them:

- **(A) `network_mode: host`** — the CP publishes workspaces on `127.0.0.1:<port>`
  via the host daemon, so it must share the host loopback to reach them. Caddy is
  host-net too, so `reverse_proxy 127.0.0.1:8099` just works.
- **(B) `${DATA_DIR}:${DATA_DIR}` (same absolute path)** — the CP passes host
  paths to the host daemon for workspace `-v` mounts, so `DATA_DIR` must resolve
  to the same absolute path inside the CP. A mismatch = workspaces mount an empty
  home.
- **(C) `user: "1000:1000"` + `group_add: [${DOCKER_GID}]`** — homes are created
  owned by uid 1000 (the workspace `dev` user), and the CP needs the host docker
  group to use the socket. A wrong `DOCKER_GID` = "permission denied" on the socket.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| CP won't start | `docker compose logs control-plane`; `curl -s http://127.0.0.1:8099/healthz` should print `ok` |
| "permission denied" on docker.sock | `DOCKER_GID` matches `getent group docker`? |
| Workspace starts but home is empty | DooD (B): `DATA_DIR` identical inside/outside; same path on restore |
| Can't reach a started workspace | DooD (A): CP + Caddy both `network_mode: host` |
| Login always denied | allowlist empty (fail-closed) — set `AF_OAUTH_ALLOWED_DOMAINS`/`_EMAILS` |
| TLS not issued | DNS A/AAAA → this host? ports 80/443 reachable? Let's Encrypt rate limit? |
| Redirect URI mismatch | Google console URI == `<PUBLIC_BASE_URL>/oauth2/callback` |

## Security notes

- **`docker.sock` = host root.** The CP is host-root-equivalent within this
  deployment; a CP/host compromise breaks isolation for **this** deployment only
  (companies are separate deployments). Restrict who can operate the host. To
  narrow the Docker API surface, front the socket with a filtering proxy.
- **`AF_MASTER_KEY`** — separate vault, independent backup, never in the data dir.
- See `../../SECURITY.md` for the full threat model and how to report issues.

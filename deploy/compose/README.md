# Agent Fleet — on-prem deployment (Docker Compose)

Agent Fleet is a self-hosted web console for running AI coding agents (Claude Code,
Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI, Kiro, OpenCode) as a managed fleet. Each member gets an
isolated workspace container — with cgroup CPU/memory quotas — holding a persistent
home and git working copies, and drives agent sessions from the browser. A Go
control plane orchestrates the workspaces; deployment targets include this on-prem
Docker Compose stack, AWS ECS (CloudFormation), and a Docker-less native runtime.

This runbook covers the Docker Compose target: your team signs in with your
company's IdP — Google, Microsoft Entra ID, Okta, Keycloak, Auth0, Cognito,
GitLab — and works on **one deployment per company**, on your own infrastructure,
with your own agent seats (e.g. your Claude subscription).

This directory is the whole deployment surface:

| File | Purpose |
|------|---------|
| `docker-compose.yml` | `cp` (control-plane) + `caddy` (auto-TLS) services |
| `Caddyfile` | reverse proxy + Let's Encrypt (ACME); self-signed fallback |
| `.env.example` | all configuration (copy to `.env`) |
| `backup.sh` / `restore.sh` | data backup & disaster recovery |
| `load-images.sh` | import images from a local tar — only for hosts that cannot reach a registry |
| `release.sh` | build a versioned release bundle (maintainers) |

Released images are published to **GHCR** as
`ghcr.io/k-k1/agent-fleet/control-plane:<version>` and
`ghcr.io/k-k1/agent-fleet/workspace:<version>`; `.env` resolves them through
`REGISTRY` + `VERSION`. The packages are published public, so pulling them needs
no registry login. Point `REGISTRY` at your own mirror to pull from elsewhere.

## Prerequisites

- A Linux host with **Docker** (Engine + `docker compose`).
- A **public domain** with a DNS A/AAAA record pointing at this host (for TLS).
  Internal-only? Use the `tls internal` fallback in `Caddyfile`.
- An **IdP client for login**: a Google OAuth 2.0 Web client, or an OIDC app
  registration at Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab. Only one
  redirect URI is ever registered, whichever (and however many) you use.
  SAML-only IdP (HENNGE One / TrustLogin / CloudGate …)? Front the CP with
  oauth2-proxy or Keycloak and run `AUTH=proxy` instead.
- Your team's **Claude** login (each user logs in with their own seat from the
  Console after first launch — bring your own; company Team/Enterprise seats
  recommended over personal Pro/Max).
- (Optional, for the `agy` agent kind) a host CPU that exposes **RDRAND**
  (`grep -w rdrand /proc/cpuinfo`). The Antigravity CLI is a FIPS build that
  aborts at startup without it (kernel-masked / BIOS-disabled counts as
  missing — seen on AMD Ryzen Embedded). Hosts without RDRAND still run
  everything else; the Console just hides `agy` from the agent selector
  ([decision 0008](../../docs/decisions/0008-antigravity-cli-agent-kind.md)).

## Quick start

> Starting from a published release? The `install-compose.sh` helper in the
> [distribution repo](https://github.com/k-k1/agent-fleet-dist) fetches + verifies
> the bundle, extracts it and pulls the images from GHCR, leaving you at the `cp
> .env.example .env` step below. This runbook is the manual/from-source path.

```bash
cd deploy/compose
cp .env.example .env
# 1) generate secrets and put them in .env:
head -c 32 /dev/urandom | base64   # -> AF_MASTER_KEY   (store a copy in a vault!)
head -c 32 /dev/urandom | base64   # -> AF_COOKIE_SECRET
# 2) set DOCKER_GID to the host docker group:
getent group docker | cut -d: -f3  # -> DOCKER_GID
# 3) fill in PUBLIC_DOMAIN / PUBLIC_BASE_URL / your IdP (GOOGLE_OAUTH_* and/or
#    AF_OIDC_*) / AF_OAUTH_ALLOWED_DOMAINS / SUPER_ADMIN_EMAILS / DATA_DIR in .env
mkdir -p "$(grep -E '^DATA_DIR=' .env | cut -d= -f2)"

# 4) build the per-user workspace image (compose builds only the CP; the CP
#    launches workspaces from WS_IMAGE, agent-fleet/workspace:dev by default):
docker build -t agent-fleet/workspace:dev ../../workspace

docker compose up -d --build     # from source
docker compose logs -f cp
```

Running a published release instead of building? Leave `REGISTRY`/`VERSION`/
`WS_IMAGE` at the values the release bundle ships with and pull rather than build
— skipping step 4:

```bash
docker compose pull                                  # cp + caddy
docker pull "$(grep -E '^WS_IMAGE=' .env | cut -d= -f2)"   # workspace (see below)
docker compose up -d
```

The workspace image is **not a compose service** — the CP launches it per user
with `docker run` — so `docker compose pull` does not fetch it. Pulling it up
front is optional (the first workspace start would pull it on demand) but avoids
a multi-minute wait for whoever presses Start first.

Then open `https://<PUBLIC_DOMAIN>`, sign in with a `SUPER_ADMIN_EMAILS` account,
and launch a workspace.

### Login IdP setup

The condensed version is below. For the walk-through that includes **what to create
on the IdP's side** (Google Cloud Console, the Entra app registration, a GitHub
OAuth App), how to verify it, and the per-IdP failure modes, see
[docs/guide/operator/05-login-idp.md](../../docs/guide/operator/05-login-idp.md).

Whichever IdPs you enable, the redirect URI you register is always this one — it
does not multiply with the number of providers:

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

**Google.** In Google Cloud Console → *APIs & Services* → *Credentials* → create
an **OAuth client ID** (Web application), add the redirect URI above, and copy the
client ID/secret into `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET`.

**Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab.** Register a
confidential *web* app at the IdP with the same redirect URI, then in `.env`:

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<tenant-guid>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<client-secret-value>
AF_OIDC_ENTRA_TRUST=issuer                       # required; see below
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン     # optional button labels
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

- **`_TRUST` has no default on purpose.** It states why this IdP's email may be
  believed, and the login page is the front door: `email_verified` accepts only
  the addresses the IdP marks verified (Google, most Okta / Keycloak / Auth0
  setups); `issuer` says the issuer is pinned to one tenant, so its directory's
  addresses are authoritative. Entra ID never emits `email_verified`, so `issuer`
  is the correct value there. A provider with no `_TRUST` is disabled at startup.
- ★ **Pin the Entra issuer to your tenant guid.** With the `/common/` or
  `/organizations/` endpoint, everyone on earth with a Microsoft account reaches
  your login, and personal accounts can change their own email — which would make
  the email allowlist meaningless. The CP refuses to start on those endpoints
  unless `AF_OIDC_ENTRA_ALLOWED_TIDS` names the tenants you accept.
- Set several ids in `AF_OIDC_PROVIDERS` (`entra,okta`) to offer several buttons.
  A provider whose settings are incomplete is disabled with a warning in the CP
  log — one broken IdP never locks the whole company out — and the CP only
  refuses to start when no provider at all is usable.
- Pressing a different button lands the same person in the same workspace as long
  as the IdPs hand out the same email address. From then on that IdP account is
  remembered, so a later rename at the IdP no longer moves anyone's home
  directory. Two **different** addresses stay two people, and cannot be merged
  afterwards — someone who signs in with an address nobody has used here before is
  shown a page saying a new workspace was created.

**GitHub.** Not OIDC, so it is configured separately — and what authorizes the
sign-in is membership in an org you list:

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs    # required; also what enables the button
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com    # strongly recommended; see below
```

- The OAuth App is the same one the Console's GitHub "Connect" button uses
  (`GITHUB_OAUTH_CLIENT_ID`) — just add the redirect URI
  `<PUBLIC_BASE_URL>/oauth2/callback` to it. Set `AF_GITHUB_LOGIN_CLIENT_ID` /
  `AF_GITHUB_LOGIN_CLIENT_SECRET` instead if you would rather the login use an app
  of its own (approving an app for an org approves it for both flows).
- ★ **If your org restricts third-party OAuth apps, an org owner must approve the
  app.** Until they do, the membership check sees nothing and *everybody* is
  rejected — with settings that look correct.
- ★ **Set `AF_GITHUB_ALLOWED_DOMAINS` too.** GitHub gives us the account's primary
  **verified** address, which for most people is a personal one — and a personal
  address is a different person here, so they land in a new empty workspace
  instead of their own. Refusing at the door is kinder than letting someone work
  in a workspace they never meant to create.
- Membership is re-checked through the API, cached for `AF_GITHUB_MEMBERSHIP_TTL`
  (10m) and, if GitHub is unreachable, honored for `AF_GITHUB_MEMBERSHIP_GRACE`
  (1h) past the last positive answer. The cache is in memory, so after a CP
  restart these sessions are asked to **sign in again** — they are not rejected,
  and GitHub usually completes that round trip without prompting.

### Git provider OAuth (GitHub / Bitbucket) — optional

Login above is for the **console**. The one-click "Connect via OAuth" buttons for
cloning private repos are configured **in the Console, per tenant** — not in `.env`
(docs/log/71). A tenant administrator opens **Tenant settings → Integrations → Git
provider OAuth** and registers:

- **GitHub** (device flow) — `client_id` only (not a secret; the OAuth App must have
  "Enable Device Flow" ON; no callback URL needed).
- **Bitbucket** (auth code) — Key + Secret; the consumer's Callback URL must equal
  `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback`, which that screen shows you.

`GITHUB_OAUTH_CLIENT_ID` / `BITBUCKET_OAUTH_KEY` / `BITBUCKET_OAUTH_SECRET` in `.env`
are **not read** for this (the first still means the GitHub sign-in app). Users can
always paste a token instead, with nothing registered.

## First administrator, tenants, members

- The emails in `SUPER_ADMIN_EMAILS` become **super_admin** on first login.
- A super_admin sees a ⚙ **Admin** panel in the Console: create tenants, add
  members, set quotas / idle-stop timeouts, view usage, force-stop.
- Single-tenant is the default: everyone lands in the built-in `default` tenant
  with zero friction. Create additional tenants only if you need hard separation
  (e.g. departments) — each membership gets a fully isolated workspace.
- **`AF_PROVISION=invite` is what `.env.example` ships with**: nobody gets a
  workspace until an admin adds them. It works from the first boot, since a
  `SUPER_ADMIN_EMAILS` account reaches the Admin panel with no membership of its
  own; everybody else lands on a page telling them which address to quote to you.
  Set `AF_PROVISION=auto` instead to auto-admit any allow-listed login into the
  default tenant — reasonable for a small single-team install, but then
  `AF_OAUTH_ALLOWED_*` is the only thing between a stranger and a workspace.
  (The *built-in* default is still `auto`, so an existing `.env` that says nothing
  keeps behaving exactly as before — only new installs start closed.)
- **An invitation is itself permission to sign in.** Somebody you add in the
  Admin panel gets past the login without also being in `AF_OAUTH_ALLOWED_*`, so
  an invite-run deployment keeps one roster rather than two lists that drift.
- Each tenant can carry **login rules** (Admin → tenant → Login rules): which
  sign-in methods it accepts, which domains join it automatically, and which
  domains may be invited. It also gets its own page at
  `<PUBLIC_BASE_URL>/login/<slug>`, showing only the methods that tenant accepts —
  that URL is what you hand to a new member (there is no invitation email).
- **Offboarding is "Remove member"** (Admin → tenant → member). Disabling the
  account at the IdP does not end a session that already exists — the signed
  cookie is valid for up to `AF_SESSION_TTL`. Removing them from the roster (or
  the allowlist) takes effect on their very next request. To cut every session at
  once, rotate `AF_COOKIE_SECRET` and restart.
- `SUPER_ADMIN_EMAILS` is read **once at startup** and is the only source of
  truth: on restart the CP also revokes the role from accounts no longer listed.

## Backup & restore

Back up regularly (cron the script):

```bash
deploy/compose/backup.sh              # -> deploy/compose/backups/agent-fleet-<ts>.tar.gz
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
# edit .env: VERSION=<new tag>, and point WS_IMAGE at the matching workspace tag
# (WS_IMAGE is an independent variable — it is not derived from VERSION)
docker compose pull            # or rebuild: docker compose build
# the workspace image is not a compose service — pull/rebuild it separately:
docker pull <WS_IMAGE>         # or rebuild: docker build -t <WS_IMAGE> ../../workspace
docker compose up -d
```

Schema migrations are embedded in the CP and applied automatically on start
(forward-compatible). **Downgrades are not supported** — snapshot with `backup.sh`
before upgrading. Read the release notes for any breaking changes.

## Air-gapped install

No image tarball is published any more — images are distributed through GHCR
([ADR 0037](../../docs/decisions/0037-registry-policy.md)). A host that cannot
reach a registry has two options: mirror `ghcr.io/k-k1/agent-fleet/*` into an
internal registry and point `REGISTRY` at it, or carry the images in by hand.

For the hand-carry path, build and export them on a networked machine:

```bash
VERSION=<version> deploy/compose/release.sh --save
#   -> deploy/compose/dist/agent-fleet-images-<version>.tar.gz  (docker save, gzip)
```

Copy that tar plus the bundle to the target host and import it there:

```bash
deploy/compose/load-images.sh agent-fleet-images-<version>.tar.gz
docker compose up -d
```

`release.sh --save` tags the images `agent-fleet/{control-plane,workspace}:<version>`
(override with `REGISTRY=`), so set `REGISTRY=agent-fleet` in the target's `.env`
to match what was loaded.

Note that a fleet is not usable offline just because the images are local. The
default workspace image is the lean variant (`BAKE_AGENT_CLIS=0`): it ships
without the agent CLIs and installs them at container start, **pinned via the
image's `versions.json`** (later starts keep the pin; following latest is the
self-update opt-in's job). For fully offline hosts build an image with the CLIs
baked in (`BAKE_AGENT_CLIS=1`) and set `CLAUDE_INSTALL=0` (via `WS_ENV`) — and
the agents still need to reach their model endpoints to do anything.

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
| CP won't start | `docker compose logs cp`; `curl -s http://127.0.0.1:8099/healthz` should print `ok` |
| "permission denied" on docker.sock | `DOCKER_GID` matches `getent group docker`? |
| Workspace starts but home is empty | DooD (B): `DATA_DIR` identical inside/outside; same path on restore |
| Can't reach a started workspace | DooD (A): CP + Caddy both `network_mode: host` |
| Login always denied | allowlist empty **and nobody invited yet** (fail-closed) — set `AF_OAUTH_ALLOWED_DOMAINS`/`_EMAILS`, or invite somebody |
| "this tenant needs a different sign-in" | the tenant's Login rules → sign-in methods excludes the IdP they used; send them `<PUBLIC_BASE_URL>/login/<slug>` |
| A removed person still has access | the IdP block doesn't end their session — remove the member (Admin → tenant → member) or rotate `AF_COOKIE_SECRET` |
| TLS not issued | DNS A/AAAA → this host? ports 80/443 reachable? Let's Encrypt rate limit? |
| Redirect URI mismatch | the IdP's registered URI == `<PUBLIC_BASE_URL>/oauth2/callback` |
| A login button is missing | that provider was disabled at startup — `docker compose logs cp \| grep -i "login provider"` names the missing setting |
| CP exits on an Entra config | `/common/` or `/organizations/` issuer without `AF_OIDC_<ID>_ALLOWED_TIDS` — pin the issuer to your tenant guid |
| Every GitHub login is rejected | the org restricts third-party OAuth apps and nobody approved this one (`docker compose logs cp \| grep "returned 403"`), or the person's primary verified address is outside `AF_GITHUB_ALLOWED_DOMAINS` |
| GitHub users are asked to sign in again after a restart | expected: the org-membership cache is in memory. They are re-verified, not rejected |

## Security notes

- **`docker.sock` = host root.** The CP is host-root-equivalent within this
  deployment; a CP/host compromise breaks isolation for **this** deployment only
  (companies are separate deployments). Restrict who can operate the host. To
  narrow the Docker API surface, front the socket with a filtering proxy.
- **`AF_MASTER_KEY`** — separate vault, independent backup, never in the data dir.
- See `../../SECURITY.md` for the full threat model and how to report issues.

## Disclaimer — autonomous agent execution

The agents run commands, edit files, and commit/push on behalf of your members —
including **unattended** (scheduled runs that wake a stopped workspace), in
**permission-bypassing modes**, and through **shell / SSM sessions that run the
strings sent verbatim**. Such actions can be destructive or irreversible and can incur
charges on the connected AI-provider and cloud accounts. The operator and each member
are responsible for the workspaces, credentials, repositories, and infrastructure they
connect, and for reviewing what the agents do; use least-privilege credentials, keep
backups, and prefer the approval gates for destructive actions. This software is
distributed under the **Apache License 2.0** and is provided **"AS IS", WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND**; the authors accept **no liability** for any
damage, data loss, downtime, or cost arising from its use. See `LICENSE`, and `NOTICE`
for bundled-OSS attribution.

Official releases come only from <https://github.com/k-k1/agent-fleet-dist>. If you
pass this bundle on, Apache-2.0 §4(d) requires you to keep the notices in `NOTICE`
with it — that URL included — so the next recipient can find the original.

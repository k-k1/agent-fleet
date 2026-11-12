---
audience: "someone standing up a deployment with Docker Compose (IT / SRE with a shell on the host)"
source_of_truth: "`docker-compose.yml`, `.env.example` and the scripts in `deploy/compose/` (a contradiction is a bug in this page)"
---

# Deploy with Docker Compose

English | [日本語](install.ja.md)

The commands that stand Agent Fleet up with Docker Compose on one Linux host, up to the first administrator signing in.
What each step decides is in [Install Agent Fleet](../install.md). Open this page when you type the commands.

## How it works

| What you do | What happens |
|---|---|
| Create `.env` and write the secrets, `DOCKER_GID`, the public URL and the IdP | The Control Plane starts with these values |
| Provide the workspace image (build or pull it) | The Control Plane can start a workspace per member with `docker run` |
| Start it with `docker compose up -d` | The Control Plane (`cp`) and Caddy run, and Caddy obtains the TLS certificate |
| Sign in with a `SUPER_ADMIN_EMAILS` account | That account becomes the deployment administrator and can launch a workspace |

You work in `deploy/compose/` (or, from a release bundle, the extracted directory).

## What is in this directory

| File | Purpose |
|---|---|
| `docker-compose.yml` | The `cp` (Control Plane) and `caddy` (automatic TLS) services |
| `Caddyfile` | Reverse proxy and Let's Encrypt (ACME). The `tls internal` self-signed fallback is here too |
| `.env.example` | All configuration. Copy it to `.env` |
| `backup.sh`, `restore.sh` | Backup and restore ([Back up and restore](../run/backup-restore.md)) |
| `load-images.sh` | Imports images from a local tar. Only for hosts that cannot reach a registry ([Install into a closed network](../run/air-gapped.md)) |
| `release.sh` | Builds a versioned release bundle. For maintainers |
| `docker-compose.macos.yml` | The overlay for a Mac ([Run on an Apple Silicon Mac](macos.md)) |

Released images are published to GHCR.

- They are `ghcr.io/k-k1/agent-fleet/control-plane:<version>` and `ghcr.io/k-k1/agent-fleet/workspace:<version>`
- `REGISTRY` and `VERSION` in `.env` resolve them. The packages are public, so pulling needs no registry login
- To pull from an internal mirror, point `REGISTRY` at it

## Before you start

| What you need | Notes |
|---|---|
| A Linux host with Docker (Engine and `docker compose`) | An Apple Silicon Mac works for one person ([Run on an Apple Silicon Mac](macos.md)) |
| A public domain with a DNS A / AAAA record pointing at this host | For TLS. Internal only? Use `tls internal` in the `Caddyfile` |
| An IdP client for sign-in | A Google OAuth 2.0 web client, or an OIDC app at Entra ID, Okta, Keycloak, Auth0, Cognito or GitLab. You register one redirect URI however many you use |
| Agent seats | Each member signs in with their own seat from the Console after the first launch. Company Team / Enterprise seats are recommended over personal Pro / Max |
| (For `agy`) a CPU that exposes RDRAND | "The Antigravity CLI and RDRAND" below |

An IdP that only speaks SAML (HENNGE One, TrustLogin, CloudGate …) goes behind oauth2-proxy or Keycloak, with `AUTH=proxy`.

### The Antigravity CLI and RDRAND

`agy` (the Antigravity CLI) is a FIPS build that aborts at startup without the CPU's RDRAND instruction. Check with `grep -w rdrand /proc/cpuinfo`.

- Where the kernel has withdrawn the instruction (seen on AMD Ryzen Embedded, whose RDRAND returns a constant), the Agent masks it out of OpenSSL's CPU detection and runs agy anyway. Its randomness then comes from the kernel rather than the CPU, and the Console's agy card says so
- A deployment that requires the FIPS module's own entropy path sets `AF_AGY_RDRAND_MASK=0`. `agy` is then hidden from the agent selector

## Starting from a published release

The `install-compose.sh` helper in the [distribution repo](https://github.com/k-k1/agent-fleet-dist) fetches, verifies and extracts the bundle and pulls the images from GHCR.
It leaves you at the `cp .env.example .env` step of step 1 below. The steps on this page are the manual (or from-source) path.

## 1. Configure and start from source

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

- Store `AF_MASTER_KEY` in a password vault the moment you generate it. It is in neither `DATA_DIR` nor the backups ([Handle the deployment keys](../secure/deployment-keys.md))
- A wrong `DOCKER_GID` gives "permission denied" on the docker socket
- `.env.example` itself explains each variable. What to choose is in steps 1–4 of [Install Agent Fleet](../install.md)

## 2. Start from the published release images

To run a published release instead of building, leave `REGISTRY`, `VERSION` and `WS_IMAGE` at the values the bundle ships with, skip 4) of step 1, and pull.

```bash
docker compose pull                                  # cp + caddy
docker pull "$(grep -E '^WS_IMAGE=' .env | cut -d= -f2)"   # workspace (see below)
docker compose up -d
```

The workspace image is **not a compose service**. The Control Plane starts it per member with `docker run`, so `docker compose pull` does not fetch it.

- You need not pull it up front: the first workspace start pulls it on demand
- Pulling it up front spares whoever presses Start first a wait of several minutes

## 3. Write the sign-in IdP

The `.env` keys and what to create on the IdP's side are on each IdP's page.

| IdP | Page |
|---|---|
| Google | [Sign in with Google](../signin/google.md) |
| Microsoft Entra ID | [Sign in with Microsoft Entra ID](../signin/entra.md) |
| GitHub | [Sign in with GitHub](../signin/github.md) |
| Okta, Keycloak, Auth0, Cognito, GitLab | [Sign in with another OIDC IdP](../signin/oidc.md) |

Whichever IdP you use, the redirect URI you register is this one:

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

GitHub's and Bitbucket's "Connect via OAuth" for cloning repositories is not in `.env`: a tenant administrator registers it in the Console ([Choose the OAuth apps for integrations](../../admin/access/oauth-apps.md)).

## 4. Sign in as the first administrator

1. Open `https://<PUBLIC_DOMAIN>`
2. Sign in with an account you listed in `SUPER_ADMIN_EMAILS`
3. Launch a workspace

- `SUPER_ADMIN_EMAILS` is read once at startup and is the only source of truth. Removing an address and restarting also takes the deployment administrator role away
- `.env.example` starts with `AF_PROVISION=invite`: nobody else gets a workspace until an administrator adds them. Someone who was added signs in without also being in `AF_OAUTH_ALLOWED_*`
- How to decide tenants, members and each tenant's sign-in page (`<PUBLIC_BASE_URL>/login/<slug>`) is in [Choose the tenant and provisioning model](../install-decisions.md)
- How to remove someone is in [Revoke access and hand over super_admin](../secure/offboarding.md)

## When it goes wrong

| Symptom | Where to look |
|---|---|
| The Control Plane does not start | `docker compose logs cp`. Does `curl -s http://127.0.0.1:8099/healthz` print `ok`? |
| "permission denied" on the docker socket | Does `DOCKER_GID` match `getent group docker`? |
| Every sign-in is refused | Every allowlist is empty and nobody has been invited yet ([Cannot log in](../diagnose/cannot-login.md)) |
| Workspaces cannot reach the public URL | [When workspaces cannot reach the CP](workspace-listener.md) |

Other symptoms are in [Find the cause from the symptom](../diagnose/checklist.md).

## Related

- [Install Agent Fleet](../install.md)
- [Run on an Apple Silicon Mac](macos.md)
- [When workspaces cannot reach the CP](workspace-listener.md)
- [Back up and restore](../run/backup-restore.md), [Upgrade](../run/upgrade.md)
- [Deploy on a single EC2 instance](../aws-ec2-single.md)

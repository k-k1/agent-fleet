# Security Policy

Agent Fleet handles credentials (Claude / GitHub / Bitbucket) and runs untrusted
user workloads in containers, so we take security seriously and publish the source
precisely so that operators can audit the encryption and isolation themselves.

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** — do not open a public
issue for anything exploitable.

- Preferred: GitHub → *Security* → *Report a vulnerability* (private advisory).
- Or email the maintainer (see the repository owner's public profile).

Include: affected version/commit, deployment shape (on-prem compose / AWS), a
reproduction or PoC, and the impact you observed. We aim to acknowledge within a
few business days.

## Deployment model (why cross-company blast radius is bounded)

The intended deployment is **one company = one self-hosted deployment** (each
company runs its own instance on its own infrastructure, with its own Claude
seats). Companies are isolated by *separate deployments*, not by in-process
boundaries. This shapes the trust model below.

## Known residual risks (operators must understand these)

These are inherent to the current on-prem architecture, documented so operators
make an informed choice — not undisclosed bugs.

- **The Control Plane holds host-root-equivalent power.** The CP shells out to the
  host Docker daemon via a mounted `/var/run/docker.sock` (docker-out-of-docker)
  and injects per-workspace decryption keys at container start. A compromise of
  the CP (or the host) therefore breaks isolation *within that one deployment*.
  It does **not** reach other companies, which are separate deployments.
  - Hardening option: front the socket with a filtering proxy
    (e.g. `tecnativa/docker-socket-proxy`) to narrow the Docker API surface.

- **`AF_MASTER_KEY` is the root of the credential encryption.** Every per-workspace
  DEK is wrapped by a tenant KEK derived from this key. **Losing it = crypto-shred:
  all stored credentials become permanently undecryptable** (including backups).
  Store it in a **separate vault** from the database and home directories, and
  back it up independently. See `deploy/compose/README.md`.

- **Backups are sensitive.** A backup archive contains per-user homes and plaintext
  Claude login state. Protect archive storage (permissions / encryption at rest).

- **`docker.sock` access = host access.** Anyone able to run the CP container (or
  reach the Docker socket) can control the host. Restrict who can deploy/operate.

## Supported versions

This is pre-1.0 software. Security fixes are made against the latest tagged
release; please upgrade to the newest version before reporting.

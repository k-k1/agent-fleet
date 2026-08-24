# 02. Day-to-Day Operations

English | [日本語](02-operations.ja.md)

This chapter covers steady-state operations after installation — backup, restore, upgrades,
air-gapped networks, and stopping Workspaces — together with the decision points involved.
**The actual commands (`backup.sh` / `restore.sh` / upgrade / air-gapped procedures) are
canonically documented in [deploy/compose/README.md](../../../deploy/compose/README.md).**
Rather than duplicating the commands here, this chapter supplements them with "what happens
and what to watch out for." The working directory is `deploy/compose/`. To dig into the
design assumptions, see [dev/09 §9.7](../../dev/09-deploy.md).

## Backup

`deploy/compose/backup.sh` packs the entire `DATA_DIR` into a timestamped `tar.gz`. For the
command and its options (`OUT_DIR` / `KEEP` / `--no-stop`), see the "Backup & restore" section
of the runbook.

### What is included and what is not

What **goes into** the archive (= with this alone you can restore onto another host):

- `control-plane.db` — the graph of tenants / members / ports / tokens.
- Each user's home (working trees, dotfiles, the envelope-encrypted `secrets.enc`).
- Each user's `claude-config` (**plaintext Claude login state**).
- Caddy's certificates (to avoid Let's Encrypt rate limits at restore time).

What is **not included**:

- `shared/jvm` (the re-fetchable, huge Temurin JDKs) is deliberately excluded.
- **`AF_MASTER_KEY` is not included.** It lives in `.env` and is by design never put into
  the archive. **Keep it outside the data area** — the archive now also carries the client
  secrets of tenant-defined sign-in methods (sealed with that key), so a copy of the key stored
  next to the data would undo the separation the envelope encryption exists for.

> These two points are the heart of operations. The backup archive is **sensitive data that
> contains plaintext Claude state**, so be strict about the permissions and encryption of
> wherever you store it. At the same time, possessing the archive alone is not enough: without
> `AF_MASTER_KEY`, the envelope-encrypted credentials cannot be decrypted. Conversely, if you
> lose `AF_MASTER_KEY`, every past archive becomes permanently undecryptable (crypto-shred —
> see [03](03-security.md)). **Keep the key and the data separate, but back up both** — that is
> the right answer.

### Impact on users

By default `backup.sh` **briefly stops the CP and Caddy** to take a consistent SQLite snapshot,
then restarts them immediately. During this, **user Workspaces (`af-ws-*`) do not stop, since
they are outside compose management** — sessions stay connected and work continues. During the
few seconds of downtime, Console logins and API relaying merely become temporarily unresponsive.
If the caller has already guaranteed quiescence, `--no-stop` lets you take the backup without
stopping anything.

### Running it from cron

In production, run `backup.sh` periodically from cron. Use `OUT_DIR` to point at your
organization's backup area (a separate volume / a remote location) and `KEEP` to control the
number of generations. An example cron entry is in the "Backup & restore" section of the
runbook. After each backup, pruning of old generations is performed automatically.

## Restore

This is the recovery procedure onto a clean host, or after data loss. The commands are in the
runbook's "Backup & restore".

The flow is: "prepare `.env` (restore **the same `AF_MASTER_KEY` as the backup source** from
your vault) → `restore.sh <archive>` → `docker compose up -d` → **Start** each Workspace from
the Console". Three key points.

1. **`AF_MASTER_KEY` must be identical to the original.** If it differs or is missing, the
   wrapped DEKs cannot be unwrapped and the credentials cannot be decrypted. Verify the
   restoration from the vault first.
2. **The `DATA_DIR` basename constraint.** The parent path of the restore destination may
   differ from the original (e.g. `/srv` → `/mnt`); at startup the CP re-points each
   Workspace's on-disk root to the current `DATA_DIR`. However, the top-level directory name
   inside the archive (= the basename of the original `DATA_DIR`) and the basename of the
   destination `DATA_DIR` **must match**. `restore.sh` validates this and refuses on a mismatch.
3. **Workspaces rehydrate via "Start workspace" in the Console.** Immediately after a restore,
   no Workspace containers exist (they are outside compose management). When a user (or an
   admin) presses Start in the Console, the CP re-creates `af-ws-*` using the ports/tokens in
   the restored DB, and the user's connections and Claude login come back from `secrets.enc`
   and `claude-config` in their home.

## Upgrade

Just change `VERSION` in `.env` to the new tag, pull (or build) the images, and run `up -d`.
The commands are in the runbook's "Upgrade" section.

- **Images come from GHCR** (`ghcr.io/k-k1/agent-fleet/*`, resolved through `REGISTRY` +
  `VERSION`); pulling them needs no registry login. Remember that the **workspace image is
  not a compose service**, so `docker compose pull` does not fetch it — `docker pull` it
  separately, or let the first Start pull it on demand.
- **Schema migrations are embedded in the CP and applied automatically at startup**
  (**forward-compatible**). No manual migration runs are needed.
- **Downgrades are not supported.** An older CP cannot understand migrations applied by a newer
  version. Therefore **always take a `backup.sh` before upgrading**. If something goes wrong,
  the correct rollback path is not "go back to the old image" but "restore from the backup."
- Check the release notes for breaking changes.
- **On AWS (`ecs` / `ecs-ec2`) the command is `deploy/aws/ecs/update.sh`, not compose**
  (`VERSION=<v> ./update.sh --profile <p> --region <r>`): it pushes to ECR, re-deploys the
  ingress stack with only `ImageTag` overridden, and waits for the CP service to roll.
  **Running workspaces are not upgraded automatically** — each picks the new image up on its
  next Start. The users concerned see a "Restart needed" badge in the Console, so when to take the
  restart is their call, not yours.

## Installing into an air-gapped network

You can install onto a host with no external network access. Since
[ADR 0037](../../decisions/0037-registry-policy.md) the images are distributed through GHCR
and **no image tarball ships with a release**, so a host that cannot reach a registry either
mirrors `ghcr.io/k-k1/agent-fleet/*` internally and points `REGISTRY` at that mirror, or has
the images carried in by hand: build and `docker save` them on a networked machine with
`release.sh --save`, then `load-images.sh` on the target host. The commands are in the
runbook's "Air-gapped install" section.

There are four decision points.

- **Where the images come from**: an internal registry mirror keeps `docker compose pull`
  working as normal and is the lower-maintenance option. The hand-carried tar means repeating
  the build/copy/load cycle for every upgrade, and its image names must match `REGISTRY` in
  `.env`.
- **TLS**: Let's Encrypt is unusable in an air-gapped network, so either switch to
  `tls internal` (self-signed) per [01 §4](01-install.md), or use an internal CA.
- **Installing Claude**: the Workspace image by default fetches the latest Claude at container
  startup. On a fully offline host, set `CLAUDE_INSTALL=0` (via `WS_ENV`) and use an image with
  Claude baked in.

- **Diagram icons (`.drawio`)**: the vendored drawio viewer draws diagrams offline, but the
  vendor icon artwork (`shape=mxgraph.aws4.*`, GCP, Azure, Kubernetes, rack gear …) is **not**
  bundled — all of it together is 40.8 MB. Normally the Control Plane fetches a set on first
  use and caches it; with no external network that fetch fails and diagrams degrade quietly to
  outlines, colours and labels. To avoid that, seed the cache from a directory you carry in:

  ```sh
  # on a networked machine, once
  git clone --depth 1 -b v31.1.8 https://github.com/jgraph/drawio
  # carry drawio/src/main/webapp/stencils to the target host, then, on the CP host:
  control-plane drawio-preseed --from /path/to/stencils        # default bundle, 49 files / 17.0 MB
  control-plane drawio-preseed --from /path/to/stencils --all  # everything, 203 files / 40.8 MB
  ```

  Every file is checked against the bundled manifest's SHA-256 before it is stored, so the
  carry-in path does not have to be trusted. The cache is content-addressed and has no index,
  so you can equally well `tar` a seeded cache directory and unpack it on another host. Run
  `--list` first to see exactly what the default bundle covers, and what it leaves out.

Be clear-eyed about what "air-gapped" buys you: local images let the fleet **start**, but the
agents themselves cannot do any work without reaching their model endpoints. The offline
install path is for hosts on a restricted internal network, not for a genuinely disconnected
one.

## Distributing fleet policy (instructions to every agent)

Every agent running in a workspace can be made to read **the operator's policy**. It lives in the
repository as `workspace/workspace-notes.md`, is baked into the Workspace image and delivered to
every container (claude reads it as the managed policy at `/etc/claude-code/CLAUDE.md` on every
session; codex and opencode are seeded with the same text at each start).

- This is where fleet-wide rules go: **what must not be done** (deleting repositories, writing
  credentials in the clear), **the constraints of this environment** (no root, no Docker, shared
  memory), **how branches are handled**.
- **Applying a change needs an image rebuild**: edit → rebuild the image → each user recreates
  their workspace, or it takes effect the next time a container is created.
- A user's own additions do **not** belong in this layer. They go in each person's ⚙ Settings →
  Agent instructions, and fleet policy wins where the two conflict
  ([member/06](../member/06-agents.md#agent-instructions-write-down-how-you-work-once)).
- **Its length is a per-session context cost.** Every agent reads it every time, so before adding
  to it, check that it is genuinely needed by everyone, every time. This layer cannot be
  delivered to cursor.

## Once it is on the public internet

Put the Control Plane on a public hostname and **vulnerability scanners find it within
hours** (measured: 172 probes for `/actuator/heapdump`, `/.env` and friends in the first
9 hours). Nothing to panic about, but three things are worth knowing.

- **Very little is reachable without a session**: `/healthz`, `/login`, `/oauth2/*`,
  `/brand/*` and a legacy redirect. Everything else answers 401. `/internal/*` (egress
  ingestion), `/mcp` and `/git/*` sit outside the session gate but carry **their own
  authentication** (404 when the feature is off).
- **The access log carries the status code**, e.g. `GET /actuator/heapdump 401 0s`, so
  the log alone answers "did we refuse it, or serve it?". Filter on `401` to count probes.
- **Per-IP analysis and blocking belong outside the CP**: ALB access logs for the former,
  AWS WAF for the latter. The CP deliberately has no path-level blocklist of its own —
  that would add a second place where access is decided. On AWS, `30-ingress` ships an
  **off-by-default WAF** with exactly two knobs: `WafRateLimitPer5Min` and
  `WafIpReputation`. ⚠️ **Signature rule sets (Core rule set, SQLi, XSS) are deliberately
  not offered** — this product carries source code and shell commands in ordinary request
  bodies, so `'; DROP TABLE` and `../../etc/passwd` are legitimate traffic and those rules
  would 403 real work at random, looking like a product bug.
- **If only your own people need it, narrow the door instead**: `00-network`'s
  `AlbIngressCidr` keeps probes from ever reaching the ALB, and it costs nothing.

## Idle stop and force-stop

- **Automatic idle stop (scale-to-zero)**: an idle claude session is halted after **1 hour**
  and a Workspace with nothing running is stopped after **2 hours**. That is the default;
  `AF_SESSION_IDLE_TIMEOUT` / `AF_INTERACTION_IDLE_TIMEOUT` (a session parked on a question or an approval) / `AF_WS_IDLE_TIMEOUT` / `AF_PRESENCE_IDLE_TIMEOUT` (how long a terminal with no typing still counts as someone being there; 30m) change it (per-tenant overrides are in the
  Admin UI — a tenant setting `0` opts that tenant out, and `0` in the env turns it off for the
  whole deployment). A stopped Workspace starts automatically the next time the user opens a
  terminal (`AF_AUTOSTART`). This is effective for
  saving resources. For the meaning of the env vars, see
  [.env.example](../../../deploy/compose/.env.example); for how it works, see
  [dev/09 §9.4](../../dev/09-deploy.md).
- **force-stop (brute force)**: `docker compose down` **does not stop user Workspaces** (they
  are outside compose management). To stop a specific Workspace for sure, a super_admin
  force-stops it from the Admin panel in the Console. When the whole host must be brought fully
  down for maintenance, after stopping the CP/Caddy you also need to `docker stop` the remaining
  `af-ws-*` containers separately (this also comes up in the troubleshooting chapter,
  [04](04-troubleshooting.md)).

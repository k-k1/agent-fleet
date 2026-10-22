---
audience: "someone keeping a running deployment healthy"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
updated: "2026-10"
---

# 03. Day-to-Day Operations

English | [日本語](03-run.ja.md)

This chapter covers steady-state operations after installation (backup, restore, upgrades,
air-gapped networks, and stopping Workspaces), together with the decision points involved.
**The actual commands (`backup.sh` / `restore.sh` / upgrade / air-gapped procedures) are
canonically documented in [deploy/compose/README.md](../../deploy/compose/README.md).**
Rather than duplicating the commands here, this chapter supplements them with "what happens
and what to watch out for." The working directory is `deploy/compose/`. To dig into the
design assumptions, see `docs/build/09-deploy.md` §9.7.

## Backup

`deploy/compose/backup.sh` packs the entire `DATA_DIR` into a timestamped `tar.gz`. For the
command and its options (`OUT_DIR` / `KEEP` / `--no-stop`), see the "Backup & restore" section
of the runbook.

### What is included and what is not

What **goes into** the archive (= with this alone you can restore onto another host):

- `control-plane.db`: the graph of tenants / members / ports / tokens.
- Each user's home (working trees, dotfiles, the envelope-encrypted `secrets.enc`).
- Each user's `claude-config` (**plaintext Claude login state**).
- Caddy's certificates (to avoid Let's Encrypt rate limits at restore time).

What is **not included**:

- `shared/jvm` (the re-fetchable, huge Temurin JDKs) is deliberately excluded.
- **`AF_MASTER_KEY` is not included.** It lives in `.env` and is by design never put into
  the archive. **Keep it outside the data area.** The archive also carries the client
  secrets of tenant-defined sign-in methods (sealed with that key), so a copy of the key stored
  next to the data would undo the separation the envelope encryption exists for.

> These two points matter most in operations. The backup archive is **sensitive data that
> contains plaintext Claude state**, so be strict about the permissions and encryption of
> wherever you store it. At the same time, possessing the archive alone is not enough: without
> `AF_MASTER_KEY`, the envelope-encrypted credentials cannot be decrypted. Conversely, if you
> lose `AF_MASTER_KEY`, every past archive becomes permanently undecryptable (crypto-shred;
> see [04](04-secure.md)). **Keep the key and the data separate, but back up both.**

### Impact on users

By default `backup.sh` **briefly stops the CP and Caddy** to take a consistent SQLite snapshot,
then restarts them immediately. During this, **user Workspaces (`af-ws-*`) do not stop, since
they are outside compose management**; sessions stay connected and work continues. During the
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
the Console". There are three key points.

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
  not a compose service**, so `docker compose pull` does not fetch it; `docker pull` it
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
  When a release needs one stack updated before another (the reverse of the script's usual
  order), `update.sh` checks for that first, stops before touching anything, and prints the
  exact `aws cloudformation deploy` command to run on its own; then run `update.sh` again.
  **Running workspaces are not upgraded automatically**: each picks the new image up on its
  next Start. The users concerned see a "Restart needed" badge in the Console, so when to take the
  restart is their call, not yours.
  **Data stack and the EFS backup:** `update.sh` does not update the data stack. A
  `Persistence=retain` deployment stood up before v0.26.0 has no daily EFS backup until
  that stack is updated by hand; `update.sh` warns when it finds this (read-only check) and
  prints the change-set command. See `deploy/aws/ecs/README.md`, "Upgrade (runbook)" step 1a.
  The same finding is shown to super_admins as a banner on every section of the Console's
  Admin screen ("no daily EFS backup"), so it is not lost if nobody reads the `update.sh`
  output. `update.sh` records it in the ingress stack (parameter `EfsBackup`), and the banner
  clears at the first `update.sh` run that confirms the data stack has its backup. Never filled
  means no banner. If `update.sh` cannot read or evaluate the data stack it keeps the previous
  verdict, so an existing banner stays until a run confirms the fix.
  **On `ecs-ec2`, a release that changes the slot launch template** reaches the slots you already
  have only when they are replaced ([below](#ecs-ec2-replacing-slots-after-a-launch-template-change)).

## ecs-ec2 slot pool

On `ecs-ec2` a workspace runs as a task on an EC2 slot taken from a pool, and its home is an EBS
volume that stays with its slot between starts. A super_admin manages the slots under
**Settings → Admin → Slots**.

### ecs-ec2: replacing slots after a launch template change

A slot reads its user data only when it is launched, and a stopped workspace goes back to the
same slot on its next Start, so a change to the slot launch template (a release's security
setting, for example) does not reach the slots you already have. Replace them from the
Console instead of terminating instances by hand.

**Slots below `$Latest` are replaced automatically.** When a workspace starts and its home is on
such a slot, that start moves it to a new slot exactly as a reservation does, but without the WS
bar notice, and a free slot below `$Latest` is terminated by the sweeper. If no new slot can be
launched (capacity, quota) the start still succeeds on the old slot, and starts within the next
10 minutes stay on it without trying again. Each automatic move is in the audit log (`pool.slot_replace_auto`). The Slots tab shows
**older than $Latest · replaced at next start** on these slots. Set
`AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS=false` on the Control Plane to turn this off. Reserve slots
yourself when a workspace must never be put back on its old slot (then a start that cannot launch
a new slot fails instead):

1. **Settings → Admin → Slots** (super_admin). The **Template** column shows the launch template
   version each slot was launched from, marked **older than $Latest** when it is.
2. **Reserve all N for replacement…** lists the slots below `$Latest` and the workspace on each,
   and reserves exactly those once you confirm. **Replace at next start** on a row reserves one
   slot (a reserved row then shows the label "replaced at next start"); **Cancel replacement** takes it back. Every reservation is in the audit log
   (`pool.slot_replace_reserve` / `pool.slot_replace_cancel`, with the workspace it moves).
3. Nothing happens to a running workspace. Its member sees **Moves to a new slot** in the WS bar,
   and their next stop → start launches a new slot from `$Latest`, moves the home onto it and
   terminates the old one. That start is about 25 seconds longer than usual. Files, repos and
   logins come along: the home is never deleted.
4. If no new slot can be launched (capacity, quota), the start fails with the reason and the
   reservation stays. The member is never put back on the reserved slot; retry later. A
   reserved slot with no workspace on it is terminated by the sweeper.

`deploy/aws/ecs/README.md`, "Moving retained slots onto new user data", has the details and how
to verify the result.

### ecs-ec2: memory protection for the management plane

The slot user data gives the SSM agent, docker
and containerd a systemd `MemoryMin` (192 / 256 / 128 MiB), and a small helper service
(`af-protect-mgmt`) finds the ECS agent, which runs as a docker container rather than under
`ecs.service`, and gives its scope a 192 MiB `memory.min`. The helper re-applies this every 30
seconds, so it follows agent restarts, and also lowers the OOM score of the ECS and SSM agents
(on the SSM agent process that is already running at first boot too). `system.slice` carries the
832 MiB total so the floors count. Under memory pressure the kernel then reclaims the workspace
container's pages first, so a memory-hungry workspace is much less likely to leave a slot whose
agents stop answering while EC2 still reports it healthy. The total sits inside the host reserve
(`AF_ECS_EC2_HOST_RESERVE_MB`, at least 1024 MiB by default); if you set the reserve below 832,
the protection is larger than the reserve. This is a launch template change: it creates a new
`$Latest`, and existing slots pick it up only through the replacement above. It needs cgroup v2
(the Amazon Linux 2023 default).

### ecs-ec2: stopping a wedged slot

A slot can be **wedged**: the EC2 instance is running and its status checks are `ok`, but its ECS
agent is not connected, SSM no longer answers, and a member's home may still be attached. Every Start
for that member then waits or fails. The Slots tab shows such a row as running with **(not accepting
tasks yet)**, and gives it **Stop (force)** (super_admin).

1. **Settings → Admin → Slots → Stop (force)** on the row, then confirm. The dialog names the
   workspace on the slot. The server refuses a slot whose ECS agent is connected **and** whose SSM is
   online (`slot_healthy`): the action is for slots that do not answer. A stopped slot
   (`slot_not_running`) and a quarantined one (`slot_quarantined`) are refused too. The API takes
   `{"override_healthy": true}` for the rare case where you must stop a healthy one; the Console never
   sends it, and a workspace running on that slot is stopped with it.
2. It ends the member's Start on that slot (their workspace reads as stopped), cancels the commands the
   Control Plane queued on the instance (a pending umount or mount is what holds the home's lock), and
   stops the instance. The stop is a normal shutdown first, so the kernel can unmount the home; if the
   instance has not stopped after 3 minutes the Control Plane repeats it with Force, and pressing the
   button again while the instance is `stopping` forces it at once. A timer never acts on an instance
   that has been started again since.
3. **The slot is not quarantined and the home stays on it.** Once the slot row shows asleep
   (stopped), the member's next Start wakes this same slot through the ordinary path. **Wait for
   that**: a Start made while the instance is still `stopping` gives up waiting after about two minutes
   and leaves the workspace on "starting" until its claim expires; if that happens, stop the workspace
   and start it again. The reboot clears whatever thrashed, and the slot's ECS agent
   reconnects and reports a task that was still `STOPPING` on it. Keeping a box as evidence is what
   quarantine is for, and routing dark slots there is a separate change (#2450).
4. The result is on the page after the call (commands cancelled, whether Force was used) and in the audit
   log (`pool.slot_force_stop`, with the workspace, both health signals and the counts).
   The Control Plane role needs `ssm:ListCommands`, `ssm:CancelCommand` and
   `ssm:DescribeInstanceInformation` for this (statement `SsmSlotForceStop` in `20-platform`):
   **update the `20-platform` stack before using the button**, or the health check fails with
   AccessDenied and the action cannot run.

**A task still STOPPING.** When a slot's agent cannot report a stop, ECS keeps the workspace's previous
task as `STOPPING`, and the member's next Start waits for it (the service allows one task at a time). The
row shows **task STOPPING N min** for a slot whose agent is not connected. Once the instance is running
again its agent reports the stop, which is what the member's next Start after a force-stop does; you can
also start the instance from the EC2 console. The Control Plane does not do that on its own.
Not verified against a live AWS account.

## Installing into an air-gapped network

You can install onto a host with no external network access. Since
ADR 0037 the images are distributed through GHCR
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
  `tls internal` (self-signed) per [02 §4](02-install.md), or use an internal CA.
- **Installing Claude**: the Workspace image by default fetches the latest Claude at container
  startup. On a fully offline host, set `CLAUDE_INSTALL=0` (via `WS_ENV`) and use an image with
  Claude baked in.

- **Diagram icons (`.drawio`)**: the vendored drawio viewer draws diagrams offline, but the
  vendor icon artwork (`shape=mxgraph.aws4.*`, GCP, Azure, Kubernetes, rack gear …) is **not**
  bundled; all of it together is 40.8 MB. Normally the Control Plane fetches a set on first
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

What "air-gapped" buys you is limited: local images let the fleet **start**, but the
agents themselves cannot do any work without reaching their model endpoints. The offline
install path is for hosts on a restricted internal network, not for a genuinely disconnected
one.

## Distributing fleet policy (instructions to every agent)

Every agent running in a workspace can be made to read **the operator's policy**. It lives in the
repository as `workspace/workspace-notes.md`, is baked into the Workspace image and delivered to
every container (claude reads it as the managed policy at `/etc/claude-code/CLAUDE.md` on every
session; codex, opencode, agy, copilot and kiro get the same text composed into their own
instruction files at each start).

The policy is split in two so that it stays short: the always-loaded file carries only the
prohibitions and the traps an agent cannot see coming, and ends with an index of **topic files**
(`workspace/notes/<topic>.md`, shipped as `/usr/local/share/agent-fleet/notes/`) that hold the
procedures; the agent reads the one for its situation when it gets there. Put a new rule in the
always-loaded file only when the agent has to know it *before* it knows it needs it; everything
else goes in a topic file, and the index line is what makes it findable. Where the CLI has a
user-level skills directory (claude, codex, opencode) the agent also registers each topic file as
a skill named `af-<topic>` at every start, so the CLI's own skill index (description at start,
body on demand) is a second way to the same text; the other kinds have only the index.

- This is where fleet-wide rules go: **what must not be done** (deleting repositories, writing
  credentials in the clear), **the constraints of this environment** (no root, no Docker, shared
  memory), **how branches are handled**.
- **Applying a change needs an image rebuild**: edit → rebuild the image → each user recreates
  their workspace, or it takes effect the next time a container is created.
- A user's own additions do **not** belong in this layer. They go in each person's ⚙ Settings →
  Agent instructions, and fleet policy wins where the two conflict
  ([member/06](../member/06-agents.md#agent-instructions-write-down-how-you-work-once)).
- **The always-loaded file's length is a per-session context cost.** Every agent reads it every
  time, so before adding to it, check that it is genuinely needed by everyone, every time; a
  topic file costs nothing until it is read. This layer cannot be delivered to cursor.

## opencode 2: Terminal sessions and the shared server

With opencode 2, Terminal (CLI) sessions attach to the one opencode server the Workspace Agent
runs ([member/06](../member/06-agents.md#opencode)). To force a private server per
Terminal session instead (about 150-250 MB more memory per session, measured idle), start the
Workspace Agent with this in its environment:

```
AF_OPENCODE_TUI_STANDALONE=1
```

- It takes effect for Terminal sessions launched after the Agent starts with it; running
  sessions keep the mode they started in.
- `AF_OPENCODE_SERVE_DISABLE=1` (managed opencode off) also gives private servers, because
  there is no shared server to attach to.
- On an image re-pinned to opencode 1 (below) neither variable has an effect on Terminal sessions.

### Going back to opencode 1

The image pins opencode 2 (`OPENCODE_VERSION` in `workspace/Dockerfile`). If it misbehaves in
your deployment, the way back is an image whose pin is `1.19.0`: rebuild with
`--build-arg OPENCODE_VERSION=1.19.0` (the install, the self-update and the drift check pick
`opencode-ai` for a 1.x pin by themselves), then Stop and Start the workspaces.

- **What carries over.** The first opencode 2 start converted `~/.local/share/opencode/opencode.db`
  in place and imported the opencode 1 sessions, keeping their ids. opencode 1 still runs on the
  converted file and sees every conversation from before the upgrade.
- **What is lost.** Conversations created under opencode 2, and turns added under opencode 2 to
  older ones, are not visible to opencode 1 (it does not read the tables opencode 2 writes), and
  conversations started under opencode 1 after the way back are not visible to opencode 2 on a
  later upgrade. The data is not deleted.
- **To be able to undo an upgrade exactly,** copy `~/.local/share/opencode/opencode.db*` of the
  workspace before the first opencode 2 start and put the copies back after the way back.
- **Leftovers of the earlier opt-in.** A workspace that had run `workspace-agent opencode-major 2`
  (a private opencode 2 install under `~/.local/share/agent-fleet/opencode2` with its own database
  in `opencode2-data`) ignores both once the pin is 2.x: the pinned opencode 2 uses the main
  database, and the sessions that only exist in the private database are **not merged** into it.
  They stay on disk until you delete the two directories. After a way back to a 1.x pin the opt-in
  works as it did before.

## Once it is on the public internet

Put the Control Plane on a public hostname and **vulnerability scanners find it within
hours** (measured: 172 probes for `/actuator/heapdump`, `/.env` and friends in the first
9 hours). Nothing to panic about, but three things are worth knowing.

- **Very little is reachable without a session**: `/healthz`, `/readyz`, `/login`,
  `/oauth2/*`, `/brand/*` and a legacy redirect. Everything else answers 401. `/internal/*` (egress
  ingestion), `/mcp` and `/git/*` sit outside the session gate but carry **their own
  authentication** (404 when the feature is off).
- **The access log carries the status code**, e.g. `GET /actuator/heapdump 401 0s`, so
  the log alone answers "did we refuse it, or serve it?". Filter on `401` to count probes.
- **Per-IP analysis and blocking belong outside the CP**: ALB access logs for the former,
  AWS WAF for the latter. The CP deliberately has no path-level blocklist of its own;
  that would add a second place where access is decided. On AWS, `30-ingress` ships an
  **off-by-default WAF** with exactly two knobs: `WafRateLimitPer5Min` and
  `WafIpReputation`. ⚠️ **Signature rule sets (Core rule set, SQLi, XSS) are deliberately
  not offered**: this product carries source code and shell commands in ordinary request
  bodies, so `'; DROP TABLE` and `../../etc/passwd` are legitimate traffic and those rules
  would 403 real work at random, looking like a product bug.
- **If only your own people need it, narrow the door instead**: `00-network`'s
  `AlbIngressCidr` keeps probes from ever reaching the ALB, and it costs nothing.

## Telling two deployments apart (colour and label)

The moment a second deployment exists (staging beside production, or a laptop beside both),
every tab in the strip and every icon on a phone's home screen is the same teal cat, and the
login page gives no hint which one you are signing in to. Two settings on the Control Plane
fix that; both are cosmetic, and nothing about access or data depends on them.

**Where to set it.** The quickest route is the Console: **Admin → Deployment → Appearance**
(super_admin only): pick a colour, type a label, save. It takes effect on the next request,
with no restart, and the tab you are looking at updates on the spot. The two settings below
are the same thing on the deployment side: they are the value a fresh deployment starts
from, and what the modal falls back to when you press "Follow the environment". **A choice
saved in the modal wins over them**: change the environment afterwards and nothing moves
until you reset.

| Setting | What it does |
|---|---|
| `AF_BRAND_COLOR` | Recolours the favicon, the PWA / home-screen icons and the colour Android paints the task switcher. One of `teal` (the shipped default), `blue`, `violet`, `magenta`, `red`, `orange`, `green`, `slate`. |
| `AF_BRAND_LABEL` | A short label (≤16 characters) folded into the app name: `[staging] Agent Fleet`. It shows in the browser tab, the installed PWA's name, the login page and the Console's top bar, where it also appears as a chip in the same colour. |

```
AF_BRAND_COLOR=violet
AF_BRAND_LABEL=staging
```

Compose reads them from `.env`; on AWS ECS they are the `BrandColor` / `BrandLabel` stack
parameters; the native runtime and `run-dev.sh` take them from the environment. A restart is
enough; there is no separate image to build, because the Control Plane recolours the shipped
art as it serves it.

- The label is a **prefix** on purpose: a crowded tab strip and a phone launcher both truncate
  the END, so `[staging] Agent Fle…` still tells you where you are.
- A PWA **already installed** on a phone keeps the colour, icon and name it had at install
  time. On Android those are baked into the installed app, which is why the status bar
  inside it can stay on the old colour while the same deployment is correctly coloured in a
  browser tab. Chrome rebuilds the installed app in the background eventually, but that
  takes days; **reinstall it** (long-press the icon, uninstall, then install again from the
  browser) to see the change now.
- An unknown colour name is not fatal: the Control Plane logs it and stays on teal.

## Idle stop and force-stop

### A start that never finishes

**A start that never finishes is stopped**: a Workspace that has been starting for
**30 minutes** with no task running (on ECS, typically a task the cluster refuses to
place) is stopped, so it no longer holds capacity and the user can press **Start** again
once the cause is fixed. The 30 minutes count from its last Start, or from when the
Control Plane first saw it starting if that is later (after a Control Plane restart, for
instance), and it is checked once per usage sample (5 minutes), so the stop comes up to
one sample late. The member gets a notification in the Console's notification center saying
the start was stopped, with the last start phase (on `ecs-ec2`, why the task could not be
placed), the tenant's admins see the same reason on that member's roster row and member
detail until the next start, and the Control Plane log says so too (`start-deadline: stopped …`). `AF_WORKSPACE_START_DEADLINE` changes the limit and `0` turns it off; on
`ecs-ec2` it is never shorter than the pool's own launch budget (`AF_ECS_EC2_CLAIM_TTL_SEC`,
`AF_ECS_EC2_WAIT_SEC`). It runs on the usage sampler, so `AF_USAGE_SAMPLE_INTERVAL=0` turns
it off too.

### Automatic idle stop (scale-to-zero)

An idle claude session is halted after **1 hour**
and a Workspace with nothing running is stopped after **2 hours**. That is the default;
`AF_SESSION_IDLE_TIMEOUT` / `AF_INTERACTION_IDLE_TIMEOUT` (a session parked on a question or an approval) / `AF_WS_IDLE_TIMEOUT` / `AF_PRESENCE_IDLE_TIMEOUT` (how long a terminal with no typing still counts as someone being there; 30m) change it (per-tenant overrides are in the
Admin UI: a tenant setting `0` opts that tenant out, and `0` in the env turns it off for the
whole deployment). A tenant's values are the four fields under **Idle auto-stop** in its limits
(**Session halt after**, **Halt when awaiting a decision**, **Max hold by background work**,
**Workspace stop after**); an empty field follows the deployment default. This is effective for
saving resources. For the meaning of the env vars, see
`deploy/compose/.env.example`; for how it works, see
`docs/build/09-deploy.md` §9.4.

A stopped Workspace comes back when the user presses **Start**, and
`AF_AUTOSTART` (on by default) additionally starts it for the deliberate "I am using this
now" actions: creating, forking or resuming a session, answering a carried question, the
SSM node lookup. **Opening a terminal does not start it, and neither does reading or
reloading the Console**: those answer "the workspace is stopped — start it first", so a tab
left open never keeps a Workspace warm.

### What keeps a Workspace awake

Background work (a dev server or a
`run_in_background` task started by an idle session) holds the Workspace for at most
**2 hours** (`AF_BACKGROUND_HOLD_MAX`, per tenant `background_hold_max`; `0` = unbounded);
after that it no longer keeps the Workspace awake, and the "do not auto-stop" pin is the way to
keep a genuinely long job.

A Terminal claude, agy, codex, cursor, copilot, kiro or opencode session showing "working" keeps the
Workspace awake only while it shows signs of life: if it has produced no output, state change or
running tool process for **1 hour**, it stops counting. That is a limit on time without any
change, not on total working time. For claude and agy a running tool process counts as life; for
codex, cursor, copilot, kiro and opencode only a pane repaint does (a spinner or elapsed-time
counter counts), so a silent tool under a pane that stays static for an hour can lapse, while a
stuck session whose spinner keeps animating keeps holding. Use the "do not auto-stop" pin for
long silent work on those kinds. A Managed cursor, kiro, codex or opencode session is bounded the same way, on the runtime's own sign of life: its last message for that session, or a running tool (an open tool call or item, or for cursor and kiro a tool process under the runtime). It was measured on cursor-agent 2026.10.16, kiro-cli 2.31.0, codex 0.163.0 and opencode 2.0.32 with a local shell tool;
whenever the Agent cannot tell what is running (a restart, a reconnect, a failed process-table read) the session keeps holding. opencode 1.x and Managed muse, lcpp and copilot are not bounded this way (see ADR 0055, 2026-10-17 addendum).
shell and ssm sessions never show "working", and muse and lcpp have no Terminal mode, so there is nothing of those kinds to bound. Background
work and the "do not auto-stop" pin are never subject to it.

### Force-stop (brute force)

`docker compose down` **does not stop user Workspaces** (they
are outside compose management). To stop a specific Workspace for sure, a super_admin
force-stops it from the Admin panel in the Console. When the whole host must be brought fully
down for maintenance, after stopping the CP/Caddy you also need to `docker stop` the remaining
`af-ws-*` containers separately (this also comes up in the troubleshooting chapter,
[06](06-diagnose.md)).

## Pausing an idle deployment

Stopping every Workspace leaves the deployment's floor billing: the Control Plane, the database,
the NAT gateway, the load balancer. For a stretch nobody uses it, the AWS and GKE deployments each
have a `pause.sh` that stops or removes what bills by the hour and keeps the data; `--up` brings
the deployment back. To get as close to zero as possible, tear it down instead (`teardown.sh` on AWS,
`terraform destroy` on GKE). What a teardown keeps depends on the deployment: on AWS a
`Persistence=retain` deployment keeps the database's final snapshot, the EFS file system and its
backup vault (and their storage cost) unless you pass `--purge-retained`; on GKE the deletion
protections have to be lifted first, and the snapshots of the Control Plane's disk outlive the
destroy. The runbooks' "Teardown" ([AWS](../../deploy/aws/ecs/README.md#teardown)) and "Tearing down"
([GKE](../../deploy/kubernetes/README.md#tearing-down)) list what goes and what stays.

Both scripts work the same way: without `--yes` a pause only prints its plan, `--dry-run` prints
every write without making it, and `--status` shows what is running and what still bills, changing
nothing. `--keep-db` leaves the database running.

- **AWS (`ecs` / `ecs-ec2`)**: `deploy/aws/ecs/pause.sh --profile <p> --region <r>`. It stops the
  running Workspaces (their sessions end), waits for the Control Plane to put the slots to sleep and
  end the engines' GPU instances (`--fast` does it directly), stops the Control Plane, stops the RDS
  instance and deletes the NAT gateway (`--keep-nat` keeps it). The gateway is deleted through the
  network stack's `NatEnabled` parameter, never by hand (the stack would keep pointing at the deleted gateway), and the egress address is kept, so the
  source IP is the same after `--up`. Nothing in a private subnet reaches out while it is gone. AWS
  starts a stopped RDS instance again by itself after 7 days; the data stack's re-stop function
  (`RdsRestopOnForcedStart`, on by default) stops it again while the instance carries the pause tag,
  and an older data stack gets it with one deploy of that stack. `--up` brings back the NAT gateway,
  the database and the Control Plane; members start their own Workspaces. The load balancer, EFS and
  the database's storage keep billing.
- **GKE (`kubernetes`)**: `deploy/gcp/gke/pause.sh --project <p> --location <region or zone> --prefix <name_prefix>`,
  in a shell whose kubectl points at the cluster; it refuses a context whose server and CA are not
  that cluster's. A running Workspace makes it refuse, unless `--stop-workspaces` stops them. It
  scales the Control Plane to 0, empties the workspace pool and the system pool, and stops Cloud SQL,
  which, unlike RDS, does not start itself again. The node counts are recorded on the Control Plane's
  namespace and `--up` restores them; until then `terraform plan` reports the pools as drift. The GKE
  management fee, the load balancer's forwarding rule, the Private Service Connect endpoint, Cloud NAT
  and its address, the persistent disks and Cloud SQL's storage keep billing (measured in
  `deploy/gcp/gke/README.md`, "Cost").

The order each script follows and why, and what to check afterwards, are in the runbooks:
[deploy/aws/ecs/README.md, "Lifecycle scripts"](../../deploy/aws/ecs/README.md#lifecycle-scripts-stand-up--pause--tear-down)
and [deploy/kubernetes/README.md, "Pausing"](../../deploy/kubernetes/README.md#pausing).

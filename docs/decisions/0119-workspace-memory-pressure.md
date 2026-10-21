# 0119. Workspace memory pressure: the Agent reads cgroup v2 PSI, warns, then refuses new sessions, then stops the largest one — and ships measure-only first

English | [日本語](0119-workspace-memory-pressure.ja.md)

- Status: **proposed** (2026-10-22). Documentation only: nothing below is built. The code facts were
  read from the tree on this date. **Every threshold is a provisional starting value, not a
  measurement**: no pressure history exists from the incidents (the signals were never recorded), and
  decision 8 is the plan to get it.
- Refs: #2453
- Related: [0045](0045-ec2-persistent-workspace.md) decision 8 (one slot per member, which is why the
  box's memory is the member's own) / [0044](0044-workspace-sizing.md) (sizing) /
  [64-ec2-persistent-workspace.md](../log/64-ec2-persistent-workspace.md) §64.40 (the first thrash
  incident and why the container has a hard cap) / #2451 (host reserve) / #2452 (slot `MemoryMin`) /
  #2446, #2447 (the failures hit while recovering)

## Context

A workspace that runs more agent sessions than its memory allows does not fail cleanly. On `ecs-ec2`
(8 GiB slot, 2026-10-21) a member's sessions drove the container to its cap. Linux never OOM-killed
anything, because file-backed pages could still be reclaimed. The box spent about twenty minutes in
reclaim thrash: EBS read operations went from ~100 to ~190k per five minutes, CPU sat at 35–95%, the
Agent stopped answering (every Control Plane proxy call timed out), and eventually the slot's ECS and
SSM agents went dark. A second member hit the same pattern the same day: the Agent log was silent for
forty minutes while ECS still reported the task HEALTHY.

What is already in the tree, and what it does not do:

- **The cap and the reserve** (`workspaceMemCapMiB`, `runtime_ecs_ec2.go`; #2451) hold 2 GiB of an
  8 GiB slot back from the container, and **`MemoryMin` on `system.slice`** (`40-ec2-pool.yaml`,
  #2452) keeps the box's daemons' pages out of reclaim. Together they confine the thrash to the
  workspace's own cgroup. They do not make it stop: inside that cgroup everything — including the
  Agent — still competes for the same pages.
- **Idle-stop** (`control-plane/reaper.go`) halts an *idle* claude session and later an idle
  workspace. Memory is not an input to it: a busy box is not idle.
- **`resources.Read`** (`workspace/agent/internal/resources`) already reports `memory.current`,
  `memory.max` and `memory.events` `oom_kill` on the 4-second Stats tick (an axis that cannot be read
  is omitted, never zeroed). It reports no pressure, and `memory.current` is the wrong gauge on its own:
  it counts page cache, so a healthy box that has read a large repository sits near the cap.
- **The Agent can already stop a session resumably**: `HandleHaltSession` kills the tmux session (or
  drops a Managed session's runtime handle), keeps the row, and stamps it stopped.

What is missing is a signal that separates "the cache is full" from "the box is thrashing", something
that acts on it from inside the container, and a way to tell the member. This record decides those.
It decides design only.

## Decision

### 1. The signal is the container cgroup's PSI, read by the Agent itself; usage figures only corroborate it

Read from the container's own cgroup directory (the `AF_CGROUP_DIR` the Agent already honours), once
per sample, every 5 seconds:

| File | What is taken | Used for |
|---|---|---|
| `memory.pressure` | `some` and `full`: `avg10`, `avg60`, and the `total` microsecond counters | **the trigger** |
| `memory.stat` | `anon`, `file`, `shmem`, `inactive_file`, `workingset_refault_file` | the working set, and the refault rate as a second thrash signal |
| `memory.current`, `memory.max` | working set = `current − inactive_file`, against `max` | corroboration, and the member-facing "N of M" |
| `memory.events` | deltas of `high`, `max`, `oom`, `oom_kill` | evidence (a `max` event means allocations hit the limit and had to reclaim) |

PSI is the trigger because it is the only figure that measures the harm — the share of wall time in
which tasks stalled on memory — instead of a cause that is sometimes harmless. `some` is "at least one
task stalled"; `full` is "every non-idle task stalled", which is the signature of the incident. The
Agent samples the counters itself and does not poll through PSI triggers (a write to `memory.pressure`
needs the file's owner, and the file is root's in the container; the Agent is uid 1000).

Where each runtime stands. This is what the code and the documented defaults say, **not measured on
each** except where marked:

| Runtime | cgroup v2 + `memory.pressure` that is the container's own (verified, see below) | What the Agent does |
|---|---|---|
| `ecs-ec2` | expected (AL2023 is cgroup v2 by default; the container gets its own cgroup namespace). **Measure on a real slot** (decision 8) | full ladder where verified |
| `ecs` (Fargate) | **unknown.** Platform kernel and PSI exposure are not documented to us | measure; ladder only if verified |
| `docker` | on a cgroup v2 host with PSI enabled. A development Workspace on the fleet host read `memory.pressure`, `memory.events` and `memory.max` as uid 1000 on 2026-10-22 | full ladder |
| `kubernetes` | depends on the node (cgroup v2, `psi` not disabled). The memory limit is set on the container's resources (`runtime_kubernetes.go`), so the container's own cgroup is the one that carries it | full ladder where verified |
| `native` | the Agent runs on someone's own machine: a readable `/sys/fs/cgroup` there is not necessarily a cgroup of its own, and other OSes have none | **warn only** (stage 1), never refuse or stop |

Where PSI cannot be read or verified, and where the cgroup is v1 (the Agent already falls back to the v1 usage
files, which have no pressure):

- **Verified means resolved, not merely readable.** A readable file is not proof that it describes this
  container: it depends on the daemon's cgroup-namespace setting (docker) and on whether the machine gave
  the Agent a cgroup of its own (`native`). At start the Agent resolves its own cgroup from
  `/proc/self/cgroup`, checks it against `mountinfo` (the cgroup2 mount's root and the path it reads), and
  accepts it only if the directory it reads is that cgroup (or, under a private cgroup namespace, the
  namespace root) and `memory.max` agrees with the limit the runtime was asked to set. Anything it cannot
  verify is treated as "no PSI" below: measure and warn only.
- The Agent reports `mem_pressure` **absent**, not zero — the same rule as every other Stats axis.
- Only **stage 1 (warn)** may run, from the working-set ratio and the major-fault rate, and it says so
  ("pressure cannot be measured here; this is a usage estimate").
- **Stages 2 and 3 never run without PSI.** Refusing work or stopping a session on a usage ratio alone
  would punish a box that is merely caching, and that is the false positive that makes members turn the
  feature off. When in doubt, do nothing.
- `/proc/pressure/memory` (the box-wide file) is **never** used for a container: it is not namespaced,
  so on a shared docker host it is other members' pressure. It is read only on `native`, where
  the box is the member's, and there it feeds the warn stage only.

Alternatives not taken:

- **A PSI responder outside the Agent.** Three places could run one, and they differ:
  1. *Inside the container, as the Agent (this record).* Portable to every runtime, knows sessions, can
     announce, hold and resume. Its weakness is the one the incident showed: it shares the thrashing
     cgroup.
  2. *On the `ecs-ec2` host, as `systemd-oomd` or a similar root daemon.* Escapes that weakness, and it
     is the strongest alternative. It is not taken now because it acts on the whole container cgroup (the
     unit it can stop is every session at once, with no choice of victim, no announced grace, no resume
     semantics), it exists only on one runtime, and its pressure limits would still need the numbers
     phase 0 produces. Its role as the *protector* of the Agent is left to open question 3.
  3. *Delegated sub-cgroups, one per session, with `memory.high` and a kill on each.* Would make
     attribution exact and `memory.high` meaningful per session. It needs root setup at launch on every
     runtime, the no-internal-processes rule (a cgroup with children cannot hold processes itself, which
     reshapes how the Agent and tmux are placed), and cannot split a shared-daemon session's threads
     from the daemon's. Too large a change for a design whose thresholds are not yet known.
  `earlyoom` reads the host's `/proc/meminfo`, which inside a container is the box's and not the
  cgroup's.
- **The kernel OOM killer (`memory.oom.group`).** It is the failure being avoided: it did not fire in
  the incident, and when it does it picks by `oom_score`, not by what the member can resume.
- **Deciding in the Control Plane from `mem_used`.** The CP sees only the Stats tick, no pressure, and
  in the incident its proxy calls were the ones timing out. The CP gets the facts (decision 5) and
  never the authority to stop a session from a figure it cannot judge.
- **Swap or zram to soften the cliff.** Needs root, and only postpones it.

### 2. A staged response: warn, refuse, stop — each with a sustained window, a confirmation, and an exit hysteresis

Thresholds are on `some avg10` and `full avg10` (percent of time stalled); `avg60` is the damper. All
values are **provisional** (decision 8 replaces them with measured ones) and are internal constants,
not member settings.

| Stage | Enter when, sustained | Leave when, sustained | Effect |
|---|---|---|---|
| 0 ok | — | — | — |
| 1 warn | `some avg10` ≥ 10 for 60 s, or working set ≥ 85% of `max` for 120 s | `some avg10` < 5 and working set < 80% for 120 s | Console notice with the figures |
| 2 refuse | `some avg10` ≥ 25 for 30 s, or `full avg10` ≥ 5 for 30 s | `some avg10` < 12 and `full avg10` < 2 for 120 s | new sessions and resumes are refused with a clear error |
| 3 stop | `full avg10` ≥ 15 for 60 s **and** `avg60` of `full` ≥ 8 **and** working set ≥ 90% **and** anonymous plus shared memory is ≥ 60% of the working set | stage 2's exit | after an announced grace (below), halt the chosen session |

How a short build spike is not mistaken for a thrash:

- **"Sustained for W seconds" is defined on timestamps.** The predicate must hold at every sample in
  the last W seconds, and consecutive samples must be at most 10 s apart (twice the interval). A missed
  or late sample **resets** the window for going up a stage and **defers** a pending stop; it never
  counts as pressure and never as relief. This is testable with a fake clock.
- **A compiler's 20-second burst moves `avg10` and barely moves `avg60`;** stage 3 needs both.
- **Stage 3 requires the working set to be mostly anonymous or shared-memory pages** (`anon + shmem` ≥
  60% of it). This is a conservative scope for the one destructive action, not a claim that
  file-dominated pressure is harmless: an active file working set that no longer fits can thrash for
  real (sustained `full`, rising `workingset_refault_file`) with a low anonymous share, and halting a
  session may not help it. That shape stays at stage 2, is logged as "file-dominated, not handled", and
  is something phase 0 must measure (decision 8). `shmem` is counted with anonymous memory because
  without swap it cannot be dropped.
- **An announced grace, then a fresh decision.** On entering stage 3 the Agent posts the notice
  ("session X will be stopped in 30 s unless memory frees up") and waits 30 s. The stop then runs only
  if a sample taken **after** the grace, no older than 10 s, still satisfies **every stage 3 entry
  predicate** (instantaneous values, not the 60 s window), the chosen victim is the same session, and
  its estimated reclaim (decision 3) is above zero. Otherwise nothing is stopped and the stage stays at
  2. Stage 2's exit hysteresis (120 s) governs only when the *refusal* is lifted; it is not the
  condition for cancelling a halt. A stale sample never authorises a halt: if the sampler is late, the
  stop waits, while the refusal is kept.
- **One stop at a time, with a cooldown.** After a stop the Agent waits 120 s for the figures to
  settle before it may choose again; a stop that freed nothing measurable is logged as such and is not
  retried on the same session.
- **Refusing is cheap and reversible, stopping is not**, so stage 2 is entered much earlier than
  stage 3 and leaves with a longer window. A flapping stage is a bug: the Agent never changes stage
  more often than its window, and logs the transition once, with the figures.

The refusal (stage 2) is made by the Agent's own handlers for creating, resuming and recreating a
session, which return the error code `memory_pressure` with the current figures and the biggest
consumers; the Console renders the localized sentence. **Wake-ups count as starts**: a scheduled run,
a peer message or an MCP call that would resume a stopped session is refused the same way and stays
held — the Agent already holds prompts for the next start — otherwise the session the Agent just
stopped would be revived into the same pressure by the next timer.

### 3. What is eligible, how the victim is chosen, and what is never touched

The unit that is stopped is a **session**, and the stop is the existing halt (`HandleHaltSession`):
the row stays, the session reads "stopped", and it resumes through the same path as an idle-stopped
one. A stop that cannot be resumed is not offered. The halt records `stopped_reason: memory_pressure`
in the session's meta so the Console can say why.

**Measuring a session cheaply.** In the enforcing path nothing per-process is measured at stage 0; from
stage 1 on (and in phase 0's own slow sampler, decision 8) the Agent walks `/proc` once per sample (the `procx` snapshot already does this at most every 750 ms)
and, for each process, reads `RssAnon` and `RssShmem` from `/proc/<pid>/status`. It does not use RSS: it
double-counts shared and file-backed pages, which are exactly what the kernel can drop. At the moment of
choosing, the top few candidates are re-measured with `Pss_Anon` + `Pss_Shmem` from `/proc/<pid>/smaps_rollup`, which
divides shared pages fairly between their users and costs milliseconds per process, not per sample.

**Attributing a process to a session.** First by the process tree under the session's tmux pane
(`procx` already has the parent and session ids). A dev server that daemonized or a build that was
reparented leaves that tree, so second by the `AF_SESSION_NAME` that the session's processes carry in
their environment (readable at the same uid; cached per pid and start time, not read every sample). What
cannot be attributed is reported as "other" with its command name, never charged to a session.

| Process | Eligible? |
|---|---|
| agent sessions (`claude`, `codex`, `opencode`, …) and everything they started: builds, test runs, dev servers, headless Chromium | **Yes**, as the session's footprint, but only the part a halt is **guaranteed** to free counts toward the expected reclaim (see "What a halt frees") |
| `shell` sessions | **Only if** the shell's footprint is above twice the largest agent session's. A halt kills a shell's live jobs, which are not durable (the reaper's idle tier skips shells for the same reason), so a shell is chosen when it is clearly the cause and not as a tie-break |
| sessions by process model (`ProcessModel` in `agents/driver.go`) | `tui`: the tmux pane's tree, as above. `per-session-child` (copilot, cursor, kiro, muse): the child **with its own body** counts, and `DropHandle` stops it. `shared-daemon` (codex, opencode): ranked by the processes attributable to the session only, with the daemon's baseline excluded; whether `DropHandle` (codex also unsubscribes the thread) returns memory from the daemon is **not measured**. `in-process` (lcpp): its memory is inside the Agent and cannot be attributed, so it is **never chosen on memory grounds**; the safe side. Each model gets its own reclaim check in phase 0/3 |
| orphaned headless Chromium (its session is gone) | **First, before any session**, as a stage 3 pre-step: it costs the member nothing |
| `af-db` servers | **Never stopped automatically.** They hold data a stop would interrupt. They are listed as consumers in the notice, with the member's own `af-db stop` as the offered action |
| the workspace Agent, the tmux server, and whatever the Agent runs for the Console (preview and port proxies, the terminal bridge) | **Never.** Stopping any of them ends every session at once |

**What a halt frees.** `HandleHaltSession` is guaranteed to remove the tmux session's tree, nothing more:
a child that called `setsid`, daemonised or was disowned survives `kill-session`, and so does a process
that `AF_SESSION_NAME` attributes to the session but that was reparented (`procx.ToolProcessIn` exists
because some CLIs start their tools in a session of their own). So the expected reclaim is the footprint
of the guaranteed part, and a pressure halt carries an **additional contract** for the rest: after the
halt the Agent signals the processes it attributed to the session, only those whose identity it
re-verifies (same uid, same pid **and start time**, still carrying the session's marker, not in the
protected set below), `SIGTERM` then `SIGKILL` after a short wait, and then re-measures to confirm they
are gone. A process whose identity cannot be re-verified is left alone and reported as "still running,
could not be attributed with confidence". The reclaim is logged as *freed* only from the re-measurement.
`procx.Info` has no start time today; the identity check needs it added.

**Unknown is not zero.** Every per-process reading (`status`, `smaps_rollup`, `environ`) can fail:
`smaps_rollup` and `environ` are guarded by a ptrace read check, so a process of another uid, or a
non-dumpable or sandboxed one, may be unreadable though the same-uid case usually works. Each
measurement therefore carries known / partial / unknown. A partial sum is never compared as a full
footprint, and a session with unknown processes is not ranked below one without; if the top
candidates include one, the stop is deferred (and logged) instead of choosing among the readable.
Unreadable or unattributable is **never** evidence that a process is an orphan: Chromium is treated as
orphaned only when a non-empty, valid `AF_SESSION_NAME` was positively read from its environment, that name is a stopped or deleted session, and it is not the Chromium the Agent keeps for the member's own viewing or otherwise protected. No marker, an empty or unknown one, or an unreadable environment (a browser the member started by hand) is "other" and is left alone. At start the Agent runs a
positive control (a same-uid process reads fine) and a negative control (a denied read is recognised as
denial), and enforcement is off if either does not behave; phase 0 records the same check.

Choice among agent sessions: the largest `Pss_Anon` + `Pss_Shmem` footprint, except that **a session in the middle
of a turn is passed over for an idle one whose footprint is within 25% of it** (halting mid-turn
aborts the turn; the transcript is durable, the turn in flight is not). The notice names the session,
its footprint, and what was consuming it.

Interaction with what exists:

- **Idle-stop** keeps running and is complementary: it halts the idle ones on a timer, the pressure
  ladder halts the largest ones on a measurement. Both use the same halt, so a session stopped by either
  resumes the same way. Neither is told about the other: the pressure ladder shortens the time that
  idle-stop would have taken, it does not replace its timeout.
- **The reaper's workspace stop** is not affected: a workspace in stage 3 has activity by definition.
- **Session resume** is the only recovery path and is refused while stage ≥ 2, with the error of
  decision 2. A session stopped for memory shows a "stopped to free memory" reason and the normal resume
  action.

### 4. `memory.high` is not set in the first phases; the Agent is the responder, and a throttle without a responder is worse

`memory.high` throttles a cgroup's allocations in direct reclaim before `memory.max` is reached. It does
not kill anything. With no swap, a cgroup whose usage is mostly anonymous memory and that sits above
`memory.high` is slowed forever instead of failing — everything in it, the Agent included, runs
slower — until something stops a process. That something is the ladder of decision 2, which would
itself be throttled. So `memory.high` before the ladder is proven is the incident over a longer time.

It is not available uniformly either. Docker and ECS expose `memory` (→ `memory.max`) and
`memoryReservation` (a soft limit), not `memory.high`; Kubernetes exposes it only through the node's
`MemoryQoS` feature. The Agent cannot write it (uid 1000 does not own the container's cgroup files). Only on `ecs-ec2`
is there a place that can: the slot's own boot script, which already runs as root, finds the container's
scope and sets `MemoryMin` on the management plane (`af-protect-mgmt`, `40-ec2-pool.yaml`).

The decision: **no `memory.high` in phases 0–2.** Phase 3 revisits it for `ecs-ec2` only, on one
hypothesis the measurement can confirm or refute: *a `memory.high` at about 92% of the cap makes
PSI rise, and `memory.events` `high` count up, earlier than `max` does, giving the ladder a longer
runway before the cap.* How it composes with the existing numbers if adopted:

- #2451: the container cap stays `slot − 2048 MiB`. `memory.high` is **inside** the cap and does not
  touch the 2048 MiB host reserve, whose size was set against the real `MemTotal`, not the nominal rung.
- #2452: `MemoryMin` protects the daemons from reclaim; it does not protect the Agent, which is in the
  workspace cgroup. This is the gap that no `memory.high` closes (open question 3).
- The sizing guideline (decision 5) is quoted against `memory.high` once there is one, not against the
  cap.

### 5. Surfaces: facts to the Console, notices to the member, a coarse switch to the admin

**Member.**

- **Stats gains one axis**, `mem_pressure` (`some`/`full` `avg10`, and the current stage), omitted when
  unreadable. The Console's resource display uses it to show a "memory is tight" state beside
  the usage bar, which today shows `memory.current` including cache.
- **Notices** go through the Agent's durable outbox (`internal/notice`) as workspace-level events, so
  they survive an Agent restart until the CP acknowledges them. One per stage transition, deduplicated;
  the text carries the number of running sessions, the stage, and the biggest consumers by name
  (session names and command names, never paths).
- **The refusal error** (decision 2) and the **stop notice** (decision 3) are the other two member
  texts. All three are Console strings in both languages.
- **Machine / sizing view.** A per-size guideline — "about N concurrent sessions on 8 GiB" — is shown as
  a guideline, derived as `(cap − baseline) / p75 session footprint`. Both inputs come from phase 0, so
  until phase 0 reports there is **no number** and the view says nothing rather than printing a guess.
  Once there is one, the Agent's own observed per-session footprint on this machine is preferred over the
  fleet-wide figure.

**Operator and audit.**

- One Agent log line per stage transition and per stop, with the figures that triggered it (`some` and
  `full` `avg10`/`avg60`, working set, `max`, the chosen session and its footprint, `Pss_Anon` + `Pss_Shmem`).
- The CP records stage transitions and automatic stops in the audit log, under actions of their own
  (no reuse of the member-initiated halt's action), so "who stopped this session" has the answer
  "the memory ladder".

**Settings.** One tenant setting, edited where the tenant's other limits are, with four values:
`off`, `measure`, `warn`, `enforce`. `measure` collects and logs with no member-visible effect; `warn`
adds stage 1; `enforce` adds stages 2–3. The deployment default comes from an environment variable of
the same four values, and the tenant value narrows it (a tenant can turn it off where the deployment
turned it on). **Thresholds are not tenant settings** in the first version: four numbers a tenant can
mis-set are four ways to reproduce the incident, and decision 8 is going to change them anyway. An
operator-only override of the constants is possible through the environment.

### 6. The Agent must be able to act when the box is slow, and cannot when it is stuck

The sampler is a single goroutine that reads a handful of small files and does no allocation worth the
name, so it keeps working through a slow box. It cannot work through a stuck one: in the incident the
Agent did not answer at all, and nothing running inside the same cgroup can act while it is being
reclaimed. Two cheap measures are in scope, and the rest is an open question:

- the Agent starts session processes with a **higher `oom_score_adj`** (raising it needs no privilege),
  so that if the kernel's OOM killer does fire it chooses a session before the Agent;
- the refusal is **held** while the sampler is late (it is cheap and reversible), but a stop is never
  authorised from a stale sample (decision 2).

What neither does is protect the Agent from reclaim. That needs a place to run it that the container's
pressure does not reach (open question 3), and it is why this record talks about "before the box
thrashes" and does not claim to prevent it.

### 7. Alternatives considered for the response itself

- **Warn only, never act.** Safe, and does not solve the incident: nobody was watching a Console during
  twenty minutes of thrash. Kept as the `warn` mode.
- **Kill the largest process instead of a session.** Cheaper and more exact, and resumes nothing: a
  claude child killed under a live session leaves a session that is "running" and broken. The session
  is the unit that can be explained and resumed.
- **Freeze (`SIGSTOP`) instead of stop.** It does not release anonymous memory, so it does not help.
- **Refuse only, never stop.** Stage 2 alone leaves a box that is already over its limit over it; the
  incident's sessions were all already running.

### 8. Rollout: measure first, enforce per tenant, and pick numbers from what is measured

| Phase | Default | What ships |
|---|---|---|
| 0 measure | **on** for every runtime where it reads; no member-visible effect | the sampler, the log lines, the Stats axis (the `measure` mode) |
| 1 warn | on for `ecs-ec2` after phase 0's numbers are read; opt-in elsewhere | stage 1 notice, the sizing guideline |
| 2 refuse | opt-in per tenant | stage 2 |
| 3 stop | **off** until a real slot has been driven through it on purpose; then opt-in per tenant | stage 3, the victim selection, the stop notice, the `memory.high` revisit |

What phase 0 records, per runtime and per size, kept as a time series (not only the latest):

1. **How it is collected.** In `measure` mode the Agent runs a per-session sampler **independent of the
   stage**, every 60 s: `status` for every process, `smaps_rollup` for the processes of a
   **budgeted set of sessions** per sample (at most 5 sessions, 50 processes; it walks page tables and briefly holds the mmap lock, so it is capped
   and skipped while `full avg10` is at stage 2 level). Sessions are picked round-robin within each kind, so a small session is measured as surely as a large one; a chosen session is measured as one observation over **all** its attributed processes, and if the budget runs out part-way the observation is recorded as missing, never as partial. Coverage (sessions measured / sessions alive, per kind) and missing observations are recorded with the series. A session's *baseline* is the median of its footprint
   while idle (no turn, no child beyond the CLI); the box's baseline is the Agent plus unattributed
   processes. The series is kept in the Agent's state directory as a capped file (about 7 days at this
   rate, a few MB) and downsampled beyond that; the Stats axis carries only the latest value.
2. `some`/`full` `avg10`/`avg60`/`total` and the working set, to place the thresholds against what a
   healthy busy box looks like (a large build; N sessions; chromium).
3. The refault rate against PSI, to confirm it is a usable second signal, and the anonymous / file /
   shmem split under real load (the incident's was never recorded).
4. Per-session footprint (`Pss_Anon` + `Pss_Shmem`, stored separately, summed on use; this is the one figure the sizing guideline, the log lines and the victim ranking all use) over a session's life, by session kind, to get the p75 behind the sizing
   guideline and the "twice the largest agent" rule for shells. No sizing number is shown until a kind has enough complete observations; how many, and how the p75 is estimated, phase 0 fixes, not this record.
5. The Agent's own latency (a loopback request) against PSI, to find the pressure at which the Agent
   itself becomes unresponsive — the number that decides whether the ladder can act in time.

Phase 3 does not start before one slot has been pushed past stage 3 on purpose and the ladder's whole
sequence observed, including the resume of the session it stopped. The load is not only a synthetic
anonymous allocator (which cannot show the file-dominated blind spot). Acceptance runs:

- anonymous allocator; a mixed file + anonymous load with rising refaults; long Go, npm and Gradle builds
  (must not stop anything, or must show why); a short spike with recovery; a missing sample during the
  grace;
- a session whose child `setsid`-detaches, and one whose child is reparented: the memory of both is
  **freed** after the halt (the reclaim contract of decision 3), verified by re-measurement;
- a `shared-daemon`, a `per-session-child` and an `in-process` session, each checked for what the halt
  returns;
- the Agent's latency and any missing samples recorded during the run, as the evidence that the halt
  came in time. That the Agent is starved under the worst load is a known limit (open question 3), not a
  blocker, but the evidence must be on record.

The cases the ladder does not catch (file-dominated thrash, in-process sessions, unreadable processes,
a stuck Agent) are listed in the phase 3 record, not hidden.

## Open questions

These are not decisions and have no owner yet.

1. **Do the thresholds hold?** `some` 10/25 and `full` 5/15 are borrowed from how such ladders are
   usually set, not measured here. The numbers must come from phase 0 on a real slot, at least 8 and
   16 GiB, with the incident's load reproduced.
2. **Is `memory.pressure` readable on Fargate, and on a real `ecs-ec2` slot, inside the container?**
   The `docker` case was read on a development Workspace; the other two are expectations from the
   platforms' documentation.
3. **How is the Agent protected from reclaim?** `MemoryMin` (#2452) protects the daemons and not
   the Agent, which lives in the cgroup that thrashes. Options: a sub-cgroup with `memory.low` for the
   Agent set by the slot's boot script (`ecs-ec2` only); running the Agent outside the capped container
   cgroup; a CP-side dead-man's check that stops a workspace whose Agent has been silent beyond N
   minutes while ECS says HEALTHY (the second member's case).
4. **`shared-daemon` sessions** (codex, opencode) share one daemon. How much does `DropHandle` really
   return, and is there any action that frees more short of restarting the daemon (is restarting it
   acceptable)? And what is the right treatment of `in-process` sessions, whose memory is the Agent's own?
5. **Is the `AF_SESSION_NAME` environment marker present on every process a session starts**,
   including Managed sessions' tool processes? It decides whether
   attribution beyond the process tree works.
6. **Should a member be able to protect one session** from the automatic stop (a pin), and does the
   answer change when the box is at stage 3 with only pinned sessions left?
7. **What does the member do next?** A stop notice that does not offer an action ("move this
   workspace to a bigger size") is half a message. Whether that link belongs in the notice depends on
   the sizing flow and on whether the member can resize at all.
8. **`kubernetes` and `native`** may deserve a narrower mode than `ecs-ec2`: on `native` the box is the
   member's laptop, and stopping a session because the machine is busy with something else may be
   unwelcome.
9. **A session's own `ulimit -v` or `MALLOC_ARENA_MAX`** would reduce the spread before the ladder is
   needed. Worth a measurement; it is not a replacement.

## Consequences

- **Good:** the incident has a visible early state, a clear error instead of a silent wedge, and — once
  phase 3 is on — a bounded response that leaves a resumable session. Nothing here changes a
  runtime adapter, the cap, or the host reserve.
- **Good:** phase 0 alone produces the data the sizing view and every threshold need, with nothing
  user-visible. It is not free: a 60 s walk of `/proc` and a capped number of `smaps_rollup` reads, whose
  cost on a box with many processes is itself something phase 0 measures first.
- **Bad, accepted:** until Open question 3 is answered the ladder is a best-effort responder on a box
  whose responder can be starved. It improves the common case (a slowly rising load that PSI shows for
  minutes) and cannot help the case in which the Agent is already stuck.
- **Bad, accepted:** a stop can interrupt a turn. The notice says which session, why, and how to resume.
- **Bad, accepted:** `native`, `kubernetes` and Fargate may run in the degraded mode of decision 1 for
  as long as it takes to learn whether they read PSI.
- **Not decided here:** the thresholds' final values, the default for each phase after phase 0, the
  protection of the Agent itself, and any change to `memory.high`.

# 0087. EFS metadata I/O - stay on elastic for now, and put back on EFS only what has to be there: credentials

English | [日本語](0087-efs-metadata-io.ja.md)

- Status: **proposed** (2026-09-17). One piece is implemented: the CloudFormation change in
  decision 2.
- **What was measured, and where.** The numbers come from three places only. (1) CloudWatch
  metrics for the production deployment's data file system (`MeteredIOBytes` /
  `MetadataIOBytes` / `ClientConnections`, 2026-09-04 to 09-17); (2) Cost Explorer actuals
  and the ap-northeast-1 unit prices from the Pricing API (read 2026-09-17); (3) **`strace -c`
  inside a Workspace container** - the agent's real code compiled into a test binary and
  started as a child process, counting file syscalls (attaching to the running Agent is
  refused: `ptrace_scope=1`, so it has to be a child). **No production box was straced.**
- Related: [0044-workspace-sizing.md](0044-workspace-sizing.md) (where `~` actually lives) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (the decision that
  introduced the `keep` volume - what it was meant to hold versus what it holds).

## Background

### What happened on 2026-09-16

The whole production deployment went slow. The cause was neither an API nor the database but
the mount underneath them. The EFS file system that carries workspace configuration and
credentials was on `ThroughputMode: bursting`, and it had **spent its burst credits**. The
bursting baseline is 50 KiB/s per GiB of Standard storage, and this file system holds only
config and credentials (about 2.6 GiB), so **the baseline is roughly 0.13 MiB/s**. The moment
the credits ran out, every workspace was pinned to it - an `openat` of a 2.4 KB file took
5.3 seconds.

Why *everything* got slow follows from what is on EFS. A production workspace runs on the EC2
launch type and has three volumes:

| Volume | Backed by | Contents |
|---|---|---|
| `home` → `/home/dev` | **instance EBS** (host path) | the real home, including `~/repos` |
| `claude` → `/var/lib/af/claude` | **EFS** (access point) | `CLAUDE_CONFIG_DIR`: `projects/`, transcripts, settings |
| `keep` → `/var/lib/af/keep` | **EFS** (access point) | `~/.config`, `~/.ssh`, `~/.claude`, `~/.codex`, `~/.gitconfig`, `~/.git-credentials`, `~/.claude.json`, symlinked out of `~` |

`control-plane/internal/runtime/runtime_ecs_ec2.go:3650` injects `CLAUDE_CONFIG_DIR`, `:3652`
injects `AF_WS_KEEP`, and `workspace/entrypoint.sh:20-52` symlinks the seven items out of `~`
into `keep`. So every single `git` invocation (`~/.gitconfig`) and every `claude` start
(`CLAUDE_CONFIG_DIR`) costs EFS metadata I/O. That is where the contradictory symptom comes
from: the working copies are on EBS, and yet *everything* is slow.

As a stop-gap the live file system was switched to `elastic` from the CLI (it took about a
minute; `/api/repos` went from 20 s to 272 ms). In parallel, the transcript search -
`filepath.Glob("projects/*/<sid>.jsonl")`, which read every project directory on every poll -
was fixed in PR #711 / 0.20.2, shipped as 0.21.0, and every user restarted.

### What was left once every box was on the new agent

**PR #711 halved the metadata I/O; it did not end the storm.** Measured on the morning of
09-17:

| | old agent (09-10..09-14) | new agent 0.21.0 (09-17) |
|---|---|---|
| per box, p50 | 1.04-1.22 MB/s | **0.55 MB/s** |
| per box, p99 | 2.75-3.12 MB/s | 1.47 MB/s |
| per box, max (1-minute) | 4.02-4.46 MB/s | 1.83 MB/s |
| file system max (1-minute) | 18.2 MB/s (7 boxes) | 5.9 MB/s (4 boxes) |

Boxes are counted from `ClientConnections` (Sum / period x 60). **One box = two connections**,
and the Control Plane does not use EFS at all, so it contributes nothing.

🔥 **The hour when exactly one box was running (09:06-10:05 JST) sat dead flat at
0.44-0.45 MB/s.** AWS states that "every NFS request is accounted for as 4 kilobyte (KB) of
throughput, or its actual request and response size, whichever is larger" (EFS User Guide,
Performance modes). So **0.44 MB/s / 4 KiB is about 110 NFS requests per second** - that is
the new floor for one user sitting idle. The user reads that as "half a megabyte per second
for one person is a lot". They are right. There is no reason for a file system that serves
config files to make 110 round trips a second.

⚠️ **Do not adopt "it will fall to 0.2 MB/s" as the target.** Across twelve days, 0.2 MB/s
only ever appears when zero or one box is running; during working hours the deployment has
been in a 0.7-1.9 MB/s per-box band continuously since 09-07.

## Measurement 1: what produces 110 requests per second

`ptrace_scope=1` refuses an attach to the running Agent, so instead **the agent's real code
was compiled into a test binary and started as a child**, counted with
`strace -c -e trace=file`. Project and session counts were set to production scale.

### Source A: `ListMetas()` - the session ledger lives on EFS (largest)

`workspace/agent/internal/session/meta.go:17`:

```go
// MetaDir lives in the home volume (persists across Stop→Start) under the
// denylisted .config/agent-fleet, so stopped sessions survive a Workspace restart.
func MetaDir() string { … filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "sessions") }
```

🔥 **That comment is false on ecs-ec2.** `~/.config` is in the default `AF_WS_KEEP_DIRS`
(`.config .ssh .claude .codex`), so the entrypoint **symlinks it into `keep`, which is EFS**.
The session ledger is on EFS.

`ListMetas()` does one `ReadDir` and then **one `os.ReadFile` per session meta**. Measured
(207 metas, 100 calls, `strace -c`):

```
openat 21,014   read 41,406   close 21,013   getdents64 204   = 83,637 syscalls
→ 836 file syscalls per ListMetas() call (openat 210 / read 414 / close 210)
```

It has 32 callers, one of which is the **`GET /sessions` handler**. The frequency is set by
the comment at `control-plane/events.go:145`:

> this tick function runs once per open tab every 4 seconds

**Once every four seconds per open Console tab**, the CP calls the Agent's `/sessions`, and
that one call opens and reads 207 files on EFS. Two tabs double it.

`~/.config/agent-fleet` measures **113 MB across roughly 5,400 files**. What ADR 0045 intended
`keep` to hold was "auth, connections and identity - seven items, under 100 MiB in total".
What is actually there:

| Directory | Files | What it is |
|---|---|---|
| `session-status/` | 1,220 | **written from the polling path** (`status.Persist(sid, "working")`) |
| `pending-perm/` | 1,429 | transient permission state |
| `chat-wd/` `chat-codex/` `chat-claude/` | 971 | chat working directories (93 MB) |
| `sessions/` | 207 | the session ledger |
| `session-injections/` `session-exit/` `codex-sid/` … | ~1,500 | assorted mutable state |

Not one credential among them. **The whole of the agent's mutable state ended up on EFS
because the keep list is directory-granular (`.config`)** - that is the entire story.

### Source B: `subagentBases()` - the other side of "a miss is never remembered" (runner-up)

PR #711 introduced `jsonl_memo.go` to memoize the transcript search. Its invariant is
deliberately strict:

> A MISS IS NEVER REMEMBERED. Answering "no transcript" when one exists makes buildProgram
> pass `--session-id` instead of `--resume`, and claude exits with "Session ID is already in
> use".

Correct - but **the other user of that same memo, `subagentBases()`, misses permanently for
most sessions**. A session that never used a background agent has no
`projects/*/<sid>/subagents` directory, and since a miss is never remembered, **the full sweep
runs on every single call**.

The call path is the idle-state decision for every claude session
(`internal/agents/claude/claude.go:213` → `BackgroundWork` → `bg.go:185` `SubagentBusy`). It
is reached whenever `BackgroundBusy` (a cheap /proc check) is false, which is the normal case.

Cost per call, counted with `strace -c` (Go's `filepath.Glob` does one `ReadDir` of
`projects/` plus one `ReadDir` per matched directory):

| Project directories | File syscalls per `SubagentLogs()` call |
|---|---|
| 38 | **158** (getdents64 78.8 / openat 39.9 / newfstatat 39.8) |
| 318 | **1,296** |

Exactly `(1 + projects) * 4 + 2`. A development-deployment box was measured with **318**
project directories; the production measurement used 38. The tell from the earlier
investigation still applies: **if every directory shows exactly the same count, something is
looking for one thing at a time.**

The same shape survives on the transcript side: a memo hit costs one `Lstat` (measured: 5
syscalls per call), but **`memoTTL = 60s` drops back to the full sweep every minute**, and
until the first turn writes the jsonl the lookup is a miss and sweeps every time.

### Source C: writes to EFS from the polling path

`status.Persist(sid, "working")` is called from the state decision in `claude.go`. Under
elastic, writes cost **$0.07/GB** against $0.04/GB for reads, and every request is metered at
a 4 KiB minimum - so a status file of a few dozen bytes is billed as 4 KiB every time.

### Putting it together

One box, one Console tab, 207 metas, 38 project directories, three idle claude sessions:

```
ListMetas()          208 file reads       x 1/4s
SubagentBusy() x3    117 directory reads  x 1/4s
                   → roughly 80 file/directory operations per second
```

On NFS each operation is one to three round trips (LOOKUP / ACCESS / OPEN / READ / GETATTR),
which lands on the same order as the measured 110 requests per second. Both source A and
source B run **on a box where the user is doing nothing at all**.

## Measurement 2: the money

Unit prices for ap-northeast-1 (Pricing API, read 2026-09-17):

| Item | Price |
|---|---|
| Elastic reads | **$0.04 / GB** |
| Elastic writes | **$0.07 / GB** |
| Provisioned Throughput | **$7.20 / MiBps-month** |
| Standard storage | $0.36 / GB-month |
| bursting / provisioned I/O | **free** (included in the throughput) |

🔥 **Under bursting, I/O was free.** Cost Explorer shows $0.02 a day (storage only) through
09-15, and then `APN1-ETDataAccess-Bytes` appears on 09-16 at **235.21 GB / $9.47**, the day
the file system moved to elastic. Ending the outage converted what used to show up as
slowness into something that shows up as a bill - that is all that changed. The blended rate
was $0.0402/GB, so metadata I/O bills essentially entirely at the read rate.

The billing meter is `MeteredIOBytes` (the CloudWatch sum for 09-16 matched the billed
quantity). Twelve days of it:

| | `MeteredIOBytes` per day |
|---|---|
| weekdays (old agent) | 146-316 GB |
| weekends | 5-8 GB |
| 13-day average | 165 GB/day |

**A month of elastic at old-agent demand is about 4,940 GB = $198/month.** The new agent
measures at about half of that, so **about $100/month**.

### The three-way comparison (monthly)

New agent (0.21.0), nine boxes, 22 working days. The provisioned figure needs a peak: take the
old agent's 1-minute maximum of 18.2 MB/s at seven boxes, normalise per box, apply the
measured ~2x improvement and scale back up to nine boxes - **about 11-12 MB/s, i.e. about
11 MiB/s**.

| | (1) stay on elastic | (2) provisioned 24 MiB/s | (3) elastic, after the permanent fixes |
|---|---|---|---|
| I/O volume | ~2,600 GB/month | - (not billed) | ~300-600 GB/month (target: 4-8x less) |
| I/O cost | **~$104** | $0 | **~$12-25** |
| fixed throughput cost | $0 | **$172.80** | $0 |
| storage | ~$1 | ~$1 | ~$1 |
| **total** | **~$105/month** | **~$174/month** | **~$13-26/month** |
| ceiling | none | 24 MiB/s hard | none |
| how it fails | the bill grows | **it throttles and every workspace stops** | the bill grows |

🔥 **At today's demand, provisioned costs more than elastic.** The break-even provisioned
amount is about 14 MiB/s, which is the same height as the estimated peak (11-12 MiB/s) - that
is, you would buy zero headroom just to reach parity. And as this deployment has now learned
repeatedly, an EFS at its ceiling does not fail as "a bit slow"; it fails as "no workspace
works".

### The 24-hour restriction, stated precisely

AWS does not say "one throughput-mode change per 24 hours". The EFS User Guide section
"Restrictions on switching throughput and changing provisioned amount" says that **after
switching to Provisioned throughput, or after changing the provisioned amount**, two actions
are restricted for 24 hours:

- switching from Provisioned back to Elastic or Bursting;
- **decreasing** the provisioned amount.

🔥 **Increasing it is not restricted.** So the risk of choosing provisioned is asymmetric: if
it is too small you can raise it immediately, but an oversized one cannot be lowered for 24
hours and you cannot go back to elastic either. The worry "if you skimp on the value you
cannot redo it the same day" is, precisely, "**you cannot go back**" - not "you cannot go up".

### How to tell you have hit the ceiling

If provisioned is chosen, watch these together:

- **`PermittedThroughput`** (Average) pins to exactly the provisioned amount. On elastic today
  it reads 5,400 MB/s (measured), so the value itself also confirms the switch took effect.
- **`MeteredIOBytes` goes flat at the same height as `PermittedThroughput`.** The "dead flat
  at 1.35 MiB/s" seen during the 09-16 credit exhaustion was exactly this: **a flat line is a
  ceiling, not a demand**. The real demand is unknowable until you release it - it jumped from
  1.35 to 19.5 MiB/s within minutes of the switch to elastic.
- `BurstCreditBalance` is not published under elastic. It comes back under provisioned.

## Decisions

### Decision 1: the stop-gap is to stay on elastic. Do not buy provisioned throughput

Per the table above, at today's demand provisioned is **70% more expensive** than elastic, and
it makes the failure mode worse: from "a bill" to "an outage". **Changing how the throughput
is bought removes exactly zero of the 110 requests per second.** The price difference only
starts to matter after the permanent fixes land - and at that point elastic gets cheaper on
its own, while provisioned, being a fixed cost, does not.

If the user prefers the predictability of a fixed cost anyway, the recommended amount is
**24 MiB/s** ($172.80/month). The reasoning: about 2x the estimated peak of 11-12 MiB/s, and
also **above the 22 MiB/s that the old agent's demand scales to at nine boxes** - so it does
not throttle even if 0.21.0's improvement were somehow lost. If it is not enough it can be
raised the same day; lowering it waits until the next. The CloudFormation default is set to
the same 24 (decision 2).

### Decision 2: make the throughput mode a parameter in `10-data.yaml` (implemented)

`deploy/aws/ecs/cfn/10-data.yaml:72` hard-coded `ThroughputMode: bursting`. The live file
system was only changed from the CLI, and the `af-ecs-data` stack has not been updated since
2026-08-25. **The next update of that stack would have flipped it back to bursting and re-armed
the outage** - with a 24-hour wait to undo it.

`EfsThroughputMode` (`elastic` / `bursting` / `provisioned`, default `elastic`) and
`EfsProvisionedThroughputMibps` (default 24) are now parameters, with the history written
directly above the resource. The default is `elastic` so that it matches live:
`aws cloudformation deploy` uses the template default for a **new** parameter that the
existing stack does not have, so leaving `bursting` as the default would have made the
parameterisation itself the landmine.

### Decision 3: alarm on `MeteredIOBytes`

Elastic has no ceiling, which means a runaway shows up as a bill rather than as slowness.
To close the "nobody noticed for a month" failure, alarm when the 30-minute average of
`MeteredIOBytes` exceeds **8 MB/s** (nine boxes at 0.9 MB/s each - about 1.6x today). This is
not a performance alarm, it is a **regression alarm**: so that the day someone lands the
inverse of PR #711, we find out from CloudWatch and not from the invoice.

### Decision 4 (permanent, P0): take the agent's mutable state off EFS

There is not one credential in the 113 MB / 5,400 files of `~/.config/agent-fleet`. It sits on
`keep` (EFS) only because `AF_WS_KEEP_DIRS` is granular to a directory named `.config`. The
comment in `meta.go` that believes it is on the home volume is a record of the design intent:
that is where it belongs.

There are two ways down, and this ADR prefers **(b)**.

- **(a) Narrow the keep list**: replace `.config` with `.config/opencode`, `.config/cursor`,
  `.config/acli`, … - only the entries that hold credentials. One line in the entrypoint, but
  the list has to grow every time a CLI is added, and forgetting one fails quietly as
  **logins that vanish on every restart**.
- **(b) Move the agent's state out of `.config`**: make `~/.local/state/agent-fleet` (i.e.
  home / EBS) the state directory and do a one-way migration from `.config/agent-fleet` once,
  in the entrypoint or at Agent start. No list to maintain, and the line "credentials on keep,
  mutable state on home" survives as a directory name. The env seam already exists
  (`AF_SESSIONS_DIR`).

⚠️ **The trade-off**: home on ecs-ec2 is **a single EBS volume in a single AZ** - which is the
very reason ADR 0045 created `keep`. Anything moved down is lost with that volume: losing the
session ledger empties the session list (the transcripts themselves live under
`CLAUDE_CONFIG_DIR` and survive). The judgement here is that this is a lighter loss than
losing credentials. **Credentials (`~/.ssh`, `.git-credentials`, `.claude.json`) stay on
EFS** - ADR 0045's line about not leaving plaintext on local disk does not move.

### Decision 5 (permanent, P1): stop the remaining two `projects/*` sweeps

- **Remember the `subagentBases()` miss.** The "never remember a miss" invariant on the
  transcript side exists to protect the `SessionJSONLExists` → `--resume` / `--session-id`
  branch; **the subagents side carries no such danger** (a false "no background agent" only
  lights the badge a few seconds late). Give it a negative cache with a short TTL (10-30 s).
  ⚠️ Do not apply the same change to the transcript memo - the type comment in `jsonl_memo.go`
  explains why the guard is written twice there.
- **Derive the project directory from the cwd.** claude encodes the cwd into the directory
  name (`/home/dev/repos/agent-fleet` → `-home-dev-repos-agent-fleet`), and the Agent already
  holds the session's `Meta.Dir`, so `projects/<derived>/<sid>.jsonl` is **one Lstat**, with
  the existing sweep as the fallback when it misses. The encoding is lossy (`.` and `@` also
  become `-`), so **the derivation is a guess, not the truth** - always keep the fallback.
- The transcript side's `memoTTL = 60s` returns a hit to the full sweep once a minute. Once
  the derivation is in, re-searching is cheap, so revisit that constant afterwards.

### Decision 6 (permanent, P2): on mount options, establish first what is *not* possible

ECS's `EFSVolumeConfiguration` carries only `FileSystemId`, `RootDirectory`,
`TransitEncryption`, `TransitEncryptionPort` and `AuthorizationConfig` - 🔥 **there is no field
for NFS mount options** (verified against the aws-sdk-go-v2 type). So `actimeo`,
`lookupcache`, `nconnect` and `noatime` **cannot be set from a task definition**.

There is exactly one way to set them: **mount the file system yourself on the EC2 instance and
pass it in as a host-path volume**. There is precedent - on ecs-ec2, `home` is already a host
bind of `/af-home/<membershipID>/dev`. But:

- the per-member root-directory isolation and POSIX uid mapping that access points provide
  would have to be rebuilt from a hand-rolled mount plus subdirectories (the isolation
  guarantee moves from the template into code);
- transit encryption goes through efs-utils' stunnel, so `nconnect` cannot be combined with it;
- the effect is **unmeasured**. Decisions 4 and 5 remove round trips outright; this one changes
  the weight of a round trip and how well the cache holds, so it comes later.

**Conclusion: judge P2 after decisions 4 and 5 have landed and been re-measured.** Doing it
first risks being left with nothing but a rebuilt isolation story.

### Decision 7: what was rejected

- **Buying provisioned throughput** (decision 1: more expensive at today's demand).
- **Going back to bursting**: getting the baseline up to demand would mean padding Standard
  storage to nearly 1 TiB, at $368/month. More expensive than provisioned.
- **Replacing EFS with S3**: `~/.gitconfig` and `~/.ssh` are opened as POSIX files. S3 would
  need FUSE, or an import at start and a write-back at stop - and the latter **loses credential
  updates whenever a box is SIGKILLed**. It collides head-on with the reason `keep` exists
  (ADR 0045).
- **EBS only**: losing the single-AZ EBS would take the login credentials with it. That is
  precisely why `keep` was created.
- **Lengthening the poll interval**: 4 s → 8 s halves the I/O at the cost of a slower UI.
  **The abnormal part is that one poll performs 800 file operations**, so fix the weight of a
  poll, not its frequency. Tune the frequency afterwards, against whatever is left.

## How to verify on real infrastructure

After decisions 4 and 5 land, verify in this order. ⚠️ A benchmark can be green everywhere and
still not have measured the deployment's wiring, so **always confirm against the CloudWatch
numbers**.

1. **Unit (`strace`).** Run the same shape of probe as this ADR - real code compiled into a
   test binary, started as a child, counted with `strace -f -c -e trace=file` - and confirm
   that `SubagentLogs()` drops from 158 syscalls per call to single digits, and that
   `ListMetas()`'s 836 becomes **zero on EFS** (because it moved to home).
2. **The one-box floor.** Arrange an hour with exactly one user running and watch the 5-minute
   `MetadataIOBytes` go flat. **0.44 MB/s (~110 req/s) is the starting point**; with decisions
   4 and 5 it should land near 0.1 MB/s. If it does not, the source is elsewhere - suspect
   `~/.gitconfig` lookups (how often `git` is started) and claude's own transcript writes next.
3. **Working hours.** Count boxes from `ClientConnections` (one box = two connections) and put
   the per-box MB/s next to the old agent (1.04-1.22) and the new one (0.55).
4. **The bill.** Watch `APN1-ETDataAccess-Bytes` in `ce get-cost-and-usage` over three working
   days. Anything finer than DAILY needs the payer account to have opted in (measured: HOURLY
   returns `AccessDeniedException`).
5. **The regression guard.** Confirm the decision-3 alarm stays silent for a week.

## Open questions

- **How much of it is claude itself.** Transcript appends and the repeated stats on the way to
  the settings file all happen under `CLAUDE_CONFIG_DIR` (EFS), and they have not been
  separated from the agent's own traffic. Whatever floor remains after decisions 4 and 5 is
  most likely here. The separation can be measured the same way: start claude as a child under
  `strace`.
- **The Fargate runtime is worse.** `runtime_ecs.go:684` puts **`home` itself on EFS** - every
  working copy under `~/repos`. Production runs ecs-ec2 so it does not appear in these
  measurements, but the same method should show an order of magnitude more. This ADR does not
  decide it.
- **How much the NFS attribute cache absorbs.** The syscall counts above are VFS-level, not
  NFS round trips. Directory contents are answered locally until `acdirmin` (default 30 s) and
  file attributes until `acregmin` (default 3 s). The "one to three round trips per operation"
  estimate comes from the ratio between the measured 110 req/s and the syscall counts; it was
  **not measured directly**. Reading `/proc/self/mountstats` on a production box gives per-op
  RPC counts, and needs no root.
- **What migration does to existing boxes under decision 4 (b).** If the Agent dies mid-way
  through the one-way migration, something has to decide which side wins.

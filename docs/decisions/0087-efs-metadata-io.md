# 0087. EFS metadata I/O - stay on elastic for now, and put back on EFS only what has to be there: credentials

English | [日本語](0087-efs-metadata-io.ja.md)

- Status: **proposed** (2026-09-17, review applied the same day). One piece is implemented: the
  CloudFormation change in decision 2.
- ⚠️ **Claims changed after the first draft**, overturned by measurement across two review
  passes. Each is marked in place ("the first draft said … and was wrong"). First pass: (1)
  `~/.config/agent-fleet` *does* contain the credential store; (2) a negative cache on the
  subagents lookup can break a safety check; (3) `MeteredIOBytes` is not the billed quantity;
  (4) the improvement is 13-46%, not "half"; (5) the floor after burst exhaustion is
  **1 MiB/s**, not 0.13 MiB/s; (6) a self-managed mount keeps access-point isolation. Second
  pass: (7) **the safety side is not just the delivery check** - the completion report and the
  firing of an armed stop go through the same `SubagentBusy`; (8) **the peak's denominator**
  (7 boxes at the peak minute, not 8) → the estimated peak is 12-15 MiB/s; (9) the cost table
  mixed two bases and the unit convention mattered; (10) "no READDIR, so B is not dominant"
  inferred the caller from the RPC mix. Third pass: (11) **billing is in GiB (2^30)** - lined up
  on the same numerator, window and minute resolution, CloudWatch and Cost Explorer agree to
  **0.12%** (elastic settles at **about $142/month**); (12) "there is no cheap-and-roomy
  provisioned option" is withdrawn (**20 MiB/s costs the same and buys 1.30-1.67x**); (13)
  headroom ratios are **1.04-1.33x** before rounding; (14) a CFN update **may**, not **will**,
  flip the mode back to bursting.
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
config and credentials (about 2.6 GiB), so the computed baseline is roughly 0.13 MiB/s - except
that **AWS guarantees a 1 MiB/s minimum metered throughput to every file system** (EFS User
Guide, "Bursting throughput", Note), so the real floor is 1 MiB/s. Confirmed in CloudWatch:
`BurstCreditBalance` was 896 MB at 09:30 JST on 09-16 and **0** at 10:30, while
`PermittedThroughput` fell from **104.858 MB/s (the 100 MiB/s burst) to 1.049 MB/s (exactly
1 MiB/s)** and stayed there until 11:45 JST. From that moment nine workspaces shared 1 MiB/s -
an `openat` of a 2.4 KB file took 5.3 seconds.

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

As a stop-gap the live file system was switched to `elastic` from the CLI (**09-16 11:56 JST** -
the minute `PermittedThroughput` jumped from 1.049 MB/s to 5,368.709 MB/s; it took about a
minute, and `/api/repos` went from 20 s to 272 ms). In parallel, the transcript search -
`filepath.Glob("projects/*/<sid>.jsonl")`, which read every project directory on every poll -
was fixed in PR #711 / 0.20.2, shipped as 0.21.0, and every user restarted.

### What was left once every box was on the new agent

**PR #711 did not end the storm.** How much it helped depends on which metric you use and what
you compare against: the answers spread over 13-46%, so **"it halved" is not supportable**.
Lining up the same clock window (09:00-13:20 JST) is the fairest comparison:

| Day | agent | per box ops/s p50 | per box ops/s p99 | per box MB/s p50 | file system max (1-min) |
|---|---|---|---|---|---|
| 09-11 | old | 175 | 476 | 0.717 | 10.100 MB/s (11:49, **5 boxes**) |
| 09-14 | old | 223 | 620 | 0.913 | 18.118 MB/s (11:46, **7 boxes**) |
| **09-17** | **new 0.21.0** | **153** | **345** | **0.627** | 5.860 MB/s (10:48, **4 boxes**) |

⚠️ The box count in the last column is **the count at that peak minute** (`ClientConnections`
summed over the minute, divided by two). The first draft put the window's *maximum* box count
there (6 / 8 / 6) - **the extrapolation below uses this as its denominator**, so mixing the two
shifts the estimated peak directly.

p50 is down 31% against 09-14 and 13% against 09-11; p99 is down 44% / 28%. Measured a
different way - **I/O per box-hour** - it is 5.20 GB (old, the 48.2 box-hours of 09-16's
elastic window) against **2.78 GB** (new, the 13.3 box-hours of 09-17), a 46% drop. ⚠️ **Per
box-hour folds in how busy the boxes were**, so the rate comparison (13-44%) and the volume
comparison (46%) answer different questions. Neither supports "half".

Boxes are counted from `ClientConnections` (Sum / period x 60). **One box = two connections**,
and the Control Plane does not use EFS at all, so it contributes nothing.

🔥 **The hour when exactly one box was running (09-17 09:06-10:05 JST) sat dead flat at
0.44-0.45 MB/s.** The request count does not have to be estimated - **`MetadataIOBytes`'
`SampleCount` is the number of metadata operations itself**. Measured over that hour:
**387,550 operations = 107.7 ops/s**. (For reference, AWS says "every NFS request is accounted
for as 4 kilobyte (KB) of throughput, or its actual request and response size, whichever is
larger", and `MetadataIOBytes / 4 KiB` agreed with `SampleCount` to 0.006%. But elastic's
*billing* minimums are a different thing - see below - so quote counts from `SampleCount`.)

The user reads this as "half a megabyte per second for one person is a lot". They are right.
There is no reason for a file system that serves config files to make 108 round trips a second.

⚠️ **Do not adopt "it will fall to 0.2 MB/s" as the target.** Across twelve days, 0.2 MB/s
only ever appears when zero or one box is running; during working hours the deployment has
been in a 0.7-1.9 MB/s per-box band continuously since 09-07.

## Measurement 1: what produces 110 requests per second

`ptrace_scope=1` refuses an attach to the running Agent, so instead **the agent's real code
was compiled into a test binary and started as a child**, counted with
`strace -c -e trace=file`. Project and session counts were set to production scale.

### Source A: `ListMetas()` - the session ledger lives on EFS

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

| Path | Files | What it is |
|---|---|---|
| `session-status/` | 1,220 | where session state is written (`status.Persist`) |
| `pending-perm/` | 1,429 | transient permission state |
| `chat-wd/` `chat-codex/` `chat-claude/` | 971 | chat working directories (93 MB) |
| `sessions/` | 207 | the session ledger |
| `session-injections/` `session-exit/` `codex-sid/` … | ~1,500 | assorted mutable state |
| **`secrets.enc` / `secrets.json` (+ `.lock`)** | 2 | 🔥 **the credential store itself** (`internal/secrets/secrets.go:383`'s `Path()` puts it under `paths.AgentConfigDir()`: Git, Claude OAuth and connection credentials) |

⚠️ **That last row is decisive.** The first draft of this ADR said "not one credential among
them" and was wrong: **`~/.config/agent-fleet` contains the credential store itself**. That runs
straight into the reason ADR 0045 created `keep`, so decision 4 cannot be "move the whole thing
down" (see decision 4). The rest is not credentials - **the keep list being directory-granular
(`.config`) is what put the agent's mutable state on EFS alongside them**, and that is the
entire story.

### Source B: `subagentBases()` - the other side of "a miss is never remembered"

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

### Source C: writes to EFS (small)

Under elastic, writes cost **$0.07/GB** against $0.04/GB for reads, so writing state files is
dearer per byte than reading. The volume is small, though: measured over 09-17 09:00-14:04 JST,
writes are **1.7%** of the total (0.80 GB written against 46.22 GB read). ⚠️ The first draft's
"0.8%" divided by the reads alone.

⚠️ The first draft described `status.Persist(sid, "working")` as "written from the polling path"
on every poll, and was wrong. That call at `claude.go:208` sits in the **self-heal branch only** -
reached when the status file says idle while the pane is visibly working - and, as its comment
states, it is **self-limiting to one capture per turn** (the next poll reads "working" from the
file). **Source C is not in the same order as A and B.**

### Putting it together (a hypothesis, not yet confirmed)

One box, one Console tab, 207 metas, 38 project directories, three idle claude sessions:

```
ListMetas()          208 file reads       x 1/4s
SubagentBusy() x3    117 directory reads  x 1/4s
                   → roughly 80 file/directory operations per second (VFS level)
```

The measured floor is 107.7 metadata ops/s, so **the order of magnitude matches**. ⚠️ **That is
not proof that A and B dominate the floor.** VFS operations and NFS round trips are different
things, and the ratio between them has not been measured. A 20-second delta of
`/proc/self/mountstats` taken on a production box (09-17 12:20 JST, collected over SSM by the
review session) reads:

| Mount | RPC/s | Breakdown (20 s) |
|---|---|---|
| `claude` (`CLAUDE_CONFIG_DIR`) | 27.5 | GETATTR 492, OPEN_NOATTR 29, CLOSE 29 |
| `keep` | 16.9 | OPEN_NOATTR 148, CLOSE 148, GETATTR 18, LOCK/LOCKU/FREE_STATEID 8 each |

No retransmissions. The OPEN/CLOSE-dominated `keep` profile is the shape of file reads like
`ListMetas()`, and LOCK/LOCKU/FREE_STATEID matches `secrets.go`'s flock.

⚠️ **Do not infer the caller from the RPC mix.** The first draft read "almost no READDIR on
`claude`" as "B (the full sweep) was not the dominant cost", and that overreaches - even when
directory contents come from cache, **the attribute revalidation that goes with a full sweep can
still produce GETATTR**. All this measurement supports is: READDIR RPCs were 0, GETATTR
dominated, and with no concurrent VFS count **the contribution is unknown**. **The ranking of A
against B is undetermined** - measure both together before acting on decision 5.

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
09-15, and then `APN1-ETDataAccess-Bytes` appears on 09-16 at **235.206 GB / $9.4655**, the day
the file system moved to elastic. Ending the outage converted what used to show up as
slowness into something that shows up as a bill - that is all that changed.

### ⚠️ `MeteredIOBytes` is not the billed quantity

The first draft said "the billing meter is `MeteredIOBytes`", and that was imprecise. AWS's
`metered-sizes.html` separates two things:

- **`MeteredIOBytes` is the throughput meter**, and it is the value **after reads are discounted
  to one third** ("Amazon EFS meters the throughput for read requests at one-third the rate of
  the other file system I/O operations … This combined throughput adjusted for consumption rates
  is reflected in the `MeteredIOBytes` CloudWatch metric"). It is what decides whether you hit
  the throughput ceiling.
- **Elastic billing** works through the read/write price difference, not that discount. Metadata
  reads bill as read operations and metadata writes as write operations, and **metadata
  operations are metered in 1 KiB increments after the first 4 KiB, data operations in 1 KiB
  increments after the first 32 KiB**.

So the cost is `(MetadataReadIOBytes + DataReadIOBytes) x $0.04 + (MetadataWriteIOBytes +
DataWriteIOBytes) x $0.07`.

### 🔥 Reconciled to 0.12% - billing is in GiB (2^30)

09-16's elastic window (**11:56 to 09:00 JST the next day**, 698 one-minute buckets summed):

| | |
|---|---|
| CloudWatch reads | 250,793,018,040 bytes |
| CloudWatch writes | 2,057,315,052 bytes |
| (reads + writes) / 2^30 | **235.485 GiB** |
| cost by the formula / 2^30 | **$9.4769** |
| **Cost Explorer actual** | **235.2056 / $9.4655** (`Estimated=true`) |

**+0.119% on quantity, +0.120% on cost.** With the same numerator (reads + writes), the same
window and minute resolution, CloudWatch and Cost Explorer **agree to 0.12%**. Read as
1 GB = 10^9 the total is 252.85 and the gap is +7.5%, so **AWS bills in GiB (2^30)**.

⚠️ **Both earlier drafts got this wrong.** The first said it "matched `MeteredIOBytes`"; the
second compared CloudWatch's **reads only** against CE's **reads + writes** and reported 5.7% /
1.5% - different numerators. And taking Period=3600 from 11:56 pulls the whole 11:00 bucket in,
including minutes from before the switch: **cut the window at minute resolution.**
⚠️ "Whether CloudWatch applies the per-operation minimums is not documented" was also wrong -
`efs-metrics.html` states for `TotalIOBytes` that "Data operations are metered at 32 KiB and
other operations are metered at 4 KiB. After the minimum, all operations are metered per KiB."

**Conclusion: Cost Explorer is the money, and CloudWatch converted at GiB estimates it well.**
That said, this is one window's reconciliation, not a guarantee for the next.

### What it costs per month (normalised per box-hour)

Daily totals move with how many people used the deployment for how long, so normalise to
**one box for one hour**:

**Derive both rows with the same formula and the same unit** (CloudWatch reads/writes times the
unit prices, divided by 2^30), and state the window.

| | window (JST, minute-cut) | box-hours | reads | writes | GiB/box-hour | $/box-hour |
|---|---|---|---|---|---|---|
| old agent | 09-16 11:56 - 09-17 09:00 | 48.6 | 233.57 GiB | 1.92 GiB | **4.845** | $0.1950 |
| **new agent** | **09-17 09:00 - 14:14** | **18.2** | **45.08 GiB** | **0.77 GiB** | **2.520** | **$0.1021** |

**48% less per box-hour.** Deriving the old row from CE's actual instead gives
$9.4655 / 48.6 = $0.1948 per box-hour - 0.1% from the $0.1950 above, as the reconciliation
predicts. ⚠️ The second draft had the old row from CE and the new one from CloudWatch, and used
10^9 as the unit.

Re-aggregating box-hours by **JST** day (09-04 to 09-16, 13 days, `ClientConnections.Sum / 120`)
gives a **weekday median of 58.7 box-hours** (range 34.2-66.6, n=9) and a **weekend median of
10.6** (range 6.9-13.6, n=4). 22 weekdays plus 8 weekend days is **about 1,377 box-hours**.
⚠️ The first draft's "weekday 54.6-66.6 / weekend 4.6-8.9" came from UTC-day buckets, which cut
each JST day across two.

- old agent: **about $268/month** ($0.1950 x 1,377)
- **new agent: about $141/month** ($0.1021 x 1,377)

⚠️ **What remains uncertain here is the sample, not the unit.** The unit is settled as GiB by
the previous section (0.12%). What is left is that the new agent's sample is **still only half a
working day**, and the 1,377 box-hours assumption. The first draft's "$100/month", the second's
"$155" and "$141-151" were all transitional; **the best current estimate is about $141/month**.
Check it against 09-17's Cost Explorer actual once it posts (Groups were still empty at
14:14 JST).

### The three-way comparison (monthly)

New agent (0.21.0), about 1,380 box-hours a month.

⚠️ **The amount of provisioned throughput needed is a scenario estimate, not a measurement.**
Take the old agent's 1-minute maximum of 18.118 MB/s at **7 boxes at that minute** (09-14
11:46), normalise per box, apply the improvement and scale to nine boxes:
`18.118 / 7 * 9 / 1.048576 * (1 - improvement)`. At 31% (the like-for-like p50) that is
**15.3 MiB/s**; at 46% (per box-hour) **12.0 MiB/s**. The tables below take **12-15 MiB/s**
(centre 13.5). ⚠️ The first draft used 8 as the denominator and arrived at 10-13 MiB/s with a
centre of 11. It applies a *median* improvement to a *peak*, and a 1-minute metric cannot see
a burst that lasts seconds. It is not a measurement.

The estimated peak is **12.00-15.33 MiB/s** (the extrapolation below). Headroom is computed
against the unrounded values.

| | (1) stay on elastic | (2) prov. 16 MiB/s | (3) prov. 20 MiB/s | (4) prov. 24 MiB/s | (5) elastic, after the fixes |
|---|---|---|---|---|---|
| I/O cost | **~$141** | $0 | $0 | $0 | **~$18-35** |
| fixed throughput cost | $0 | **$115.20** | **$144.00** | **$172.80** | $0 |
| storage | ~$1 | ~$1 | ~$1 | ~$1 | ~$1 |
| **total** | **~$142/month** | **~$116/month** | **~$145/month** | **~$174/month** | **~$19-36/month** |
| headroom over the estimated peak | no ceiling | **1.04-1.33x** | 1.30-1.67x | 1.57-2.00x | no ceiling |
| how it fails | the bill grows | **it throttles and every workspace stops** | same | same | the bill grows |

⚠️ **This is the table's third version.** First draft: $105/month, "provisioned is more
expensive". Second: $141-151/month, "16 MiB/s is 25% cheaper". Now this. **Break-even is about
19.6 MiB/s** ($141 / $7.20), so **20 MiB/s costs what elastic costs today and buys 1.30-1.67x
headroom.**
⚠️ The second draft's "there is no cheap-and-roomy provisioned option" judged from two points
(16 and 24) without defining how much headroom is needed. **Withdrawn** - what can be said is
that the 16 compared here has little headroom (4% over the upper demand estimate), the 24 costs
23% more than elastic, and the 20 sits in between at the same price.

Even so, "cheaper" is not the reason to stay on elastic. Three reasons are, and they are
decision 1.

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
- **`MeteredIOBytes` goes flat at the same height as `PermittedThroughput`.** The 09-16 credit
  exhaustion was exactly this: `PermittedThroughput` was pinned at **1.049 MB/s (the 1 MiB/s
  minimum)** from 10:00 to 11:45 JST. **A flat line is a ceiling, not a demand.** The real
  demand is unknowable until you release it - it jumped from 1.35 to 19.5 MiB/s within minutes
  of the switch to elastic.
- `BurstCreditBalance` is not published under elastic. It comes back under provisioned.

## Decisions

### Decision 1: the stop-gap is to stay on elastic. Do not buy provisioned throughput

⚠️ **On cost alone, provisioned at 16 MiB/s ($116/month) is cheaper than elastic
(about $142/month), and 20 MiB/s ($145/month) costs about the same while buying 1.30-1.67x
headroom.** The first draft said the opposite. Three reasons still favour staying put.

1. **We do not know, by measurement, how high to buy.** The ceiling would be set against an
   estimated peak (12-15 MiB/s, a scenario extrapolation) derived by applying a median
   improvement to a peak, from a 1-minute metric that cannot see a burst lasting seconds.
   **Buying a ceiling against an extrapolated peak is structurally what happened on 09-16.** And
   the cheaper option, 16 MiB/s, leaves only **1.04-1.33x** over that estimate (4% over the
   upper demand figure) - **the saving is bought out of the headroom**.
2. **The failure modes differ in kind.** Overrunning elastic produces a bill. Overrunning
   provisioned produces an **outage** - and an EFS at its ceiling is not "a bit slow". That is
   the measurement from 09-16: the moment it dropped to 1 MiB/s, an `openat` of a 2.4 KB file
   took 5.3 seconds.
3. **The permanent fixes only make elastic cheaper.** The 4-8x that decisions 4 and 5 aim at
   lands directly on the bill ($17-38/month); provisioned is a fixed cost and does not move a
   dollar. **Changing how throughput is bought removes exactly zero of the 108 requests per
   second.**

So this is not "pick the cheaper one" but "**do not buy a ceiling until the measurements are
in**". Revisit once decisions 4 and 5 have landed and been re-measured - by then demand will be
lower, so provisioned would be either much smaller or unnecessary.

If the user prefers the predictability of a fixed cost anyway, the recommended amount is
**24 MiB/s** ($172.80/month), on the reasoning that it is 1.57-2.00x the estimated peak of
12.00-15.33 MiB/s. ⚠️ The first draft added "and above the 22 MiB/s the old agent's demand scales to
at nine boxes, so it will not throttle even if 0.21.0's gain were lost"; **that guarantee is
withdrawn** - the peak is itself an extrapolation, so a multiple of it is only another one (and
with the denominator corrected, demand with the improvement entirely lost extrapolates to
22.2 MiB/s, which very nearly consumes 24). If it is not enough it can be raised the same day; lowering it waits until the
next. The CloudFormation default is set to the same 24 (decision 2).

### Decision 2: make the throughput mode a parameter in `10-data.yaml` (implemented)

`deploy/aws/ecs/cfn/10-data.yaml:72` hard-coded `ThroughputMode: bursting`. The live file
system was only changed from the CLI, and the `af-ecs-data` stack has not been updated since
2026-08-25. **The next update of that stack could flip it back to bursting and re-arm the outage,
depending on what the update changes.** ⚠️ Not "would": an ordinary change set compares the old
and new **templates**, so drift introduced by a CLI-only change to the live file system does not
show up there (comparing against actual state is what drift-aware change sets do). That is
exactly what makes it nasty: **whether it flips depends on the update, and if it flips, nothing
goes wrong until the credits drain.** ⚠️ The first draft added "with a 24-hour wait to undo it"; the asymmetric restriction above
binds only **after a switch to provisioned or a change of its amount**, so
elastic → bursting → elastic is not covered by it. The danger is not the wait but **the mode
changing silently at all** - nothing goes wrong until the credits drain, so you find out at
exhaustion.

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

`~/.config/agent-fleet` sits on `keep` (EFS) only because `AF_WS_KEEP_DIRS` is granular to a
directory named `.config`. The comment in `meta.go` that believes it is on the home volume is a
record of the design intent: that is where it belongs.

🔴 **But it cannot be moved wholesale.** The same directory holds `secrets.enc` /
`secrets.json` (`internal/secrets/secrets.go:383`), which is the credential store for Git,
Claude OAuth and connections. Naively repointing `AgentConfigDir()` **takes the credentials down
to EBS with everything else** - exactly what ADR 0045 decision 3-6 forbids. So **whichever
option is taken, separating the credential store out and leaving it on `keep` is a
precondition.**

There are two ways down, and this ADR prefers **(b)**, subject to that precondition.

- **(a) Narrow the keep list**: replace `.config` with `.config/opencode`, `.config/cursor`,
  `.config/acli`, … - only the entries that hold credentials. The list has to grow every time a
  CLI is added, and forgetting one fails quietly as **logins that vanish on every restart**.
  ⚠️ The first draft called this "one line in the entrypoint" and was wrong: existing boxes
  already have a `~/.config` → `keep` symlink, so it **also requires tearing that down and
  sorting the contents** (credentials stay on keep, the rest returns to home).
- **(b) Move the agent's state out of `.config`**: make `~/.local/state/agent-fleet` (i.e.
  home / EBS) the state directory and do a one-way migration from `.config/agent-fleet` once,
  in the entrypoint or at Agent start. No list to maintain, and the line "credentials on keep,
  mutable state on home" survives as a directory name. The env seam already exists
  (`AF_SESSIONS_DIR`). **Exclude `secrets.*` from the migration explicitly and keep
  `secrets.Path()` pointing at `keep`.**

⚠️ **The destination needs today's protection too.** `~/.config/agent-fleet` is covered by the
Console file browser's denylist (`fs.go:123`). If the new location is not on that list, the
mutable state (session ledger, chat working directories, pending permission state) **becomes
browsable**. Add the denylist entry in the same commit as the move.

⚠️ **The trade-off**: home on ecs-ec2 is **a single EBS volume in a single AZ** - which is the
very reason ADR 0045 created `keep`. Anything moved down is lost with that volume: losing the
session ledger empties the session list (the transcripts themselves live under
`CLAUDE_CONFIG_DIR` and survive). The judgement here is that this is a lighter loss than
losing credentials. **Credentials (`secrets.*`, `~/.ssh`, `.git-credentials`, `.claude.json`)
stay on EFS** - ADR 0045's line about not leaving plaintext on local disk does not move.

#### Implementation (option (b))

`paths.AgentStateDir()` = `~/.local/state/agent-fleet` was added; `AgentConfigDir()` still
resolves to `~/.config/agent-fleet`. Which root a store belongs in is decided in this order:

1. **A credential, or anything that can carry one**, stays in `AgentConfigDir`. Besides
   `secrets.*` that is `mcp-tenant.json`: 🔥 a tenant-distributed definition that is not
   `user_secret` **arrives with its header and env VALUES filled in** (the reason is written on
   `UserSecret`, `secrets.go:325`), so treating it as "ids only" would take credentials down to
   EBS. `chat-mcp/` (the per-conversation `--mcp-config`) stays for the same reason - and at
   3 files / 16 KB there is no I/O to win by moving it.
2. **User-supplied configuration and user-authored content** stays in `AgentConfigDir`
   (`rtk.json`, `ui-prefs.json`, `toolchains.json`, `user-notes*`, `assistants/`, `knowledge/`,
   `locks.json`, `mcp-optout.json`, `chats/`). `mcp-managed.json` stays too: it is the only
   authority for deleting a row af wrote into another CLI's config, and those files
   (`~/.claude.json`, `~/.codex`, `~/.config/opencode`) are on keep as well. Split across the
   two volumes, losing one leaves **orphans nothing can remove**.
3. **Everything else moves.** Anything keyed by session name or sid belongs on that side: the
   ledger itself moved, so state outliving it is meaningless.

The migration runs once at Agent start (`internal/statemig`, immediately after main's
subcommand branches and before any other read). **Which side wins when it is interrupted** is
settled by three rules:

- **The destination is the truth.** Nothing already at the destination is ever overwritten.
  Only the new build writes there, so what is there is newer by construction - including a
  file a hook subprocess wrote while the migration was running.
- **Per file, not per directory.** Per directory, a half-copied directory is indistinguishable
  from a finished one and the next boot skips the rest of it forever.
- **The source file is removed once copied, and a finished entry is recorded in
  `.migrated-from-config.json` at the destination.** Both exist to stop something the user
  deleted *after* the migration from coming back: without them, deleting a session drops its
  meta and the next boot restores it from the leftover under `.config`.

Leftovers under `.config/agent-fleet` are therefore expected, and the next boot converges.

🔴 **A symlink is copied as a symlink.** The chat working directories **borrow the real
credentials through links** - measured, three of them: `chat-claude/.credentials.json` → the
claude config mount, `chat-codex/auth.json` → `~/.codex/auth.json`, and agy's OAuth token under
`chat-wd/agy-*/home/.gemini/…`. Following them writes **three plaintext copies onto home**,
which is precisely what ADR 0045 decision 3-6 forbids, and leaves `reconcileChatCreds` folding
a rotated token into a file nothing else reads.

The destination was added to the file browser's denylist (`fs.go`) in the same commit.
`statemig.Entries` is an allowlist, with a test (`statemig_drift_test.go`) that fails when a
store resolves through `paths.AgentStateDir` and is not listed. The inverse was rejected
because the two kinds of omission are not equivalent: forgetting an allowlist entry leaves one
store on EFS, forgetting a denylist entry takes a credential or a user's configuration down to
EBS. `deploy/` (written by `deploy/aws/ecs/env.sh`) is the clearest case - moving it breaks the
deployment scripts.

Measured (`internal/session/meta_probe_test.go`, strace, 207 metas × 100 calls): the **836
file syscalls per `ListMetas()` do not change**. What changed is that **all of them land on
home**, and **zero** land under `.config/agent-fleet` (openat 21,018 / read 41,404 / close
21,017 / getdents64 213, every one of them under `.local/state/agent-fleet/sessions/`).

⚠️ **`chats/` (the assistant conversations themselves) was not moved.** `ListConvs()` has the
same shape as `ListMetas()` (one `ReadDir` plus one `ReadFile` each) and what it reads is the
full conversation. But its polling frequency has not been measured, and this is content the
user wrote: accepting "lost with the EBS volume" for conversations is a separate judgement.
Next candidate.

#### Four things review corrected (how the migration breaks)

- 🔴 **It deleted things it had not copied.** `copyEntry` used one return value for both "the
  destination already has it" and "not a regular file or a symlink (socket / fifo / device)",
  so the source was removed in the second case too (reviewer measured it with a fifo). **A live
  socket is a running process's listener**, so that is now a third outcome - leave it, delete
  nothing - and an entry with anything left behind is **not recorded as finished** (`RemoveAll`
  cannot tell the difference).
- 🔴 **A credential that should be a symlink is sometimes a real file.** `reconcileChatCreds`
  exists precisely because the CLIs **replace the link with a real file** on a token refresh,
  and migrating one in that state writes the plaintext token onto home (measured).
  `auth.json`, `.credentials.json` and agy's OAuth token are **left in place when they are
  regular files**; the next chat turn's reconcile restores the link.
- 🔴 **The `af-db` subcommand can outrun the migration.** Its branch in `main.go` returns before
  `statemig.Run()`, and it is the one subcommand **a user runs by hand**. It reads a missing
  registry as an empty one and writes that back, leaving a thin `{"instances":{}}` at the
  destination that "the destination is the truth" then keeps - **orphaning a running postmaster**
  (measured). That branch now runs the migration itself. The hook path (a tmux session that
  outlived an Agent restart) is **deliberately not fixed**: it would put a 100 MB copy in front
  of a claude turn, and the symptom is one mis-filed event that the next hook self-heals.
- **af-db's `<engine>-<major>.pass` is a plaintext password.** It meets rule (1) head-on but
  **stays on the state side as an exception**: it authenticates nothing outside this Workspace.
  It is generated here, reaches only a loopback server whose datadir is on the same volume, and
  **losing that volume loses the thing it opens**. A copy on keep would outlive what it unlocks.
  `passPath` now says so - without that sentence the next reader applies rule (1) and moves it
  back.

⚠️ **The migration blocks boot.** Nothing is served until 5,400 files / 113 MB have been moved.
Review's concern that a rollout would have every workspace do this at once and exhaust burst
credits **does not apply to the current configuration**: decision 1 keeps it on `elastic`, which
has no burst credits. What remains is a longer first boot - and **a readiness failure never
fails Start** (`runtime_ecs.go`: "A readiness failure must still NEVER fail Start", structurally,
nothing reads it), so it cannot turn into a task-replacement loop. One log line is emitted when
there is something to move, so a slow boot is diagnosable rather than silent.

⚠️ claude's hook definitions do not break (verified). What `hooks.go` embeds in a command line
is **the agent binary's path only** (`<exe> session-status <state>`); where the state lives is
resolved by the agent at run time, so a hook written before the migration writes to the new
location unchanged.

### Decision 5 (permanent, P1): stop the remaining two `projects/*` sweeps

- **Remember the `subagentBases()` miss, but only on the status path.** The "never remember a
  miss" invariant on the transcript side exists to protect the `SessionJSONLExists` →
  `--resume` / `--session-id` branch.

  🔴 **The subagents side carries a danger too.** The first draft said a false "no background
  agent" only lights the badge a few seconds late, and that was wrong. `SubagentSnapshot` /
  `SubagentReceivedSince` (`bg.go:377,395`) go through the same `subagentBases()`, and
  `internal/sessionx/session_delivery.go:121` uses them for the **misdelivery check**: twelve
  seconds after typing, it asks whether the prompt landed in a background agent's transcript,
  and if it did it **refuses to resend** (resending would fire the same interruption into the
  agent a second time). A 30-second negative cache on the shared `subagentBases()` would
  **hide a `subagents/` directory created just after typing, letting that safety check through**.

  🔴 **"display versus delivery" is not a sufficient split either** (found on the second review
  pass). `SubagentBusy` itself is also called from `collectReportSignals` at
  `internal/chatx/chat_report_reconcile.go:388`, where `busyEvidence()` (:186) pushes
  `subagent-busy` as evidence and **suppresses the completion verdict**. And
  `chat_stop_after_turn.go:81` runs the same `collectReportSignals` → `evalReportEvidence`, and
  proceeds to `stopArmedSession` once quiet holds. So a negative cache that hides busy can
  **report completion early** or **stop a session while a background agent is still running** -
  precisely the case of a new Workflow agent that the main transcript's
  `BackgroundAgentsRunning` does not catch, where child-transcript freshness is the only
  evidence there is.

  So draw the line at "**display only versus every safety decision**". There are three safety
  decisions - (1) the delivery check (`session_delivery.go:121`), (2) the completion report
  (`chat_report_reconcile.go`) and (3) firing an armed stop (`chat_stop_after_turn.go`) - and
  **all three must search for real**. Two ways to build it: (a) **add a display-only entry
  point** and put the negative cache there alone, leaving `SubagentBusy`'s existing meaning
  untouched; (b) give every safety path an **explicit bypass**. Prefer (a), because it makes the
  safe behaviour the default. There are **two display-side callers** to switch over under (a):
  `claude.go:213` (the session list) and `internal/sessionx/session_transcript.go:289` (the chat
  header). ⚠️ "Leaving the existing meaning untouched" does **not** mean dropping the existing
  positive `pathMemo` (a hit is revalidated with `Lstat`); the one thing that must not change is
  that **absence is never decided from a negative cache**.

  Acceptance test: **create a child transcript just after a miss, inside the TTL**, and confirm
  that with the display cache warm, (1) a misdelivery is still caught, (2) completion is not
  reported early, and (3) an armed stop does not fire. ⚠️ Do not apply the same change to the
  transcript memo - the type comment in `jsonl_memo.go` explains why the guard is written twice
  there.
- **Derive the project directory from the cwd.** claude encodes the cwd into the directory
  name (`/home/dev/repos/agent-fleet` → `-home-dev-repos-agent-fleet`), and the Agent already
  holds the session's `Meta.Dir`, so `projects/<derived>/<sid>.jsonl` is **one Lstat**, with
  the existing sweep as the fallback when it misses. The encoding is lossy (`.` and `@` also
  become `-`), so **the derivation is a guess, not the truth** - always keep the fallback.
- The transcript side's `memoTTL = 60s` returns a hit to the full sweep once a minute. Once
  the derivation is in, re-searching is cheap, so revisit that constant afterwards.

#### Implementation (option (a) was tried, rejected, and replaced by one with no cache)

🔥 **The first implementation was (a) — a display-only entry point with a 15s negative cache —
and review rejected it.** Drawing the line at "display versus every decision" was right; what
was wrong was the census of what counts as display. **One of the two supposed badges is not a
badge**: the `LiveInfo.BackgroundBusy` that `WireLive` fills travels the wire as the session's
`backgroundBusy`, and **the CP's reaper decides on it**:

```
claude.go WireLive → agents.LiveInfo.BackgroundBusy → sessionx/session.go sessionWire
  → CP control-plane/session_activity.go sessionActivity()
     → holdsWorkspace()  … tier 2: whether to stop the WORKSPACE
     → tier1Reapable()   … tier 1: whether to halt the session
```

The comment on that very line records the incident that put it there: **the reaper did not look
at this and stopped running background work**. So the cached implementation was reopening, one
layer up, the hole this decision closes inside the Agent — a 15s-stale "no background work" can
stop the whole box, and tier 2 has no debounce, so one sweep is enough.

**The implementation taken drops "avoid looking" for "look in exactly one place".** claude puts
a session's subagents directory **beside the session's own transcript**, in the project
directory derived from the cwd. `jsonlPaths` has already located that transcript and memoized
it, so `subagentBases` needs **one `Lstat`** next to it — and produces its answer, "there is
none" included, **from the disk every time**. The negative cache is gone.

- The anchor rule in `subagentBases` **was corrected once more in the second review pass**.
  🔥 **Absence of Y is being concluded from the presence of X, so an incomplete anchor produces
  a false negative.** Two were found, both measured:
  - **One transcript is not enough.** `Meta.CWD()` returns `Dir/Subdir` only while that
    directory EXISTS and falls back to `Dir` when it does not (a branch switch removing the
    folder). The same sid can therefore hold state under two project names while `jsonlPaths`
    answers with whichever today's cwd resolves to — and the background agent running beside
    the other one is invisible. 🔴 **Worse than the negative cache it replaced**, which healed
    itself in 15s; this did not heal at all. `session.CWDCandidatesForUUID` returns every cwd
    the session can have, and all of them are checked.
  - **The cwd alone must not be an anchor either.** A cwd says where the session was launched,
    not what claude wrote where. **With no transcript located, nothing is concluded and the
    original sweep runs** — dropping that rule turns the existing safety test
    `TestSessionReportDeferredWhileSubagentBusy` (hold the completion report while background
    agents run) red, which was confirmed by making it red.
  - The remaining assumption cannot be checked from here: claude runs at a cwd derived from the
    session's own Meta. AF sets it at launch (`BuildLaunch` passes `m.CWD()`), so the
    enumeration is exhaustive — but **it is written down as an assumption**.
- `SubagentBusyDisplay` / `BackgroundWorkDisplay` / `absenceMemo` **do not exist**. There is one
  entry point, `BackgroundWork`, and the three decisions and the badge read the same fresh
  answer.
- **Deriving the project directory from the cwd** (`project_dir.go`) stays, on the transcript
  side. The encoding is "every non-alphanumeric becomes `-`", verified against a live tree. It
  is **lossy and not injective**, so the derivation is a guess, taken only when one `Lstat` of
  `<sid>.jsonl` confirms it — a sid is unique, so finding it there settles that it IS that
  session's transcript. A miss falls through to the old sweep. A sid cannot be turned back into
  a cwd (UUIDv5(dir|name)), so `session.CWDForUUID` records it whenever a meta is read or
  written, which costs no extra I/O. With a `Meta.Subdir` it returns `CWD()`.
- **`memoTTL = 60s` stays** (the review's outcome): the re-search it forces measures 2 syscalls.

Measured (`internal/agents/claude/bg_probe_test.go`, strace, 39 project directories, per-call
cost as the **slope** between 10 and 110 calls so startup and the fixture are out of the
denominator):

| path | before | cached version (rejected) | shipped |
|---|---|---|---|
| subagent lookup (no background agents) | 201 | display 0 / safety 201 | **3** (every caller) |
| transcript re-search, cwd known | 201 | 2 | **2** |
| transcript re-search, cwd unknown | 201 | 201 | 201 |

⚠️ **The shipped version is both faster than the rejected one and never stale.** The original
pass criterion — "the safety side must stay at 201" — was only needed *if* the implementation
split display from safety. With no split there is nothing to hold at 201, and what has to be
proved instead is that **no absence is remembered**:
`TestAnAgentStartingIsVisibleImmediately` (create the child transcript right after a miss; the
very next call must see it), with a positive control that breaking `pathMemo`'s "never remember
a miss" invariant turns it red.

`jsonl_memo.go`'s "A MISS IS NEVER REMEMBERED" is now **one invariant covering both the
transcript and the subagents lookup**, since the exception it would have had is gone.

### Decision 6 (permanent, P2): on mount options, establish first what is *not* possible

ECS's `EFSVolumeConfiguration` carries only `FileSystemId`, `RootDirectory`,
`TransitEncryption`, `TransitEncryptionPort` and `AuthorizationConfig` - 🔥 **there is no field
for NFS mount options** (verified against the aws-sdk-go-v2 type). So `actimeo`,
`lookupcache`, `nconnect` and `noatime` **cannot be set from a task definition**.

There is exactly one way to set them: **mount the file system yourself on the EC2 instance and
pass it in as a host-path volume**. There is precedent - on ecs-ec2, `home` is already a host
bind of `/af-home/<membershipID>/dev`.

⚠️ The first draft claimed this would mean rebuilding the access point's isolation and uid
mapping by hand. **That is wrong.** The efs-utils mount helper takes an access point directly:
`mount -t efs -o tls,accesspoint=<fsap-id> <fs-id> <mountpoint>` (EFS UG, "Mounting with EFS
access points"). **Isolation and uid mapping are preserved; only the mount options are added.**
The reasons to defer are not about isolation:

- the effect is **unmeasured**. Decisions 4 and 5 remove round trips outright; this one changes
  the weight of a round trip and how well the cache holds, so it comes later;
- transit encryption goes through stunnel, so `nconnect` cannot be combined with it (`actimeo`,
  `lookupcache` and `noatime` can);
- ownership of the mount moves from the CFN task definition into EC2 user-data. When that breaks
  it breaks as **the box not starting**, so it needs test coverage on the `40-ec2-pool.yaml`
  side.

**Conclusion: judge P2 after decisions 4 and 5 have landed and been re-measured.** Whether
shaving the cost of a round trip is still worth anything is only knowable once the number of
round trips has come down.

### Decision 7: what was rejected

- **Buying provisioned throughput** (decision 1 - **it is cheaper**, but we do not know by
  measurement how high the ceiling has to be, so we do not buy one).
- **Going back to bursting**: the floor once credits are gone is 1 MiB/s (AWS's minimum), and
  demand during working hours is 5-18x that. Getting the baseline up to demand would mean
  padding Standard storage to nearly 1 TiB, at $368/month. More expensive than provisioned.
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

0. **First, settle whether A or B dominates** (before decision 5 is implemented). On a
   production box, take a `/proc/self/mountstats` delta (READDIR / GETATTR / OPEN / READ RPC
   counts for `claude` and `keep` separately) **together with** the VFS operation count for the
   same interval. ⚠️ **The RPC mix alone cannot identify the caller** (see the note in the
   body): READDIR RPCs being 0 on the `claude` mount in the 20-second delta on 09-17 at
   12:20 JST does not prove B contributes little - the entries can come from cache while the
   attribute revalidation still shows up as GETATTR. **The contribution is currently unknown**,
   and fixing without knowing the ranking means not knowing what is left when it does not help.
1. **Unit (`strace`).** Run the same shape of probe as this ADR - real code compiled into a
   test binary, started as a child, counted with `strace -c`. ⚠️ **Measure the display path and
   the safety paths separately** (decision 5): (1) the **display** path, with the cache warm,
   should drop from 158 syscalls per call to single digits; (2) the **safety** paths (delivery
   check, completion report, stop firing) still search for real, so **158 is the correct number
   there** - demanding single digits would pass an implementation that deleted the safety check.
   Confirm that `ListMetas()`'s 836 becomes **zero on EFS** (because it moved to home).
   ⚠️ **`-e trace=file` is not enough**: `read` and `close` take no filename and so fall outside
   that set, losing 624 of `ListMetas()`'s 836. The set actually used was
   `-e trace=getdents64,openat,newfstatat,read,close`.
2. **The one-box floor.** Arrange an hour with exactly one user running and watch
   `MetadataIOBytes`' **`SampleCount`** (the operation count itself, no estimation).
   **107.7 ops/s is the starting point.** ⚠️ "it should land in the low single digits" is an
   **unmeasured expectation, not a pass/fail criterion** - while the ranking of A against B is
   unknown (step 0), how far it falls cannot be predicted. What to look at is whether it fell
   significantly, and where to look next if it did not: `~/.gitconfig` lookups (how often `git`
   is started) and claude's own transcript writes.
3. **Working hours.** Count boxes from `ClientConnections` (one box = two connections) and
   compare per-box ops/s **within the same clock window** (09:00-13:20 JST: old 175-223, new
   153). ⚠️ Do not compare across different times of day - that is how the first draft's "half"
   was produced.
4. **The bill.** Watch `APN1-ETDataAccess-Bytes` in `ce get-cost-and-usage` over three working
   days. ⚠️ CE rows stay `Estimated=true` for a while. When reconciling against CloudWatch, line
   up **the same numerator (reads + writes), the same window, minute resolution and GiB** - done
   that way, 09-16's window agreed to 0.12% (mismatched, it is 6-8% out). That is this window's
   residual, not a guarantee for the next. Anything finer than DAILY needs the payer account to
   have opted in (measured: HOURLY returns `AccessDeniedException`).
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
- 🔴 **Whether source A or source B dominates the floor is still undecided** (the biggest gap).
  `/proc/self/mountstats` was captured once on a production box (the table above), but **the RPC
  mix does not identify the caller**, so all those 20 seconds support is "READDIR RPCs were 0,
  GETATTR dominated, **contribution unknown**". The ranking is not settled until **VFS operations
  and RPCs are captured together, during an interval where the floor is visible**. That is why
  decision 4 is sequenced ahead of decision 5.
- **Decision 5's anchor is complete over DIRECTORIES, not over SESSION IDS.** `jsonlPaths`
  follows the drifted id (`LiveSID`), while `subagentBases` builds `<dir>/<sid>/subagents` from
  the SLOT sid. When claude restarts itself onto an id of its own, the transcript is found, the
  directory beside it is empty, and the answer is a confident "none". ⚠️ **The sweep it replaced
  answered "none" on the same tree**, so this is a standing gap rather than a regression.
  Closing it means using `LiveSID(sid)` — a no-op on the non-drifted path, since
  `LiveSID(sid) == sid` there — but **nobody has looked at a real drifted session to see which
  id the subagents directory lands under**, so it must be measured before it is changed. (Found
  in the second review pass; the same note is in `bg.go`.)
- **What a token left behind by the migration costs.** A credential that was rotated while
  borrowed stays on the old volume, and the next chat turn only re-links the new path, so the
  rotation is never folded back. With a provider that retires used refresh tokens, **the user
  is asked to sign in again**. The boot log now names the file; teaching `reconcileChatCreds`
  to look at the legacy path once was rejected as permanent legacy knowledge in chatx for a
  one-boot window.
- **How much the NFS attribute cache absorbs.** The syscall counts above are VFS-level, not
  NFS round trips. Directory contents are answered locally until `acdirmin` (default 30 s) and
  file attributes until `acregmin` (default 3 s). The first draft estimated "one to three round
  trips per operation"; because the concurrent VFS operation count was never captured, **that
  ratio remains unverified**.
- **What migration does to existing boxes under decision 4 (b).** If the Agent dies mid-way
  through the one-way migration, something has to decide which side wins.

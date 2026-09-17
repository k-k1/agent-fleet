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
  mixed two bases and the unit convention matters (elastic is a provisional **$141-151/month**);
  (10) "no READDIR, so B is not dominant" inferred the caller from the RPC mix.
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
DataWriteIOBytes) x $0.07`. For 09-16's elastic window (from 11:56 JST):

| | |
|---|---|
| CloudWatch reads (metadata 247.43 + data 1.27) | **248.70 GB** |
| CloudWatch writes | 2.05 GB |
| cost by the formula above | $10.09 |
| **Cost Explorer actual** | **235.206 GB / $9.4655** |

**The gap depends on the unit convention.** At 1 GB = 10^9 bytes, CloudWatch's 248.70 against
CE's 235.206 is **5.7%**; at 1 GiB = 2^30 bytes CloudWatch is 231.6 GiB and the gap shrinks to
**1.5%** - **reading CE as GiB is the more consistent interpretation**. The residual has not
been decomposed either: (1) the CE rows are `Estimated=true`; (2) the mode switch fell mid-day
(09-16 11:56 JST). ⚠️ The first draft also said "whether CloudWatch applies the per-operation
minimums is not documented", and that was wrong - `efs-metrics.html` states for `TotalIOBytes`
that "Data operations are metered at 32 KiB and other operations are metered at 4 KiB. After
the minimum, all operations are metered per KiB." **Conclusion: take Cost Explorer as the money
and CloudWatch for shape and trend. This window reconciled to 1.5% read as GiB, but that is the
result of one comparison, not a guarantee for the next.**

### What it costs per month (normalised per box-hour)

Daily totals move with how many people used the deployment for how long, so normalise to
**one box for one hour**:

**Derive both rows with the same formula** (CloudWatch reads/writes times the unit prices),
and state the window.

| | window (JST) | box-hours | reads | writes | GB/box-hour | $/box-hour |
|---|---|---|---|---|---|---|
| old agent | 09-16 11:56 - 09-17 09:00 | 48.6 | 250.79 GB | 2.06 GB | **5.20** | $0.209 |
| **new agent** | **09-17 09:00 - 14:04** | **17.5** | **46.22 GB** | **0.80 GB** | **2.69** | **$0.109** |

⚠️ The first draft derived the old row from **Cost Explorer's actual / box-hours** ($0.196)
while the new row came from CloudWatch ($0.109) - two bases in one column. Do not mix them.
For reference, the old row from CE is $9.4655 / 48.6 = $0.195 per box-hour, and the gap to the
CloudWatch formula is the same 6% (1.5% if GiB) discussed above.

Re-aggregating box-hours by **JST** day (09-04 to 09-16, 13 days, `ClientConnections.Sum / 120`)
gives a **weekday median of 58.7 box-hours** (range 34.2-66.6, n=9) and a **weekend median of
10.6** (range 6.9-13.6, n=4). 22 weekdays plus 8 weekend days is **about 1,377 box-hours**.
⚠️ The first draft's "weekday 54.6-66.6 / weekend 4.6-8.9" came from UTC-day buckets, which cut
each JST day across two.

- old agent: about 7,160 GB = **about $290/month**
- **new agent: about 3,700 GB = about $150/month** ($0.109 x 1,377)

⚠️ **The unit convention moves this by 7%.** The above converts at 1 GB = 10^9 bytes. If AWS
bills in GiB (2^30), it is **about $140/month**. **The Cost Explorer measurement actually
favours GiB**: over 09-16's elastic window, CloudWatch's 248.70x10^9 read bytes are 231.6 GiB
against CE's billed quantity of 235.206 - a 1.5% gap read as GiB, 5.7% read as GB.
**Conclusion: a provisional estimate of about $140-150/month**, not a settled figure. Both the
first draft's "about $100/month" and its replacement "the figure is $155" overstated the
certainty. Check against 09-17's Cost Explorer actual (its Groups are still empty).

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

| | (1) stay on elastic | (2) provisioned 16 MiB/s | (3) provisioned 24 MiB/s | (4) elastic, after the fixes |
|---|---|---|---|---|
| I/O volume | ~3,700 GB/month | - (not billed) | - | ~400-900 GB/month (target: 4-8x less) |
| I/O cost | **~$140-150** | $0 | $0 | **~$15-37** |
| fixed throughput cost | $0 | **$115.20** | **$172.80** | $0 |
| storage | ~$1 | ~$1 | ~$1 | ~$1 |
| **total** | **~$141-151/month** | **~$116/month** | **~$174/month** | **~$16-38/month** |
| headroom over the estimated peak (12-15 MiB/s) | no ceiling | **1.07-1.33x** | 1.6-2.0x | no ceiling |
| how it fails | the bill grows | **it throttles and every workspace stops** | same | the bill grows |

⚠️ **This table has now changed twice.** The first draft estimated elastic at $105/month and
concluded provisioned was more expensive. Measured per box-hour, elastic is **$141-151/month**
(the range is the unit convention) and **provisioned at 16 MiB/s ($116/month) is cheaper** -
**but once the peak's denominator is corrected, that 16 MiB/s leaves only 1.07-1.33x headroom
over the estimated peak of 12-15 MiB/s.** The real choice is between "cheap with no headroom"
(16) and "headroom at 15-23% more than elastic" (24); **there is no cheap-and-roomy provisioned
option.** Break-even is around 20 MiB/s, uncomfortably close to the top of the estimated peak
band (15.3 MiB/s).

So "cheaper" is not the reason to stay on elastic. Three reasons are, and they are decision 1.

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
($141-151/month).** The first draft said the opposite. Three reasons still favour staying put.

1. **We do not know, by measurement, how high to buy.** The ceiling would be set against an
   estimated peak (12-15 MiB/s, a scenario extrapolation) derived by applying a median
   improvement to a peak, from a 1-minute metric that cannot see a burst lasting seconds.
   **Buying a ceiling against an extrapolated peak is structurally what happened on 09-16.** And
   the cheaper option, 16 MiB/s, leaves only 1.07-1.33x over that estimate - **the saving is
   bought out of the headroom**.
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
**24 MiB/s** ($172.80/month), on the reasoning that it is 1.6-2.0x the estimated peak of
12-15 MiB/s. ⚠️ The first draft added "and above the 22 MiB/s the old agent's demand scales to
at nine boxes, so it will not throttle even if 0.21.0's gain were lost"; **that guarantee is
withdrawn** - the peak is itself an extrapolation, so a multiple of it is only another one (and
with the denominator corrected, demand with the improvement entirely lost extrapolates to
22.2 MiB/s, which very nearly consumes 24). If it is not enough it can be raised the same day; lowering it waits until the
next. The CloudFormation default is set to the same 24 (decision 2).

### Decision 2: make the throughput mode a parameter in `10-data.yaml` (implemented)

`deploy/aws/ecs/cfn/10-data.yaml:72` hard-coded `ThroughputMode: bursting`. The live file
system was only changed from the CLI, and the `af-ecs-data` stack has not been updated since
2026-08-25. **The next update of that stack would flip it back to bursting and re-arm the outage.**
⚠️ The first draft added "with a 24-hour wait to undo it"; the asymmetric restriction above
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
  (always a real search) untouched; (b) give every safety path an **explicit bypass**. Prefer
  (a), because it makes the safe behaviour the default.

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
   same interval. The 20-second delta on 09-17 at 12:20 JST showed almost no READDIR on the
   `claude` mount, so **B (the full sweep) is not always the dominant cost**. Fixing without
   knowing the ranking means not knowing what is left when it does not help.
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
   days. ⚠️ CE rows stay `Estimated=true` for a while, and reconcile with CloudWatch only within
   6%. Anything finer than DAILY needs the payer account to have opted in (measured: HOURLY
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
- 🔴 **Whether source A or source B dominates the floor is still undecided** (the biggest gap).
  `/proc/self/mountstats` was captured once on a production box (the table above), and the
  `claude` mount was GETATTR-dominated with **almost no READDIR** - a short 20-second window
  under different conditions from the morning floor, but not what "B dominates" would predict.
  The ranking is not settled until **VFS operations and RPCs are captured together, during an
  interval where the floor is visible**. That is why decision 4 is sequenced ahead of decision 5.
- **How much the NFS attribute cache absorbs.** The syscall counts above are VFS-level, not
  NFS round trips. Directory contents are answered locally until `acdirmin` (default 30 s) and
  file attributes until `acregmin` (default 3 s). The first draft estimated "one to three round
  trips per operation"; because the concurrent VFS operation count was never captured, **that
  ratio remains unverified**.
- **What migration does to existing boxes under decision 4 (b).** If the Agent dies mid-way
  through the one-way migration, something has to decide which side wins.

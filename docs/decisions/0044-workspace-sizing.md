# 0044. Workspace size is held as three numeric axes with named sizes living in the UI layer; `~` is split by "number of files"

English | [日本語](0044-workspace-sizing.ja.md)

- Status: adopted, in implementation (2026-08-15. The design and the measurements are in docs/63.)
  Decided after measuring **all 74 valid Fargate task sizes** and **EFS I/O** in an AWS sandbox. The
  measurements broke two parts of the original proposal (Elastic throughput does not help small
  files; moving only the caches to local storage does not help).
- See also: [63-workspace-sizing.md](../log/63-workspace-sizing.md) /
  [62-ecs-start-latency.md](../log/62-ecs-start-latency.md) (the start-latency side of the same ECS) /
  [history/p3-7-aws-adapter.md](../log/p3-7-aws-adapter.md) §20b.7.4 (the frozen specification that chose EFS) /
  [0012-go-internal-refactor.md](0012-go-internal-refactor.md) (the adapter holds no state in the CP)

## Context

We want per-user task size and disk for workspaces on the ECS runtime. A per-user memory cap already
runs through `user_limit.mem_limit` → `fargateSize()` → the task definition, but (1) CPU cannot be
chosen independently, (2) `disk_gb` is only used for display on ECS, and (3) the default 1024/2048
(2 GiB) is small for real use.

Two things were measured before deciding (docs/63 §63.2 / §63.4).

- **Fargate's valid sizes are discrete, and the steps are not uniform.** The 8 vCPU band steps by
  4096 MiB and the 16 vCPU band by 8192 MiB. The existing `fargateTiers` assumed 1024 steps in every
  band, and there was a bug where **setting something like 34 GiB generated an invalid combination
  and made Start fail outright**.
- **EFS is slow with respect to the number of files, not the number of bytes.** There is a fixed
  penalty of about 14.5 ms per file, and the bandwidth difference is only about 1 ms per MiB.
  Neither raising parallelism from 16 to 64 nor raising vCPU from 2 to 4 to 8 improves it.

## Decision 1 — hold size as three numeric axes (bytes / cpu units / GiB), with named sizes in the UI and MCP

Add `cpu_limit` (vCPU units; 0 = unset) to `user_limit`, and **hold memory (bytes), CPU (units) and
disk (GiB) as three independent axes**. Named sizes (S/M/L, …) are not a storage format but **a way
of presenting choices** that the Console and MCP expand into the three axes.

Why the alternative (making named sizes canonical in storage — the original front-runner) is not
taken:

- The existing `mem_limit` (bytes) already runs through the API, MCP, the Console and the two-level
  quota, so **moving to tiers would require migrating existing values and rewriting every path**.
  Three axes need one added `ALTER TABLE`.
- **On the docker runtime an arbitrary byte figure is meaningful** (the granularity of
  `WS_MEMORY=5g`). Rounding to tiers would reduce the on-prem side's expressiveness. CPU also drops
  straight into `--cpus`.
- Fargate's discrete constraint is **absorbed by `fargateSize()` snapping to a valid combination**
  (which is already its shape). An invalid combination cannot reach a task definition even without
  tiers in storage.
- Tiers' advantages (the UI mirrors the constraint; it is easy to explain) are **obtained as they are
  by putting the choices in the UI layer**.

`fargateTiers` is reshaped to hold a per-band step (`stepMiB`), and `fargateSize` treats the
requested CPU as a lower bound (choosing the band from CPU as well as memory).

## Decision 2 — split disk between ephemeral and EBS at 200 GiB, and show the admin one number

- **1–200 GiB is ephemeral storage** (anything under 21 is rounded up to 21; measured, the API
  rejects under 21 and over 201).
- **Over 200 GiB is ECS-managed EBS.**
- The branch is confined inside the CP, and the concept the admin sees stays one thing: "disk GB".
- The default is **20 GiB** (the free allowance; with `disk_gb` unset, `EphemeralStorage` is not
  set). The deployment default can be changed with `AF_ECS_WS_DISK_GB`.

The prices measured out to be nearly identical (ephemeral $0.097/GB-month, EBS gp3 $0.096/GB-month),
but **ephemeral is free up to 20 GiB** and needs no infrastructure IAM role and no startup overhead
for creating, attaching and formatting a volume. Over 200 GiB, and specifying IOPS, are EBS's only
territory.

## Decision 3 — split where `~` lives by "average file size"

EFS's penalty is about 14.5 ms per file, and the bandwidth difference about 1 ms per MiB. So
**anything whose average file size exceeds 1 MiB stays within twice local speed even on EFS**. `~` is
split on that criterion.

| Contents | Where | Grounds (measured) |
|---|---|---|
| Credentials, connections, identity (the seven in `homeKeep`) | **EFS** | persistence is absolute; under 100 MiB |
| Tracked files and uncommitted changes in `~/repos` | **EFS** | persistence is absolute. `git clone` 4.9s, `git status` < 0.4s — bearable on EFS |
| Large tarball-shaped things like `~/.npm` and `ms-playwright` | **EFS** | `.npm` is 20.6 GiB but only 6,756 files (average 3.1 MiB) — not a shape EFS struggles with |
| `node_modules`, `target`, `dist`, `.venv` | **local** | average 17 KiB. The main reason `npm ci` is 9.4× slower |
| `go-build`, `uv`, `go/pkg/mod` | **local** | `uv` has 101,949 files in 1 GiB (average 10 KiB) — writing that to EFS is 26 minutes by simple arithmetic |
| `~/.local` (the CLI binaries) | **deferred** | 24,223 files. CLI startup cost unmeasured |

This split works because the relationship **"what is expensive to re-fetch has few files, and what
has many files is cheap to regenerate"** holds in the measurements (it comes from the structure that
package managers hold their distributions as tarballs, while the unpacked output and intermediate
artifacts are piles of small files). It is not a coincidence.

**Options not taken**:

- **Put all of `~/repos` on local storage** — the largest effect, but automatic idle stop would
  **destroy uncommitted work**.
- **Put all the caches on local storage** — the measurement *was* exactly that arrangement (the npm
  cache local, only the write destination on EFS), and it was still 9.4× slower. **The dominant term
  is writing the artifacts**, not reading the cache.
- **Switch EFS to Elastic throughput** — it does not help small files (tar unpack 98.3s versus
  bursting's 98.0s). It helps only sequential bandwidth (writing 1 GiB in 2.6s = 394 MB/s). If it is
  ever introduced, the reason will be bandwidth and insurance against burst-credit exhaustion, not
  build speed.
- **Solve it by raising the task size** — vCPU 2→4→8 changes nothing on the EFS side. It cannot be
  bought.

## Decision 4 — putting home on EBS is not adopted (impossible in principle on Fargate)

`ServiceManagedEBSVolumeConfiguration` (the service path, i.e. the current setup) has **no**
termination policy and the volume is always deleted when the task stops.
`TaskManagedEBSVolumeConfiguration` (`RunTask`) does have
`TerminationPolicy.DeleteOnTermination=false`, but **the API has no field pointing at an existing
volume**, so a retained volume cannot be reattached (ECS always creates a new one). Carrying over is
only possible via `SnapshotId`, which simultaneously brings "home rolls back to the last snapshot on
a crash", "it is slow right after restore because of lazy hydration from S3", "stopping takes
minutes" and "Service Connect is lost".

**It is not a cost question** (EBS $0.096 < EFS $0.36 /GB-month). Fargate simply has no "fast and
persistent". When it becomes genuinely necessary the answer is the EC2 launch type plus instance stop
(the volume survives a stop), and that is considered as a separate piece of work. docs/62's
"(d) EC2 launch type = rejected" gave its reason as "scale-to-zero disappears", so **there is room to
reconsider** on the premise of instance stop.

## Decision 5 — enable relocation by default (a 50 GiB default), and move artifacts out at the moment the working copy is created

Decision 3 was implemented, but **in the shipped state it never fired once**. Two causes, both the
difference between "put in" and "in effect".

1. **The working disk defaulted to 0 (i.e. Fargate's free 20 GiB).** Relocation is designed to become
   active only when the working disk is 30 GiB or more (docs/63 §63.6.1), so with the default no
   deployment meets the condition. → **Raise the default to 50 GiB** (`WsDiskGiB` /
   `AF_ECS_WS_DISK_GB`). Above the free allowance it is **$0.097/GiB-month, and only while the task
   runs**, so 30 GiB extra is $2.9 a month even at 24/7. `WsDiskGiB=0` restores the old behaviour.
   **Existing stacks do not rise automatically** (CFN retains parameter values).
2. **Relocating artifacts was manual.** `af-scratch node_modules` can only move a tree that already
   exists, and by then **the first `npm ci` has finished running on EFS** (the 105 seconds are already
   paid, and the move itself is slow too). The only shape that captures the benefit is **creating the
   symlink while it is still empty**, so **the Agent calls `af-scratch --auto` immediately after a
   clone or a worktree creation** (docs/63 §63.6.3).

**Tracked files are never moved** — something that exists is moved only when `git check-ignore` says
it is ignored. It is not applied to existing working copies (moving a huge tree on resume would stall
session startup). The price is that scripts shaped like `[ -d node_modules ] || npm install` are
fooled (disable with `AF_WS_SCRATCH_AUTO=0`).

## Impact

- `control-plane/mem.go` — reshaped to hold a per-band step. Includes fixing the existing bug
- `control-plane/migrations/0044_user_limit_cpu.sql` / `migrations-pg/0027_user_limit_cpu.sql`
- `control-plane/store.go`, `store_sqlite.go`, `store_postgres.go` — `UserLimit.CPULimit`
- `control-plane/workspace_lifecycle.go` — `resolveWorkspaceCPUUnits` / `resolveWorkspaceDiskGB`
- `control-plane/limits.go` — the tenant caps `max_workspace_cpu` / `max_workspace_disk_gb`
- `control-plane/runtime_ecs.go` — applying CPU, `EphemeralStorage`, EBS above 200 GiB
- `control-plane/runtime_docker.go` — `--cpus` (native has no cgroup, so it is ignored as before)
- `control-plane/tenants.go`, `mcp.go` — the configuration paths
- `console/src/features/settings/tenantMembers.tsx` — the named-size selection UI
- `workspace/entrypoint.sh` — splitting where things live (enabled only on ECS)
- `workspace/af-scratch.sh` — `--auto` (find the artifacts from markers and symlink them while empty)
- `workspace/agent/scratch.go` — a best-effort call after a clone or worktree creation
- `deploy/aws/ecs/cfn/30-ingress.yaml` — the `WsDiskGiB` default from 0 to 50
- `workspace/workspace-notes.md` — rewrite the persistence model's description for ECS (a promise to
  users)

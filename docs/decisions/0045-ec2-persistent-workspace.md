# 0045. Adopt the EC2 launch type for workspaces as "a pool of generic slots plus swapping a per-user EBS volume"

English | [日本語](0045-ec2-persistent-workspace.ja.md)

- Status: **adopted, in implementation** (2026-08-15. The design and the measurements are in
  [docs/64](../log/64-ec2-persistent-workspace.md)). Feasibility was **measured end to end** in an AWS
  sandbox (create from scratch → warm start → stop → terminate → move the volume → snapshot for
  hibernation → restore → resize, plus the second round of the pool model, hot swapping and the golden
  snapshot).
  ⚠️ **This ADR turned from "deferred with conditions" to "adopted" within the same day.** Decisions
  1–9 were written as the record of deferring; **the reason for turning and the shape adopted are in
  decision 10**. Decision 2 (not adopting it now) is overridden by decision 10, and decision 5 (the
  gate for starting) was passed by deciding **to start without waiting for it to be satisfied**.
  **Decisions 3, 4 and 6–9 are live** — they are the list of traps and shapes to carry into the
  implementation.
- See also: [64-ec2-persistent-workspace.md](../log/64-ec2-persistent-workspace.md) /
  [0044-workspace-sizing.md](0044-workspace-sizing.md) decision 4 (the homework that raised this ADR) /
  [63-workspace-sizing.md](../log/63-workspace-sizing.md) §63.4 (measuring EFS I/O) /
  [62-ecs-start-latency.md](../log/62-ecs-start-latency.md) §62.5 (rejecting (d) the EC2 launch type — revised by this ADR) /
  [0012-go-internal-refactor.md](0012-go-internal-refactor.md) (adapters hold no state in the CP)

## Context

ADR 0044 decision 4 said "Fargate has no 'fast and persistent'. When it becomes genuinely necessary
the answer is the EC2 launch type plus instance stop, to be considered as a separate piece of work."
This is that consideration. It also judges whether docs/62's "(d) EC2 launch type = rejected" still
holds.

## Decision 1 — it works technically (confirmed by measurement)

Within ECS, "one user = one instance plus a persistent EBS volume, stopped when unused" **can be
built**.

- A container instance joins the cluster **from user-data alone**, and the attributes it declares via
  `ECS_INSTANCE_ATTRIBUTES` are registered as they are. A service lands only on that instance with
  `launchType=EC2` plus the placement constraint `memberOf attribute:af-membership == <id>`.
  **Neither an ASG nor a capacity provider is needed** (`PlacementConstraint` cannot be used on
  Fargate, so this is a tool specific to the EC2 launch type).
- **Putting home on an additional EBS volume really does keep it** — through instance stop/start, and
  through **terminate → `AttachVolume` to a new instance** (`DeleteOnTermination=false`).
- **The image cache works** (pull 31.8s → **0.09s**).
- **Service Connect works on the EC2 launch type too** (verified from a Fargate client acting as the
  CP). `Endpoint()`'s contract does not have to change.
- **The CP can stay stateless** (ADR 0012). Instances, volumes and snapshots are all found by tag, and
  placement by ECS attributes.

## Decision 2 — ~~and yet we do not adopt it now~~ (continue with Fargate plus ADR 0044 decision 3)

> ⚠️ **Overturned by decision 10 (2026-08-15, the same day).** What follows is the record of the
> reasons at the time of deferring, and is not the current direction. Of the three reasons, (1) was
> already revised by decision 8 as "does not apply to the pool model", (2) is a comparison about the
> Fargate path, and only (3) (the operational surface going from 2 to 6 kinds) **remains as a price** —
> decision 10 takes it in the form of "run it in parallel as a separate adapter, so only deployments
> that use it pay".

Three reasons, all based on measurement.

1. **~~Startup does not get faster~~ (revised — see decision 8).** With one user = one fixed instance,
   stop→start→task RUNNING is **83.5s**, no different from Fargate's warm-home restart (**~84s** to the
   same point) — the 35 seconds saved on the pull are spent on instance start 19s, ECS re-registration
   1s and placement 13s. **But the pool model gives 22–27s**, so this must not be generalised into
   "EC2 does not make it faster". As grounds for deferring *this shape*, (2) and (3) remain.
2. **Decision 3 takes most of the benefit first.** The point of EC2 is I/O, but ADR 0044 decision 3
   (moving `node_modules` and the like to local storage) takes `npm ci` from 105s to 11s **with one line
   in the task definition and a branch in the entrypoint**. What is left for EC2 narrows to "the
   persistent areas (`~/repos`, `~/.local`) are fast too" and "no regeneration each morning".
3. **The operational surface goes from 2 kinds to 6.** Per workspace, from "a service plus an EFS
   access point" to "an instance, an EBS volume, a snapshot, a container instance registration, a
   service and a task definition". The ECS configuration has **zero production track record**, and this
   is not the moment to widen the surface.

**Cost is not a reason for deferring** — EC2 is in fact cheaper ($28.5 versus $39.6 at 160h/month with
a 45 GiB home; $7.7 versus $16.3 in a fully idle month). But since **EFS bills for what you use and EBS
for what you provision**, it is worth recording that the break-even fill rate is **26.7%**
($0.096 / $0.36).

## Decision 3 — what must be implemented if it is adopted (traps found by measurement)

These are pinned here because they will certainly be hit if forgotten when "we build it eventually".

1. **Resizing is a set of three steps.** Changing the type with `ModifyInstanceAttribute` while stopped
   makes the ECS agent **exit terminally** with `Container instance type changes are not supported` and
   never rejoin the cluster. You must do `DeregisterContainerInstance --force`, **delete
   `/var/lib/ecs/data/*`**, and `systemctl restart ecs` (measured at 46s to recover, with attributes
   preserved).
2. **Call `DeregisterContainerInstance` explicitly on deletion.** The SDK documentation states that a
   stopped or agent-disconnected instance is not deregistered automatically when terminated. Left alone,
   ghost registrations pile up.
3. **Do not depend on a public IP.** An awsvpc task ENI stays on an instance while the agent is
   disconnected, and in a multi-ENI configuration **no automatically assigned public IPv4 is attached at
   start**. In measurement that lost egress and the agent could not reconnect for 11 minutes.
   Production uses private subnets plus NAT so it is not exposed, but the premise is stated.
4. **The AZ becomes fixed.** EBS cannot cross AZs and a stopped instance retains its AZ, so if capacity
   is unavailable in that AZ the start fails. The only escape is recreating it in another AZ via a
   snapshot (30–40 minutes for 45 GiB).
5. **`State()` must be read together with the instance state.** A service's desired/running alone
   cannot say "the instance is starting" as `starting`.
6. **Keep the credentials alone on EFS, as a hybrid.** So that if a single-AZ, single-volume EBS is
   lost, the login information does not go with it (the seven items in `homeKeep` are under 100 MiB).

## Decision 4 — hibernation of long-unused users goes as far as a standard-tier snapshot; archive is not used

- Hibernation (stop the task → terminate → `CreateSnapshot` → delete the volume) and restoration
  (`CreateVolume` → a new instance → `AttachVolume`) both worked in measurement. **The only thing the
  user waits for is restoration's 122 seconds**; creating the snapshot (267s for 5.45 GB of real data =
  about 20 MB/s; 30–40 minutes for 45 GiB) can be asynchronous.
- The cost is **$4.80 → $1.00 for a user with 20 GiB used out of 50 GiB provisioned** (a snapshot bills
  only for used blocks).
- **It is 2.3× slower right after a restore** (reading 4 GiB at 57.4 MB/s versus a normal 135 MB/s).
  It applies only to what is touched, so it spreads out as "the first day is a bit heavy". It can be
  removed with `VolumeInitializationRate` (100–300 MiB/s, paid), but **Fast Snapshot Restore is
  $0.90/hour = $648/month** and is out of the question per user.
- **The archive tier ($0.0125/GB-month) is not adopted.** `RestoreSnapshotTier` takes **24–72 hours** to
  restore and carries a 90-day minimum charge. If it were ever used, it would be a feature stated
  explicitly in the Console as a separate state, "a dormant account", and could not sit on the
  continuum of automatic idle stopping.
- Hibernation is decided by **the last-used date, not by size** (the billing difference only matters for
  people who have not used it for a long time).

> **Decision 13 gave it one more role.** Hibernation was written as a cost measure, but it is also
> **the mechanism that makes automatic deletion reversible**. Making the retention sweep's action
> "hibernate" rather than "destroy" keeps the product in a state where **no path erases a home that
> nobody pressed a button for**.

## Decision 5 — ~~the gate for starting~~ (started without waiting for it)

> ⚠️ **Passed by decision 10.** **None of the five below can be said from measurement.** ADR 0044
> decision 5's implementation (hibernation enabled by default) went in, but with no re-measurement
> afterwards, **we started as a user's decision**. So this gate is not a record of "we proceeded because
> it was satisfied" but **a record kept so it is visible that we proceeded without satisfying it**.

After ADR 0044 decision 3 is implemented, proceed to the EC2 proposal **when any of the following can be
said from measurement**. Until then, do not start.

1. Even after decision 3, `git status` / `rg` on `~/repos` is observed to take over 5 seconds for one
   operation.
2. Regeneration each morning (`npm ci` plus the first build) exceeding 5 minutes becomes normal for some
   users.
3. EFS billing exceeds $10 per user per month.
4. **A request arrives that hits Fargate's size ceiling (16 vCPU / 120 GiB / ephemeral 200 GiB)** —
   this alone cannot be solved by decision 3, so it is a reason on its own.
5. **"Start in under 30 seconds" becomes a product requirement** (decision 8) — Fargate cannot
   structurally move from ~105s even with a warm home, and **only the pool model can produce 22–27s**,
   so it is a reason independent of the other four.

## Decision 6 — ECS Managed Instances is not an option

Confirmed against the type definitions in `aws-sdk-go-v2/service/ecs@v1.87.0`:
`InfrastructureOptimization.ScaleInAfter` **terminates** idle instances, `AutoRepairConfiguration`
**replaces** unhealthy ones, and `ManagedInstancesStorageConfiguration` **can only specify a size**
(there is no field pointing at an existing volume). **ECS owns the instance lifecycle, and the state
"stopped" does not exist.** Managed Instances is about "getting Fargate's ephemerality at EC2's price
and flexibility"; it is not about persistence.

## Decision 7 — revise docs/62's "(d) EC2 launch type = rejected"

There were four reasons at the time, but **the main one was wrong**, so it is rewritten (already
appended to docs/62 §62.5).

| The reason at the time | Verdict |
|---|---|
| **scale-to-zero's economics disappear** | ❌ **Wrong.** A stopped instance is not billed; only EBS is. EC2 is cheaper in the measured costs too |
| Capacity providers / ASGs / draining are added | ❌ **Not needed.** `launchType=EC2` works on a plain registered instance (measured) |
| AMI updates increase | ✅ **Valid.** This one remains (a long-lived instance holding an image cache needs a patch route) |
| It breaks "per-workspace means the CP is stateless" | ❌ It **does not break**, since everything is found by tag and ECS attribute. But the kinds of resource handled go from 2 to 6 |
| "The single-VM shape already exists as `ec2-single`" | ❌ **A different thing.** `ec2-single` is one all-in-one machine with no per-user isolation |

**The rejection itself stands as a conclusion (in the context of startup latency)** — measurement shows
83.5s versus ~84s, no difference, so it does not help docs/62's purpose. But **the reason is not
"scale-to-zero disappears" but "startup does not get faster".**

⚠️ **That applies only to the "one user = one fixed instance" shape.** Decision 8's pool model produces
**22–27s** and therefore **does help** even in the startup-latency context. docs/62's (d) is a record of
"that shape does not help", not "EC2 cannot be made fast". The axis for considering the EC2 launch type
is **I/O and persistence**, not startup latency, and those are handled by decisions 2 and 5 of this ADR.

## Decision 8 — if adopted, the shape is "a pool of generic slots plus swapping the EBS", not "one fixed instance per user"

We measured the shape in which **instances are not tied to users and only the per-user EBS is swapped**
(docs/64 §64.12).

- **`AttachVolume` to a stopped instance works** (3s). The path of waking from a stopped slot is start
  19s + registration 1s + attach/mount 8s + task 22s = **~50s**.
- **On a hot slot (left running), the swap is 22–27s** (24s to unload; the pull is 0.045s). **A quarter
  of Fargate's ~105s, and the only shape that also keeps a persistent home.**
- Placement needs only **the placement constraint `ec2InstanceId == i-xxx`** (no rewriting of
  attributes).
- Since the root volume (the image cache) is only needed **per slot**, storage is cheaper than the fixed
  model, which needs one root per user.

**A slot is exclusive to one user.** "Placing several users' tasks on one machine" did work technically
(mounting the EBS volumes on different devices with both tasks RUNNING; `MaximumEbsAttachments` is
**32** on the m7i family and is separate from ENIs), but **it is not adopted** — **EC2's on-demand price
is perfectly linear in vCPU** (m7i large / xlarge / 2xlarge are all **$0.0651 per vCPU-hour**), so
**packing them saves nothing at all on compute**. Co-tenancy only saves the per-slot root volume
(30 GiB = $2.88/month) and the amortised fixed host overhead (409 MiB measured), which does not balance
against sharing a kernel and a root. With exclusivity the pool model's advantages (Start 22–27s plus a
persistent home) survive intact, and since hot slots ≈ concurrently active users, **we pay for the same
amount of compute as running them concurrently on Fargate**. Note that with exclusivity the correct
thing is to **omit the task's `cpu` and let it use the whole machine** (on EC2, `cpu` is a reservation
of the instance's CPU units).

**The prices that remain even with exclusivity** (to be designed in if adopted):

1. **The root volume is shared with the previous user** — the container's write layer and `/tmp` remain.
   ECS's cleanup, `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION` (**3h in the agent README's default table**;
   there is also a `..._JITTER`), must be tightened.
2. **`/tmp` carries two risks, residue and shared capacity** — while running it is separated by a mount
   namespace, but it physically sits on the shared root volume and is readable by anyone who can reach
   the host (a route that did not exist with Fargate's per-task microVM). And one person filling it takes
   down everything on that slot. **On EC2 you can make `/tmp` a tmpfs with `linuxParameters.tmpfs`
   (`containerPath` plus a mandatory `size` plus `noexec,nosuid,nodev`)** (unavailable on Fargate) =
   nothing is written to disk, it vanishes at exit, and it has a cap. A quota on the write layer
   (overlay2 plus xfs prjquota) and deleting the container when the slot is returned go in alongside.
3. **A route is needed for the CP to run `mount` / `umount` on the instance** (measured with SSM
   SendCommand). **Always umount before detaching** (a forced detach corrupts the filesystem).
4. **A pool is needed per AZ** (EBS is AZ-bound).

**The speed/residue trade-off** (docs/64 §64.12.4): disposable slots (terminate on every return) leave
zero residue but **throw away the image cache too, returning to ~120s**. What we adopt is
**hot + exclusive + tmpfs + a short cleanup** (22–27s).

## Decision 9 — a new user's home is created from a "golden snapshot"

There is no need to make every new user pay for boot-install (4 CLIs 41s + rtk 1s + agy 6s = **48s**) and
a first `npm install` with an empty cache. **Bake one snapshot of a home that is boot-installed with a
warm cache, and create new users' EBS volumes from it** (measured in docs/64 §64.13).

- **`CreateVolume` (from the snapshot) plus attach plus mount is 17–20s.** The golden home's task is
  **RUNNING in 17s and ready 4s later** (`npm ci` 3.8s, no network needed). An empty home pays 15.3s over
  the network for the first `npm install`.
- **The lazy-hydration tax was zero for small files** — a full read right after a restore took 25.0s,
  **the same** as reading the source volume with the same contents after drop_caches (26.1s). The 2.3×
  tax seen in §64.7 shows up on **a 4 GiB sequential read**, and in home's access pattern (small files,
  IOPS-bound) hydration hides behind it. **A metadata walk over 23,012 files takes 0.118s.**
- **One golden for all users** (a snapshot can grow any number of volumes). The cost is $0.05/GB-month ×
  1.
- **Tie re-baking to releases.** When the image or a CLI pin is raised, re-bake the golden too (start one
  machine, run the entrypoint, take a snapshot). Forgetting means only new users start on old CLIs, so
  **the image tag is stamped on the golden and the CP reconciles it.**
  - **Addendum (2026-08-23, [docs/73](../log/73-dev-deploy.md) decision 3)**: reconcile by **contents**.
    In addition to `af-image` (a reference string), stamp **`af-image-fp` (a fingerprint made from the
    per-platform manifest digests)**; when both sides have it the fingerprint decides, and otherwise
    strings are compared as before. **A reference is not identity** — merely re-placing the same digest
    under a different tag triggers a re-bake (10 minutes and two slots in docs/72 §72.6.4), and
    conversely pushing new contents to a mutable tag leaves the strings matching while **an old home is
    distributed to new members**.

### 9-1 — the CP does the re-baking (2026-08-21, docs/64 §64.29)

The "if you forget to re-bake" above was **a promise kept by procedure**, and the only way to notice it
had not been kept was "a warning keeps appearing in the CP's log". **The trigger is not "we deployed"
but "the workspace image changed", and the CP already holds that judgement** (`goldenSnapshot()`
reconciles `af-image` and rejects an old golden). We turn a judgement it already holds into action.

- **The shape is the same as hibernation (decision 4). One step per tick, with state on AWS tags**
  (ADR 0012). Baking takes minutes, so no loop may wait on it. If the CP dies part-way, the next tick
  continues from where it stopped.
- **The seed is a reserved membership** (`af-golden-seed` in tenant `af-golden`) started **through the
  product's normal Start path**. Decision 9's proviso of not reimplementing the entrypoint applies here
  too.
- **★ Baking alone does not publish it.** It is first placed as `af-role=golden-candidate`, and **a
  different reserved membership with no history** (`af-golden-probe`) is started from that candidate;
  only once the Agent comes up is it promoted to `af-role=golden`. **A golden that cannot start looks
  entirely successful right up to "baked", and what breaks is the next new user, whose only symptom is a
  restart loop** (actually hit in docs/64 §64.28.3). The probe **must be a new membership**, because that
  fault only appeared **when the keep-side EFS was new**, and re-starting the seed would not catch it.
- **The brakes**: do not begin unless two slots are free (do not take them from real users; the price of
  having no golden is only a slow first start, which is no reason to evict anyone); give up after two
  failures on the same image; `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` turns it off.
- **A rejected candidate is not deleted.** It is "why this deployment has no golden", and at the same
  time the tally for giving up. It is shown in the operations screen (the pool) too — a single log line
  flows past.

**An option not taken**: promoting the first real user's home to be the golden. It needs no seed and
looks neat, but it means snapshotting **a volume that already belongs to someone**, and "before they
touch it" becomes a race against time. Get it wrong and that person's data is distributed to everybody —
a silently broken pattern, so it is not adopted.

## Decision 10 — turn to adopting it. The shape is the pool model, **run in parallel as a separate adapter from the existing Fargate path**

**On 2026-08-15 we start, on the user's decision.** Decision 5's gate is not satisfied (none of the five
can be said from measurement). We proceed anyway because there are two things **we decided to go and get
rather than wait and measure**.

1. **Start 22–27s** (decision 8). Fargate cannot structurally move from ~105s even with a warm home.
   ⚠️ **That figure was revised downwards once the implementation was measured (decision 10-5). The
   22–27s was measured without Service Connect.**
2. **Lifting the size ceiling** (16 vCPU / 120 GiB / ephemeral 200 GiB). As in decision 5-4, this alone
   cannot be solved by ADR 0044 decision 3.

**So this start is not "demanded by measurement" but "led by a product judgement"** — decision 2's (3)
(the operational surface going from 2 to 6 kinds; the ECS configuration has zero production track record)
**remains as a price**, and it is taken by the two things below.

### 10-1. Run it in parallel as a separate adapter, `AF_RUNTIME=ecs-ec2` (not one line of the Fargate path changes)

`runtime_ecs.go` (Fargate) is **not touched**. The EC2 pool model is a new `runtime_ecs_ec2.go`, and
`ecs-ec2` is added to `newRuntimeFactory`'s profile branch.

- **The retreat being one line of profile** is the substantive substitute for the unsatisfied start gate.
  Branching on launch type inside one adapter would require a revert to retreat, and would put branch
  risk on the Fargate path too.
- **Common parts are factored out and shared** (the token/DEK in an SSM SecureString, `Endpoint()`'s
  Service Connect contract, `watchReady`, assembling the env). **Do not duplicate by copying.**
- Even with a different launch type, **`Endpoint()`'s contract does not change** (as in decision 1,
  Service Connect works on EC2 too).

### 10-2. P0 is the lifecycle only. The golden snapshot, hibernation and restoration come later

**P0 (the scope of this start)**: acquire a slot → `AttachVolume` → mount → place the task → `umount` →
`DetachVolume` → return the slot, plus `Start` / `Stop` / `State` / deletion. **Every trap from decisions
3 and 8 goes into P0** (making `/tmp` a tmpfs, `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION`, umount before
detach, a pool per AZ, `DeregisterContainerInstance`, no dependence on a public IP, the three-step resize,
the EFS hybrid, one user per slot). These are the kind of traps that cannot be "added later" — fix them
after hitting them and by that point a corrupted filesystem or someone else's `/tmp` residue really
exists.

**P1 onwards**: the golden snapshot (decision 9) / snapshot hibernation and restoration of long-unused
users (decision 4) / pre-warming and shrinking the pool.

### 10-3. The working disk `/scratch` is not injected on the EC2 adapter

ADR 0044 decisions 3 and 5 (moving artifacts to a working disk) address **EFS's small-file tax**, and on
the pool model home becomes a per-user EBS volume so **that tax disappears** (measured in decision 1:
creating 2,000 small files takes 30.7s on EFS versus 0.04s on EBS). The implementation is a complete
no-op unless `AF_WS_SCRATCH` is injected, so **the EC2 adapter does not inject it** — that is the default
judgement.

There is, however, **a different motive**: "keep the EBS home's snapshot and hibernation size small by
moving regenerable artifacts to the local root". That only starts to matter when P1 brings in the golden
snapshot (decision 9) and hibernation (decision 4), so **it is reconsidered then** (noting that decision
8's price 1, "the root is shared with the previous user", and the write-layer quota become design
constraints on `/scratch` directly).

### 10-5. What was learned by running the implementation on real AWS (details in docs/64 §64.16)

**"Start 22–27s", the grounds for adopting it, does not happen with Service Connect.** The 22–27s
decision 8 quoted was from the measurements in docs/64 §64.12, taken **with bridge networking and no
Service Connect**. The product uses Service Connect for CP → Agent reachability (`Endpoint()`'s contract,
decision 1), so as §64.4.3 says, **awsvpc plus SC adds roughly 20 seconds**. Measured through the
adapter, it is **a band of 13–95 seconds** (13.2s when everything is warm, 52–94s when moving an existing
home, 82s for a new home).

**It is still faster than Fargate's ~105s, but it is not "a quarter".** Decision 5-5 ("Start in under 30
seconds" being a requirement is a reason to start on its own) **cannot be met in this shape** — meeting
it would require dropping Service Connect and changing `Endpoint()`'s contract (having the CP resolve the
instance's private IP and a fixed port itself), which is a separate decision. The persistent home, the
I/O and the lifted size ceiling are obtained as originally expected, so the adoption itself stands.

The implementation was wrong in four places against measurement (docs/64 §64.16.2). Two of them were
design holes:

- **Putting a `PosixUser` on an EFS access point makes the task fail to start on the EC2 launch type**
  (ECS hands EFS to Docker, and the `lchown` in the stage where Docker replicates the image's ownership
  information is rejected). Fargate mounts EFS itself and has no such path.
  → EC2 uses a dedicated access point with no `PosixUser` (`rootDirectory` is shared, so the contents
  continue across a profile switch).
- **Reading "attached but no service" as `starting` makes Start a permanent no-op** (Start returns early
  on `starting`, so nobody can start a workspace that died between the attach and the CreateService).
  → Only **a claim tag with an expiry** is allowed to declare that convergence is in progress.

The other two are handling AWS's eventual consistency (a device is released 8–9 seconds after the detach
response; a window where `DeleteVolume` returns `VolumeInUse`).

### 10-4. Two decisions that moved once implemented (details in docs/64 §64.15.9)

1. **Drop `noexec` from `/tmp`'s tmpfs by default.** Decision 8 said `noexec,nosuid,nodev`, but in a
   development container installers and test runners execute from `/tmp` every day, and `noexec` breaks
   that in the shape of "Permission denied". What decision 8 actually wanted —
   **"nothing is written to the shared root" and "there is a cap"** — both hold without `noexec`.
   It can be restored with `AF_ECS_EC2_TMP_OPTS`.
2. **The occasion for decision 3-1 (the three-step resize) disappears in the pool model.** Users are not
   tied to instances, so a resize becomes **"move onto a slot of a different size"** (Stop → attach to a
   different pool → Start) and never calls `ModifyInstanceAttribute`. Trap 1 was the kind that
   **disappears the moment the shape is chosen**. The procedure itself stays in decision 3, for "changing
   a slot's type operationally".

## Decision 11 — a slot is "put to sleep, not returned" (deferred return + idle stop + eviction only at the cap)

Running the implementation on real AWS (decision 10-5) showed that **Start's speed is decided by whether
home is already on the slot** (13.2s if it is, 52–94s if it must be moved). So **do not detach on Stop**.
Details in docs/64 §64.17.

1. **Deferred return**: `Stop` only sets the service's desired to 0, leaving home attached.
   **The affinity is the attachment itself**, so there is no need to remember "which slot last time"
   separately — the same person naturally returns to the same slot, on the fastest path (keeping
   ADR 0012 intact).
2. **Idle stop**: the sweep loop issues **`StopInstances`** at `AF_ECS_EC2_SLOT_SLEEP_SEC` (15 minutes by
   default) — never terminate, so the image cache stays on the root. A stopped slot **bills only for the
   root EBS** ($9.6/month versus $95 running), and the person's return takes ~90s.
   ⚠️ **This also fixes a design omission in P0** — P0 never shrinks, so it **paid for the peak
   concurrent machine count 24/7**.
   ⚠️ **It is a different thing from the existing reaper** (`AF_WS_IDLE_TIMEOUT` / a tenant's
   `ws_idle_timeout`). That one **watches people and stops the workspace** (runtime-independent). This is
   **a second tier that takes effect after it** and **stops the box of a stopped workspace**. They are in
   series, so the actual wait is "the reaper's setting + 15 minutes". They are named differently so as
   not to be lumped together as "idle".
3. **Eviction only at the cap**: below the cap, grow the pool (with idle stop in place, the waiting cost
   of the extra machines is just the root EBS). Only at the cap is a slot taken from **the longest-dormant**
   user. **A running one is never chosen.**
   ⇒ `AF_ECS_EC2_MAX_SLOTS` decides "how many slots are held" and `AF_ECS_EC2_SLOT_SLEEP_SEC` decides "how
   many of them are running" — a division of roles.
4. **No pre-warming of hot free slots** (the user's decision). The first person in the morning pays ~90s
   (waking a stopped slot) or 135s (the pool is empty).

**Do not umount when detaching from a stopped slot** — SSM does not reach it, and the mount is already
gone (stopping an instance is a normal shutdown and systemd unmounts). Waiting on an umount that cannot be
delivered would make a sleeping slot permanently unreclaimable.

## Decision 12 — remove startup time from the reasons for adopting (a second downward revision by measurement)

Decision 10-5 corrected it to "22–27s was without SC; measurement gives 13–95s", but **re-measuring with
deferred return in place gave 43–110 seconds** (docs/64 §64.17.5). And §64.16's 13.2s **appeared once in
three runs under the same conditions and does not reproduce** (most naturally read as having hit a window
where the previous task was still counted in `RunningCount`).

**Against Fargate's ~105s warm restart, there is almost no startup-time advantage.** The 35s saved on the
pull is eaten by awsvpc's ENI plus Service Connect (about 20s), the CP's mount (an SSM round trip of
10–20s) and the propagation of the service update.

> ⚠️ **Correction (docs/64 §64.38, 2026-08-26)**: the "SSM round trip 10–20s" was an estimate; measured in
> production it is **2–4 seconds** (`af-mount` itself has a median of 0.6s, n=37). What eats the time is
> not the mount but **ECS's task placement and container start (90–120s)**. **The conclusion (almost no
> advantage; the reasons for adopting are I/O, persistence and size) is unchanged.**

> ⚠️ **It turned out we were creating 40 seconds of it ourselves (fixed; docs/64 §64.39, 2026-08-26)**: of
> that, **40 seconds** are made not by ECS but by **how we issue the request**. `upsertService` issues
> "`desiredCount` 0→1" and "swap the task definition" in **one `UpdateService`**, so ECS **satisfies the
> increment against the old revision's deployment first** — if the slot the old revision points at is
> alive, **a task on the old image runs for one or two minutes and sits alongside the real one in Service
> Connect**, and if it is gone it **spins for about 41 seconds** because `MemberOf` is unsatisfiable.
> **40% of starts (6 of 15) on the production deployment** hit it, and the difference is a consistent
> +39.7 to +40.7 seconds.
> The fix (implemented) is (1) update only the td first → (wait for the `ACTIVE` old deployment to
> disappear, measured at 10–23 seconds, overlapping the 18s wait for the slot's ECS registration) → (2)
> `desiredCount=1`. The A/B is measured (one call = 2 tasks; split = 1 task). ⚠️ **Sending
> `ForceNewDeployment` in (2) puts it back.** The saving differs by path: waking or building saves the
> whole ~40 seconds, while **moving onto a running free box exposes the wait and saves 20–30 seconds**.
> Alongside that, **the fingerprint is baked into the revision's docker labels so a `lastTaskDef` miss is
> re-asked of AWS** (`serviceTaskDefIfFingerprint`). A process-local cache is always lost on a CP restart
> = on every deployment, and it **registered a new revision and rolled the service even when the contents
> were identical**.
> ⚠️ **Verified on a real-hardware harness** (docs/64 §64.39.8): after the fix, both a Start that changes
> the task definition and a Start simulating a CP restart produce **one task**, the latter with **zero new
> revisions**.
> ⚠️ **The biggest error found there was the wait's cap** — it had been 25 seconds so as to fit inside the
> Start HTTP request, but real retirement takes **53–55 seconds**, and the 25-second version cut it short,
> scaled up and **stood up two tasks on the spot**. The wait was moved **outside the request** (`bg`) and
> the cap raised to 90 seconds.
> ⚠️ **The only racy part was "whether one combined call creates that condition"** (§64.39.10).
> **Given the condition (raising the count while an `ACTIVE` exists) plus `maximumPercent 200`, two tasks
> is deterministic**, and an A/B on the same substrate split 2/2 versus 2/2.
> ✅ **So the real fix is the service's `deploymentConfiguration`** — changing the default 200% to
> **`maximumPercent 100` / `minimumHealthyPercent 0`** (`ec2SingleTaskDeployment`) means **ECS cannot
> stand up a second task for this service**. §64.39's mechanism disappears structurally, so **the
> retirement wait (53–55 seconds on real hardware) was deleted entirely**.
> ⚠️ **Put the setting on all four call sites** (the default is on AWS's side, so services created before
> this change stay at 200 until somebody sends it). ⚠️ `minimumHealthyPercent 0` admits "running may reach
> 0 during a deployment", and it is the necessary counterpart because **at max 100% there is no room to
> stand the new one up first**. Two tasks at once on a workspace is **always harmful** (SC splits across
> both), so it is semantically right as well.
> ✅ **Passed on a real-hardware harness (docs/64 §64.39.11, 2026-08-27)**: a new service is `100/0`, and a
> task-definition-changing Start against **a service reverted to 200/100** (i.e. every production service)
> **returned to `100/0`, one task, and zero tasks on the demoted deployment**, on both runs. The ordering
> was measured too: **`max=100` costs +4s and `desiredCount=1` +6s** — **safe from the very first Start
> after deployment.**
> ⚠️⚠️ **`maximumPercent 100` cannot be sent on its own (§64.39.12; a production incident on 0.12.3)**:
> ECS's **AZ rebalancing rejects `maximumPercent <= 100`** (400), and `availabilityZoneRebalancing`
> **is fixed at creation time, with an omission in `UpdateService` meaning "keep the existing value"**.
> Services created by the new version pass because `CreateService` silently sets it to DISABLED, so
> **only services created by the old version become unstartable**. `ec2NoAZRebalancing` (DISABLED) is
> **sent on the same four call sites**. Since the task is pinned to one machine and home cannot leave that
> AZ, **there is nowhere to rebalance to** — DISABLED is the correct value, not a workaround.
> ★ **§64.39.11 was green because "the pre-upgrade state" was created with an `UpdateService` against a
> new service** — **a new thing you made and then edited is not an old thing.**
> ⚠️ But **"40 seconds" has not gone entirely**: the count first goes into the demoted `ACTIVE`, and moving
> to `PRIMARY` takes **29–42 seconds** (harmless, since no task can be stood up, but slow).
> ⚠️ What 100% caps is **one task in total**, not "one `PRIMARY`" — there is one observation of the demoted
> side taking that one first (it never becomes two at once).
> ⚠️ The Fargate side (`runtime_ecs`) is **in the same shape** — the surface this decision says it does not
> change. But **it reproduced once on real hardware** (two Starts giving 2 tasks then 1), so it is no longer
> speculation. Whether to fix it is left as a separate judgement (docs/64 §64.39.6.3).

So **the reasons for adopting the EC2 pool model narrow to two: "I/O and persistence" and "lifting the
size ceiling"**. Decision 5-5 ("Start in under 30 seconds" being a requirement is a reason on its own)
**cannot be met in this shape** — meeting it would require dropping Service Connect and changing
`Endpoint()`'s contract, which is a separate decision.

**The adoption itself stands** (a persistent home, 8–30× small-file I/O, and the lifted size ceiling are
all confirmed on real hardware). But **do not sell it as "faster"**.

> **Re-measuring in a production-equivalent setup (private subnets plus NAT) did not move it** (docs/64
> §64.19.2). A warm return is 84–97s. The network's shape does not affect startup time.

## Decision 13 — create a seam for "destroying" a workspace. **The automatic path only goes as far as hibernation**

Decision 10-5 implemented `Destroy`, but **there is no caller anywhere**. Trying to wire it up revealed
that this is not "connect it to an existing hole" but **creating a new operation, destroying a
workspace**.

**Three facts settled first** (confirmed by reading `control-plane`):

1. **Removing a membership is not the seam.** `DELETE /api/admin/memberships` is a **logical delete**
   (`status='inactive'`) and is **explicitly designed** as "keep the workspace, home and encrypted
   secrets; restoring is just re-inviting" (`removeMembership` in `tenants.go`). Adding destruction there
   contradicts the intent of decisions 22/27.
2. **ADR 0028 (the deletion lock) does not cover workspaces.** It protects sessions, working copies and
   conversations, and the lock lives **entirely inside home** (`~/.config/agent-fleet/`). In other words,
   **when destroying a stopped workspace the CP cannot read whether a lock exists**. docs/64 §64.15.9 (3)
   said "wiring is ADR 0028's homework", but what it actually meant was "there is no seam anywhere".
3. **The hole is not only in ecs-ec2.** Fargate too has two per-membership EFS access points, two SSM
   SecureStrings and an ECS service that outlive the membership. docker and native leave `dataDir`.
   **Even ecs-ec2's `Destroy` was incomplete** — it deleted the EBS but left the service, the access points
   and the SSM parameters (it did not fold up the `base`-side resources).

### 13-1. Split into two operations of different strength

| | Trigger | What it does | Reversible |
|---|---|---|---|
| **Hibernate** | An automatic sweep N days after stopping | Take a snapshot, then delete the volume. The next Start restores from the snapshot | ✅ |
| **Destroy** | Only when a person presses | Delete everything, including the hibernated snapshot | ❌ |

**The point of the split is that the automatic path is never irreversible.** "A home disappears though
nobody pressed anything" must not be in the product. **Decision 4's hibernation (a standard-tier snapshot,
decided by last-used date) becomes the automatic sweep's action directly** — decision 4 was written as a
cost measure, but **it is simultaneously the mechanism that makes automatic deletion reversible**.

### 13-2. Three triggers. Anything irreversible only when a person presses

1. **An explicit admin operation** (`DELETE /api/admin/workspaces {tenant_slug,user_key}`) — destroy.
   **It targets inactive memberships only** (so a stray click in the admin screen cannot erase the home of
   someone still employed). Recorded in the audit log.
2. **The retention sweep** — hibernation only. Off by default.
   > **It briefly fell to per-deployment, and decision 14 brought it back.** The sweep loop derives the
   > world from EC2 tags and can see neither tenants nor the CP's database (ADR 0012), so the first
   > implementation had only `AF_ECS_EC2_HIBERNATE_AFTER_SEC`. Resolved by moving the trigger up to the
   > reaper.
3. **`purge=true` on removing a membership** — destroy. For finishing an offboarding in one action.
   The default is false, and the current "keep the home" contract does not change.

### 13-3. `Destroy` is implemented in all four adapters

To avoid "the same button works or does not, depending on the environment". `runtimeDestroyer` moves to
`runtime.go` (the port side).

| Adapter | What is folded up | What remains |
|---|---|---|
| docker | the container, the network (Stop handles those), `dataDir` | nothing |
| native | the process (Stop), `dataDir` | nothing |
| ecs (Fargate) | the ECS service, two EFS access points, two SSM parameters | ⚠️ **the actual data on EFS** |
| ecs-ec2 | the four above plus returning the slot, the home EBS and its snapshots | nothing |

⚠️ **On Fargate the CP cannot delete home's actual data.** The EFS directories
(`/home/<membership>` and `/claude-config/<membership>`) cannot be deleted without mounting, and deleting
the access point leaves the data. **The billing remains too.** So that nobody "thinks they deleted it",
`Destroy` **reports the paths left behind as an explicit return value rather than an error**, and puts them
in the audit log. Actually emptying EFS would require running a throwaway task, which is a separate
decision.

### 13-4. Its relationship to the deletion lock

The inside of a stopped workspace cannot be read, so **destroying a workspace cannot respect ADR 0028's
lock**. That is not laziness but a structural consequence of where the lock lives (inside home).
Therefore:

- **Hibernation does not conflict with the lock** (it is reversible and the contents live on in the
  snapshot). That the automatic path only goes down this route is right on this count too.
- **State explicitly that destruction is an operation that overrides the lock.** Write it in the UI the
  admin presses.

## Decision 14 — put hibernation's trigger in the reaper (the sweep loop only "resumes" things)

Decision 13-2's "enable it in tenant settings", delivered in implementation. **The layer that decides
timing and the layer that does the work were separated.**

| | Decides | The world it can see |
|---|---|---|
| **the reaper** (`hibernateHome`, tier 3) | **when to begin** | the tenant, `limits`, `last_active_at` (i.e. the database) |
| **the sweep loop** (`sweepVolume`) | **how to advance what has begun** | EC2 tags only (as in ADR 0012) |

- The seam is `hibernatingRuntime` (`BeginHibernate`), **one method on an optional interface**. Like
  `runtimeDestroyer`, only ecs-ec2 implements it — on other runtimes **tier 3 does not half-work; it does
  not exist**.
- The setting is `limits.home_hibernate_after`, with the same resolution rule as the other two idle
  settings (empty = the deployment default `AF_ECS_EC2_HIBERNATE_AFTER_SEC`; `"0"` = do not hibernate for
  this tenant).
- **Tier 3 looks at stopped workspaces.** Tiers 1 and 2 return immediately for anything not `running`, so
  the trigger has to go **beyond** that return. Get this wrong and it never fires despite the setting being
  enabled.
- **Do not mix `bootTime` into tier 3's clock.** `idleBase` (tier 2) uses the process start time as a
  lower bound — correct at minute scale, but hibernation's window is days to weeks. **On a deployment
  where the CP restarts more often than that window, the deadline moves backwards forever** (it looks
  enabled and never fires). The only readable thing is the persisted `last_active_at`, and **if it cannot
  be read, do nothing**.
- **`BeginHibernate` only begins.** If `af-hibernating` is already stamped it does nothing — the reaper
  and the sweep loop advancing the same hibernation at once would run two `CreateSnapshot`s and leave an
  orphaned snapshot billing forever. The only authority on whether it is running is the tag on the AWS
  side.
- **The sweep loop no longer starts hibernations.** The branch that carries a running one to completion
  stays (without a gate) — so that turning the feature off part-way does not leave you paying for both the
  snapshot and the volume. **As a side effect, a deployment that stopped the reaper
  (`AF_IDLE_SWEEP_INTERVAL=0`) no longer hibernates.**

## Decision 15 — only "people not yet bound" can have their AZ re-chosen

Closing the hole found in docs/64 §64.20.4. A new home's AZ was hard-coded to `anyAZ()` = **the first of
the configured subnets in id order**, and if there was no capacity there `runSlot` simply failed —
existing users kept working while **only new users could not be started** (which does not look like an
outage from outside).

- **For new homes only, if `RunInstances` returns insufficient capacity, try the next AZ** (`growPool`).
  An existing home stays pinned to its own AZ and fails — **standing up a slot somewhere it cannot go only
  adds a box that cannot be attached to**.
- **Do not retry on failures other than capacity.** An invalid launch template or a quota overrun fails
  the same way in every AZ, and trying three times buries the real reason in the last one.
- **The substance is a change to the order of creation.** Previously the AZ was decided and **the volume
  was created first**. As long as a free slot exists the answer is the same, but **on insufficient capacity
  the only way to try elsewhere becomes "delete the volume you created and start over"**. Harmless for an
  empty home, but **for a home restored from a snapshot the data is lost the moment it is deleted**
  (`createHomeVolume` deletes the source snapshot once the volume is usable). **Not creating anything until
  the destination is decided** removes the choice entirely.
- As a side effect, **eviction at the cap now crosses AZs**. Previously it was "the longest-dormant within
  an AZ chosen before anyone was looking", which could *evict someone who stepped away ten minutes ago
  while leaving a week-idle home in another AZ*.
- ~~**No spreading** (it stays hard-coded).~~ → **Overturned by decision 16.**
- ⚠️ **Unverified on real AWS.** Insufficient capacity cannot be induced deliberately in a sandbox. The
  fake imitates AWS's error strings, but **whether it actually arrives with that string has not been
  confirmed on real hardware**.

### 15-1. No operation is built to move a user to a different AZ (hibernation is that operation)

EBS cannot cross AZs and AWS has no "move" — in reality it is always recreation via a snapshot, **which is
the same procedure as hibernation (decision 4)**. So no dedicated operation is added:
**stop → hibernate → make room only in the target AZ → Start** (the procedure is in docs/64 §64.20.7).

⚠️ **The destination cannot be named.** All you can choose is "in which AZ to make room". For one person
with a named destination, do the same four steps directly with the AWS CLI (a note in §64.20.7).
If naming the destination is ever to become a product operation, first decide where it lives (the member
detail in the admin screen) and **what happens when a Start arrives mid-capture** (the same problem as
§64.18.3.1).

## Decision 16 — spread new homes across AZs (overturning decision 15's "do not spread")

Decision 15 said "new homes clustering in the same AZ is intentional", without looking at the consequence:
**when one AZ goes down, the blast radius is not half but nearly everybody.** A home cannot be evacuated
(EBS does not cross AZs), so the only available move is "do not put them in the same place to begin with".

- `growPool` tries AZs **in order of fewest homes**. Ties keep the previous stable order.
- ⚠️ **There is a price.** A home in 1a can only ride a slot in 1a, so free capacity in the other AZ is
  worthless to that person, and **the pool grows instead of being reused** (more instances for the same
  number of people).
- **`placeHome`'s "prefer a free slot" is untouched.** Spreading decides only **where to stand up a new
  slot**, not whether to use one that already exists.
- If counting fails (an API failure) it falls back to the previous fixed order. That is not a reason to
  stop placement.

## Decision 17 — take periodic spare copies of home (the only escape from losing an AZ)

An out-of-AZ copy of home **existed only for people who happened to be hibernating**. An availability
incident can be waited out, but **if an AZ is lost, that home is gone.** Snapshots are a regional resource,
so this is the only escape.

- The trigger is the reaper's **tier 4** (a tenant's `home_backup_every`). It runs **regardless of idleness**
  — what it protects does not wait for people to go home. The seam is the optional `backingUpRuntime`
  interface, implemented only by ecs-ec2 (other runtimes' homes are not bound to one AZ in the first
  place).
- **Taking it while in use = a crash-consistent copy.** Quiescing would mean taking a working person's home
  away on a timer, which is worse as a product. ⚠️ This is **the opposite judgement from `bake-golden.sh`'s
  "do not snapshot while attached"**, and for the opposite reason (the golden is everyone's initial state
  and has the right to be clean).
- **It is never restored automatically.** A spare is by definition older than home, and silently handing
  over an old home is exactly the failure this ADR has consistently avoided. Restoring is an operator
  action (the procedure is in docs/64 §64.21.4).
- **A third role, `af-role=backup`.** Every existing query filters on `af-role`, so it is caught by neither
  restoration, nor hibernation's cleanup, nor the golden search. **`Destroy` deletes the spares too** (being
  a different role they are invisible to the other cleanups, and left alone a leaver's spares bill
  forever).
- **No state in the CP.** When the next one is due is read from the latest spare's `af-backup-at`
  (ADR 0012). Two replicas firing in the same window make one extra, but it is incremental so it is cheap,
  and the retention count drops it soon.
- **The interval is the tenant's; the count is the operator's** (`AF_ECS_EC2_BACKUP_KEEP`, default 3). How
  far back you may roll is the tenant's judgement; how many you pay for is the deployment's.
  **Only completed ones are pruned** — so the count does not drop below the setting during a replacement.

### 17-1. Avoiding an AZ based on health is not done for now

With a failure that does not return a capacity error (instances start but never register with ECS), new
users flow into a dead AZ and wait. No automatic skipping was added — **an operator removing it from
`AF_ECS_SUBNETS` is more reliable and the judgement is explicit**. If it is ever added, "skip for a period
after consecutive failures" is the minimum, but the failure mode of a false positive narrowing the whole
pool is worrying.

⚠️ **Decisions 15–17 are all unverified on real AWS.** Neither an AZ failure nor insufficient capacity can
be induced in a sandbox.

## Decision 18 — bake the slots' image cache into an AMI (rather than a seed instance per AZ)

> ⚠️ **Withdrawn by decision 19, and the implementation removed.** Baking really does remove the pull
> (measured 31.8s → **0.185s**), but **the first start of a custom AMI costs more than that** (a new user's
> Start goes from 144.0s to 179–192s). What follows is kept as a record of the judgement — the design was
> right, and **what was wrong was the premise.**

A newly stood-up slot's root is always cold, so **the first person in an AZ, growing to the cap, and
standing one up in a new AZ under decision 16's spreading** each pay for the image pull (pull 31.8s → 0.09s,
decision 1). The idea of "keep one stopped instance per AZ to warm just the root" is **right in principle**
— stopping is cheap, the value is in the root, and it need not be in the pool — but what we want warm is
the root rather than the instance, and **the way to have a warm root with no instance is an AMI.**

| | A seed instance | **An AMI (adopted)** |
|---|---|---|
| Scope | only the first person in each AZ | **every slot creation thereafter** |
| Number of AZs | one machine per AZ | **a regional resource; one covers every AZ** |
| Pool bookkeeping | needs contrivances to keep it out of the cap and the free list | it never appears |
| Contention | needs arbitration (attaching home first and then promoting reuses the same arbiter) | does not arise |
| Change footprint | a promotion path in the adapter | **just swapping `SlotAmiId`**. The adapter is unchanged |

- ⚠️ **Delete `/var/lib/ecs/data/*` before baking.** Leaving it makes **every** instance from this AMI
  believe it is already registered — exactly the trap already hit in decision 3-1.
- ⚠️ **Do not bake with `af-role=slot`.** The CP builds its world from that tag, so somebody's home would
  land on the box being baked. The script uses `af-role=bake`.
- **Staleness is handled exactly as for the golden (decision 9).** `af-image` is stamped on the AMI and the
  CP reconciles it and keeps saying so in the slots tab. Forgetting does not break anything, only slows it
  down, and there is nowhere else to notice. The reconciliation reads **from the instance's ImageId** (so
  that merely fixing the template is not reported as "improved"). On a mixture, it reports **the side still
  paying for the pull**.
- **The adapter does not choose the AMI.** The launch template does; the CP only reports.

### 18-1. The CP's task role had no snapshot permissions (fixed at the same time)

Found while adding `ec2:DescribeImages`. `Ec2SlotPool` in `20-platform.yaml` has
**no `DescribeSnapshots` / `CreateSnapshot` / `DeleteSnapshot` at all**. On a real deployment, hibernation,
spares and the golden would all fail, and since `createHomeVolume` first looks for the person's own
hibernation snapshot, **a new user's Start would not work at all**.

⚠️ **Why the real-hardware E2E did not catch it matters**: the harness runs with **the deployer's
credentials**, not the CFN task role. **"It worked on real AWS" is not "it worked with production's
permissions."** From now on this distinction is part of the practice of hardware verification.

**Addendum 2026-08-16 (the hole was closed)**: the harness now **extracts the policy of `CpTaskRole` from
`20-platform.yaml` verbatim**, creates a role with the same permissions, and runs **the product side only**
under that role (the test's own checks and cleanup stay on the deployer). The lifecycle E2E passed under it,
and the added snapshot permissions were confirmed to work on real hardware (docs/64 §64.23).
⚠️ **Not transcribing the policy by hand** is the crux. The moment you transcribe it, it stops being "the
permissions the template grants" and becomes "the permissions we think it grants", letting the same hole
through again.

## Decision 19 — do not bake into the slot AMI (withdrawing decision 18, from measurement)

Running `bake-slot-ami.sh` for real and standing up slots from the baked AMI, measurement showed that
**the aim was achieved and yet end to end it got slower** (a production-equivalent harness: NAT, two AZs,
m7i.large, compared within one session).

| | Plain ECS-optimized AMI | **The baked AMI** |
|---|---|---|
| A new user's Start (empty home, cold slot) | **144.0s** | **191.7s / 179.0s** (two runs) |
| Image pull at task start | 31.8s (decision 1) | **0.185s** (measured from the ECS agent's log) |
| Boot → registration with the ECS cluster (A/B, started simultaneously) | **21s** | **77s** |
| systemd's breakdown | loader 0.9s / initrd 1.0s / userspace 37.7s | loader 6.1s / initrd 8.9s / userspace **53.1s** |

**The reason is how an AMI is made.** A custom AMI's root is **lazily loaded from a new snapshot** (fetched
from S3 on first access) and is not warmed by wide use the way Amazon's ECS-optimized AMI is. And baking
adds real data (root usage 2.9G → 5.6G), so **there are more cold blocks**. It did not narrow on the third
machine either (77s to registration). The 30 seconds saved on the pull are paid back at boot, 30–56
seconds of it.

- **Stop.** `SlotAmiId` is faster left as the parameter for the plain ECS-optimized AMI.
- **Remove the implementation entirely** — `bake-slot-ami.sh`, the CP's `af-image` reconciliation
  (`readSlotAMI`, `ec2PoolStatus.SlotAmi*`), the Console's "slot image" panel and wording,
  `ec2:DescribeImages` on the CP task role, and the harness's cleanup. **"Mark it deprecated and keep it"
  is not adopted**: a feature not used by default is never run and rots quietly — and this time three
  errors in the script showed up **only on a real run** (the same shape as [[scratch-disk-inert-default]]).
  The record of the judgement stays in the docs and this ADR, so anyone who needs it can retrieve it from
  git history.
- **The only remaining reason for the effect is not speed** (not going to a registry at task start =
  independence from an ECR outage or pull throttling). That is not this ADR's reason for adopting, and
  **this pool does not terminate instances**, so the occasion to create a new slot is itself rare.
- **Startup time is not a reason for adopting in the first place** (decision 12). An optimisation for
  startup time that makes startup time worse has no reason to stay.

⚠️ **This too is an instance of "it does not run until you run it".** The script itself had two errors that
only appear on a real run (`--min-count/--max-count` do not exist in CLI v2; `rm -f /var/log/ecs/*` fails on
a directory — right after pulling 2.6GB). And **`SlotAmiId` takes an SSM parameter name, not an AMI ID**,
while the script and the README both told you to pass an AMI ID (i.e. the deploy would fail as written).
**Nobody thought any of the three were wrong at the time of writing.**

## Decision 20 — quarantine a slot that could not mount home

It happened on real hardware (docs/64 §64.24): the processes of a workspace whose home had been ripped away
fell into **uninterruptible sleep**, XFS froze trying to write back to a device that had gone, and the kernel
**kept holding the dead volume's NVMe namespace**. As a result the next volume attached did not appear in
`/dev` at all and `af-mount` failed with "no device". **And since the slot was still free, every subsequent
Start went there and failed the same way** — one machine's kernel jam silently became everybody's outage.

- **A slot that fails to mount is re-stamped `af-role=quarantined`.** Every pool query filters on that one
  tag, so **one write** removes it from the free list, from the cap's arithmetic and from placement, all at
  once. No state is added on the CP side (ADR 0012).
- **Detach home and drop the claim too.** So that the person's next Start can go to a different slot
  without waiting out the claim TTL.
- **Stop the box.** Do not leave a box that cannot accept tasks billing by the hour. Terminating is left to
  the operator's judgement (this adapter has no `TerminateInstances`, §64.22.1). **Note that on real hardware
  this stop actually completed the stuck detach** (stopping → stopped returned the volume to `available`).
- **Keep it on screen.** It is removed from the pool's counts but not from the table — a box that is still
  billing disappearing from the screen is a bill the operator cannot notice. The reason and the time are
  kept in tags too.
- Whether it can be repaired is outside the CP's remit (the kernel is jammed). **All it can do is stop it
  taking others down** — that is what quarantine is.

## Decision 21 — size stays "one axis, memory". The instance type is not chosen; **the box it lands on is displayed as a result**

The same settings UI faces four runtimes (docker / native / ecs (Fargate) / ecs-ec2). Putting `ecs-ec2`'s
vocabulary (`m7i.xlarge`) in there adds an item meaningless to the other three. Conversely, as it stands
**two of the three axes on screen are lies on `ecs-ec2`** (below). Of three options we take (c) — **change
nothing about the storage form (three independent numbers, ADR 0044 decision 1); have the runtime declare
what those values become, and have the screen say exactly that.**

**First, the five discrepancies settled by reading the implementation** (the starting point of the design;
details in [docs/64](../log/64-ec2-persistent-workspace.md) §64.27):

1. **The CPU setting is discarded on `ecs-ec2`.** `fargateSize()` exists only on the Fargate path, and
   `slotTypeFor(memBytes)` looks only at memory. CPU does not enter the task definition either. **"Workspace
   CPU" on screen is an input field that does nothing on this runtime.**
2. **Memory is not a cap.** An EC2 task has only `memoryReservation: 512` (soft) with no hard limit
   (decision 8: one person per box). `mem_limit` is **a "requirement" for choosing a box**, not a cgroup
   cap. Someone who entered 4 GiB can use **the whole 8 GiB** of an m7i.large.
3. **The disk axis means a different thing.** On Fargate it is the working disk (ephemeral, lost on stop),
   but on `ecs-ec2` `homeGiB()` means **the persistent home's EBS size**. The on-screen note said "the
   working disk is lost when stopped; only home persists" — **the opposite of the truth**.
4. **The disk value only takes effect when home is created.** `ModifyVolume` is not in this adapter (a
   comment says "it can be grown online", but that is a property of EBS, not of the implementation).
   Changing an existing user's value does nothing. **And EBS cannot be shrunk.**
5. **The member detail's running indicator is always "stopped" on ECS.** `containerStats()` calls
   `docker inspect`, so on ECS variants where the CP has no docker it always returns `running:false`. As a
   result **"force stop" can never be pressed** (`disabled={!running}`). The same on Fargate.

**The two options not taken**:

- **(a) Let the instance type be chosen directly.** It would make tenant admins choose EC2 model numbers,
  and it cannot be displayed on the other three runtimes. Moreover **the choices are already closed by the
  operator via `AF_ECS_EC2_SLOT_TYPES`**, so the freedom the user has is only "which rung of the ladder" —
  and that is expressible as memory. (**In the operator-facing screen, the slots tab, the model numbers are
  shown** — that is where AWS's vocabulary is correct.)
- **(b) Make abstract sizes (S/M/L) the storage form.** S/M/L **already exist as presets**
  (`WS_SIZE_PRESETS`). Promoting them to the storage form would collapse docker's byte-accurate `--memory`
  and Fargate's 74 combinations into five names — which is exactly why ADR 0044 decision 1 chose independent
  numbers, and there is no reason to overturn it. **Presets stay "a presentation of valid combinations".**

**The specification of the adopted option (c)**:

- **The runtime declares the semantics of size.** `SizingProfile()` is added on the same optional interface
  as `WorkspaceImage()`, and `GET /api/admin/workspace-sizing` (available to tenant_admin) returns: whether
  the CPU axis has any effect, whether the memory axis is a "cap" or a "requirement", whether the disk axis
  is the working disk or home, the defaults, and — on `ecs-ec2` — the slot ladder. **The screen decides its
  wording and its fields from this declaration alone** (the same pattern as the existing use of `hasPool` to
  decide whether to show the hibernation and spare fields).
- **Do not show the CPU field on `ecs-ec2`.** Do not leave an input field that does nothing. But **send back
  the value that was read when saving** (so that hiding the field does not, as a side effect, zero a value
  configured for another runtime — a shape already hit with the disk field).
- **The memory field says "requirement" and shows the box below it** (e.g. `→ m7i.large (8 GiB, dedicated)`).
  **0 (unset) means the smallest slot** — different from Fargate's "0 = the deployment default" — so that is
  stated too: "0 = the smallest slot (m7i.large)".
- **Presets are built from the ladder.** On `ecs-ec2` only, the slots' memory values are listed instead of
  S/M/L… (8 GiB / 16 GiB / 32 GiB). Do not recommend an intermediate value that does not exist.
- **The disk field says "home (persistent)" and states that it only takes effect at creation and cannot be
  shrunk.**
- **The ladder's vCPU is declared by the operator.** `AF_ECS_EC2_SLOT_TYPES` is extended to
  `type:memMiB[:vcpu]` (backwards compatible; the vCPU is not displayed when omitted). Adding
  `ec2:DescribeInstanceTypes` to ask AWS is not adopted — **adding one IAM permission to the CP's task role
  purely for a display** is not worth it against having the operator write one word they already know, and
  the latter can be verified without real hardware.
- **The member detail's running indicator comes from `State()`** (discrepancy 5). When docker's cgroup read
  returns nothing, ask the runtime again. This ends the state where "force stop" cannot be pressed on the
  ECS variants.

## Decision 22 — idle stop applies to "free slots" too, through the same single timer (a supplement to decision 11-2)

**Decision 11-2 only stopped slots dormant with a home attached.** The sweep loop's sleep check lives inside
`sweepVolume`, i.e. **inside a walk rooted at `af-role=home` volumes**, and **the moment a slot is
`releaseSlot`ed it leaves that walk**. After that no path issues `StopInstances` (`quarantineSlot` is only
for faulty boxes, and `sweepGhostInstances` merely deregisters container instances **whose EC2 instance is
already gone**).

⚠️ **Hit on a real deployment** (the development deployment, 2026-08-23): **three `m*.large` machines with
zero tasks, running for over 24 hours**. In 24 hours of logs there is **not a single** `sleep` record. The
paths that hit it are (a) eviction, (b) a size or class change, (c) `Destroy`, and (d) **when the golden's
seed/probe finish**. The three machines on the development deployment were residue from (c) and (d), and
**they were burning ~$95/month each right next to `Ec2SlotSleepSec`'s description saying "~$9.6/month"**.

1. **Add a slot-rooted walk** (`sweepFreeSlots`). It looks at instances with `af-pool` plus `af-role=slot`
   that are running, and stops those with **no home attached, no live claim, and zero ECS tasks** after
   `slotSleepAfter`. Because it queries `af-role=slot`, **quarantined boxes are naturally excluded**.
2. **⚠️ The `taskENIsAttached` guard must apply to the free path too.** Decision 3-3 says "stopping with a
   task ENI attached brings it back MULTI-ENI and silently loses the public IPv4 and egress", and that has
   actually been hit on real hardware. **Free slots are in the same window.**
   ⚠️ **"No home" is not the same claim as "nothing is on it".** There are paths where a task runs without a
   home, such as the baker's probe, so **ask both the EC2 side (volumes) and the ECS side (task count), and
   do not stop if either says "in use".**
3. **"Free since when" lives in the instance tag `af-slot-idle-since`** (RFC3339, UTC). Keeping ADR 0012
   (state lives on AWS) **leaves nowhere else** — a free slot has no home to write `af-idle-since` on.
   **It is written by `releaseSlot`** (the only place that knows the exact time), **and the sweep loop
   re-stamps it if missing** — the same "the actor writes it, the sweep loop repairs it" division as
   `af-idle-since`. **It is deleted when taken** (so a shorter-than-one-sweep occupancy does not leave the
   previous free time behind).
   ⇒ The answer is the same across a CP restart and across the two replicas that overlap for 51 seconds on
   every deployment (ADR 0053), and `StopInstances` is idempotent so a double call is harmless.
4. **Do not keep a single warm free box** (extending decision 11-4's "no pre-warming" directly).
   Measurement (docs/64 §64.17.5) gives **a hot free slot 43.2s / waking a stopped slot 110.1s / growing the
   pool 135.4s**. **Keeping boxes as stopped already buys "not paying the 135s"**, and leaving them running
   buys only **67 seconds** at a price of **$95 versus $9.6 a month**. The right way to put it is that the
   implementation so far was **unlimited pre-warming with nobody deciding the count**. A deployment that
   wants it can keep every machine warm with `Ec2SlotSleepSec=0`.
5. **Fix `Ec2SlotSleepSec=0` to mean "never sleep".** The description always said so, but the
   implementation was `idle < slotSleepAfter`, so **0 meant "stop at the first sweep" — the exact
   opposite**. It is aligned with `hibernateAfter`'s 0=OFF and, like §64.26's `AF_WS_IDLE_TIMEOUT=0`,
   **reads an operator's explicit off as off**.

6. **Both sides of the sweep loop treat "starting (a live `af-claim`)" the same** (docs/64 §64.31.6). The
   free side looked at it from the start, but **the home side (`sweepVolume`) only deleted expired claims and
   never looked at live ones**. `Start` runs `clearDormancy` first and `upsertService` last with the mount in
   between, so a sweep in that window (20–120 seconds; ⚠️ this originally said "the mount's SSM round trip,
   10–30 seconds", but measurement puts the mount at 2–4 seconds and most of the window is instance start
   plus ECS registration and placement — docs/64 §64.38) **re-stamps `af-idle-since` on a starting
   workspace**. It does not cause a premature stop (the clock goes backwards), but **the mark survives the
   start**, the operations screen shows a running workspace as "idle", and it becomes a candidate victim for
   `evictLongestIdle`.
   ⚠️ Its placement is **after the hibernation branch** (a claim must not stop a capture part-way), and
   **expired claims pass through** (`claimLive` looks at `af-claim-at`; writing "return if the tag exists"
   would let a dead launch pin a slot forever).

**What was not done**: a second grace parameter just for free slots (`slotSleepAfter` suffices; a free
slot's warmth is **worth less** than an occupied dormant one, so making it longer would be backwards).
~~**Terminating** (this adapter by design has no `TerminateInstances` — discarding a box is the operator's
judgement, and discarding the image cache turns 110s back into 135s).~~
→ **Withdrawn (decision 23).** "Discarding a box is the operator's judgement" also meant **the root volume
keeps billing until the operator notices and deletes it by hand**. That was hit on a real deployment
(docs/64 §64.32).

## Decision 23 — a stopped slot is **terminated** once more time passes (the next tier after decision 22, 2026-08-26)

Decision 22 made free slots sleep too, but **there is nowhere beyond sleeping**. This adapter has no path to
delete a box, so **the number of retained root volumes grows monotonically and sticks at `Ec2MaxSlots`**.
Raising `Ec2MaxSlots` from 6 to 30 on the live `<prod-deployment>` was simultaneously signing up to "buying
30 roots indefinitely" (30 × 40 GiB × $0.096/GB-month = **about $115/month**). Three stopped slots were
**terminated by hand** to bring EBS back from 900 to 600 GiB.

**Add `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` / `Ec2SlotTerminateAfterSec` (default 0 = disabled), which
terminates a box N after it stopped.** The recommended value is 4h.

- **Stopping and terminating stop different bills.** Stopped costs $0 for compute, but the root ($3.84/month)
  remains until the box is gone. ⚠️ The root is `/dev/xvda` with `DeleteOnTermination: true` so **it cannot
  be deleted on its own** — "delete the root" *is* "terminate", and it is **one inseparable decision**.
- **What is lost is 25 seconds.** Waking a dormant box is 110s versus creating a new one at 135s (§64.17.4),
  because the 32s of cold pull the image cache saves is eaten by instance start 19s plus ECS re-registration.
  Under an operating policy of "slower is better than unusable", that is affordable.
- ⚠️ **No warm floor (a minimum warm count).** A box with a home attached is reported busy by
  `occupiedInstances` even when stopped, so it never appears in `freeSlots`, and **keeping it warm only helps
  the last N people**. Conversely, detaching home to make it generic means the next person pays wake plus
  attach plus mount = **115–117s** (⚠️ originally estimated at 123–143s using a 10–30s mount, i.e. about the
  same as a new one at 135s; replaced with the measured 2–4s, docs/64 §64.38), i.e. **taking 25 seconds from
  the owner to give 19 seconds to somebody else** — **a difference of 6 seconds**, not "nobody gets it".
  Even so the trade of giving up affinity is poor, and a fixed cost remains through the dormant period. The
  92 seconds worth buying are on **the running free box** side ($95/month), and that is a reconsideration of
  decision 11-4's "no pre-warming", not this decision.
- ⚠️ **Detach home with `releaseSlot` before terminating.** With `DeleteOnTermination: False` it is not
  deleted either way, but if the owner Starts within terminate's ~60-second window, `placeHome`
  **derives placement from the attachment** and reads the shutting-down box as "my slot", claims it, and then
  fails at `StartInstances`. `releaseSlot` is **anchored to the Start generation** so a racing Start falls
  back to re-mounting — `TerminateInstances` has no such undo.
- ⚠️ **Widen `sweepFreeSlots`'s instance-state filter to `running, stopped`.** While stopping was the last
  thing that happened to a box, `running` alone was correct, but adding the terminate tier means **a box
  disappears from the walk forever the moment it stops** — precisely the boxes we want to collect.
- ⚠️ **When terminating ourselves, `DeregisterContainerInstance` (`Force: true`) first.** As decision 3-2
  says, the registration remains `ACTIVE / agentConnected=false`, and **a ghost that looks ACTIVE satisfies
  placement constraints**. `sweepGhostInstances` is the repair path for "boxes that disappeared for other
  reasons"; there is no reason to leave the window open when we are the ones deleting it.

**As a result `Ec2MaxSlots` sheds one role.** It served as **A** the cap on concurrency, **B** the number of
retained roots, and **C** the eviction threshold, and A and B coincided only "because boxes are never
terminated". The steady-state box count now follows "activity plus the threshold", and **B falls away**.

**What was not done**: a warm floor (above). Separate thresholds for free and occupied (both answer the same
single question, "how many hours to keep a box", and splitting them only adds operational surface).
Automatically terminating quarantined boxes (`af-role=quarantined` is excluded from both walks — **the
evidence is deliberately kept**).

## Decision 24 — eviction takes only "boxes you can ride". But it **does cross tenants** (2026-08-26)

Two rough edges in decision 11-3 (eviction at the cap) found while working on decision 23. One is a bug, the
other an undocumented design judgement (docs/64 §64.33 / §64.34).

**(1) The type was not being checked (a bug; fixed).** `freeSlots` has filtered on instance-type from the
start, but `evictLongestIdle` filtered only on AZ and on itself. `placeHome` puts the home onto the box won by
eviction directly with `attachHomeWithRetry` (it does not re-query `freeSlots`), so `slotTypeMatches` is not
consulted either. The result: **an xlarge user takes a large and lands on it, and ECS keeps refusing placement
with `no container instance met all of its requirements`** — stuck forever at desired 1 / running 0. The same
symptom as decision 21's "observed across architectures on real hardware".

**It survived because it is only exposed at the cap** (below the cap `growPool` runs, and a new box is by
definition the right type). ⚠️ The fix is **making two copies into one** (`slotsOfMyType`) — as in §64.31.6,
the root cause is "the same judgement in two places, only one of which is right". A consequence is that
**the global longest-dormant is no longer necessarily the victim**.

**(2) It crosses tenants (left as is).** Tenant A's Start may reclaim tenant B's dormant slot. Forbidding it
produces the rule "tenant B cannot use a box tenant A is not using", which creates "members who cannot start"
at the cap — the one outcome the operator stated they would absolutely avoid. What binds a tenant is
`max_workspaces` (how many may run **concurrently**), not which physical box they land on. The victim's data
is not exposed (`releaseSlot` completes umount then detach; the root is "shared with the previous user" by
design anyway). ⚠️ **On a deployment where the sum of tenant caps exceeds the pool cap, this path surfaces as
"stealing from another tenant"**, but what should be closed is validation on the tenant-cap side, not a
restriction on eviction.

~~**What was not done**: terminating a wrong-type dormant box while pinned at the cap and rebuilding the right
type.~~ → **Implemented in decision 26.** "Failing explicitly" still leaves **people who cannot start unable
to start**, which is the one outcome the operator explicitly said to avoid.

## Decision 26 — if the cap is filled with boxes you cannot ride, clear one and rebuild (continuing decision 24, 2026-08-26)

Adding the type check to eviction in decision 24 turned "at the cap with every dormant box the wrong type"
from "silently stuck" into "explicitly failing". Diagnosis became possible, but **from the user's point of
view both mean they cannot start**. The policy is "slower is acceptable, unusable is not", so this must not
fail.

A tier is added after ⑤ in `placeHome`: `evictLongestIdle` → (on failure) **`makeRoom`** → `growPool`. It tries
in order of speed (moving 109.7s → clearing and rebuilding 135s plus a terminate), and **the last one became a
"slow success" instead of a "failure"** — that is the whole of this decision.

- **Terminate rather than take.** The instance type is not an attribute a running box can change.
  `evictLongestIdle` **has first refusal over every correctly sized box**, so not finding one there means the
  rest are "boxes that cannot be ridden however they are reused", and one of them is holding a slot.
  **The only way to free the slot is to delete the box** (as in decision 23, the root cannot be deleted on
  its own).
- ⚠️ **Do not filter by AZ (the opposite of eviction).** Eviction **reuses** the box and so is bound to
  home's AZ, whereas this **destroys a box to buy a slot**, and since `Ec2MaxSlots` counts the whole pool,
  **freeing a box in another AZ is worth the same**. Filtering would leave a requester stuck while a box
  could have been freed.
- ⚠️ **Do not gate it on `Ec2SlotTerminateAfterSec`.** That is a knob for **idle cost**, where 0 means "keep
  the box". This is about **people who cannot start**, and it only runs when the alternative is "the Start
  fails". The victim loses only the image cache (110s→135s next time) and not home (`releaseSlot` detaches
  first; `DeleteOnTermination: False`). And **the default is 0**, and **deployments with 0 are exactly the
  ones whose boxes grow to the cap and stick there** (decision 23) — a gate would leave it ineffective on
  precisely the deployments that need this path most.
- **Take an empty box before one with an owner** (it uses nobody's affinity, so it is free). Among owned
  ones, the longest dormant.
- **Do not take a box with a live claim** (a landing Start has no attachment yet, and the claim is the only
  evidence). **Do not take one with even one ECS task** (a task can run without a home — the bake's probe).
  **Do not consider boxes of your own type** (eviction has first refusal; destroying one would throw away a
  box that could have been reused, and rebuild it). Quarantined boxes stay out of both walks (decision 20).
- Right after terminating, `DescribeInstances` may still say `stopped`, so **wait until it drops out of
  `poolSize`** (`runSlot` re-reads the cap; without waiting, your own Start fails as "full" because of the
  slot you just freed).

**If there is not a single box that can be cleared, return eviction's original error as it is** — "the pool
is full" is the one sentence that points at what the operator can do. That `makeRoom` does not become "delete
any box at all" is pinned by tests. A stage was added to the launch dialogue too (`slot: making room`) —
**it is the longest wait in the product**, and falling back to something generic would leave the longest wait
the only one that does not name its reason.

## Decision 25 — reconcile the tenant caps against the pool cap. **A warning, not a refusal** (2026-08-26)

`setTenantLimits` validated only the terminal history retention days and the format of a time string; integer
quotas were **stored unchecked** (docs/64 §64.35). There are two ways it breaks, handled differently.

**(1) A negative number is refused (400).** Since 0 = unlimited, negative is not "a small cap" but **a cap
nobody can satisfy**. `max_workspaces = -1` makes `running >= limit` true before anyone starts, and that
tenant can never open a workspace again. A typo in a number field must not be something you discover through
a member's failed start.

**(2) Exceeding the pool cap warns (and still saves).** The capacity is **`Ec2MaxSlots` −
`bakeReservedSlots`(=2)** — a golden re-bake needs two slots at once, for the seed and the probe, and
distributing all of them means it can never bake again (the symptom being "new members' first start is slow",
noticed weeks later). The constant is shared with the baking side.

Why it is not a refusal:

1. **This endpoint cannot protect that invariant.** `Ec2MaxSlots` is a CP env, and **lowering it involves no
   API call**. A 400 that closes one direction only would be claiming a guarantee it cannot give.
2. **Oversubscribing is legitimate operation.** `max_workspaces` caps **concurrent** use, so deliberately
   exceeding it is right between tenants whose peaks do not overlap.
3. **A deployment already over the limit would be unable to edit any other field on this screen.** Saving
   unrelated settings would stop because of a condition they did not create.

⚠️ **0 means "unlimited", not "zero machines".** With even one such tenant the sum does not bind the cap, so
it is emitted as **a separate warning** from "exceeded" (`unbounded_tenants`).

⚠️ **The denominators differ.** `max_workspaces` counts *running/starting* workspaces, while `Ec2MaxSlots`
counts *boxes that exist*, and **a stopped workspace holds a box while counting against neither tenant's
allowance** (deferred return). So Σ ≤ capacity is **necessary but not sufficient**. Decision 23 narrows the gap
to "the last N hours" but not to zero. **Do not use one word for the two on screen either** — the cap field is
annotated "the number running concurrently", and the warning always carries this proviso.

It only applies **on runtimes that have a pool cap** (`slotCapacityReporter`). Elsewhere it is `ok=false`,
which means "there is no such question", not "everything is fine". There are two outlets, the save response and
the pool screen, and both **only carry it when there is a problem** (an "all fine" every time is not read when
it finally does appear).

## Decision 27 — put a time limit on "not registered yet". **If the box's OS dies, discard it and re-place rather than wait** (a production incident, 2026-08-27)

Decision 20 quarantined "a box that could not mount home". This time the same thing happened one step earlier,
**while ECS was being asked whether the box could accept a task** — in a form nobody could notice.

It happened on real hardware (docs/64 §64.40): something inside a workspace held several GB of anonymous
memory, and **with zero swap the kernel's only means of reclaim was to shave the page cache**, so every process
thereafter kept re-reading its executable pages. **The root volume holds only 8.0 GB of data, yet it did
39.34 GB per 5 minutes for 3 hours** — **re-reading the disk's contents 4.9 times every five minutes** (refault
thrash). The box's management stack (efs-utils' stunnel → the ECS agent → the SSM agent) **all died within 15
seconds**. All three EC2 status checks stayed `passed`, i.e. **from outside the box was healthy**.

⚠️ **The OOM killer never fired once** (`hung_task` is 0 too; `OOMKilled=false`). **It does not fire in this
state** — because reclaim keeps succeeding, by destroying the cache.
★ **Do not read "no OOM" as "not memory".** During the investigation we did read it that way and once wrote the
**opposite conclusion**, "a hard memory limit will not help" (the history is kept in §64.40.2).

**So, in addition to decision 20 (quarantine), containment is needed**: `MemBytes` (a per-workspace RAM cap)
already exists and is used on the docker runtime, yet **only ecs-ec2 leaves `Memory: nil` and does not apply
it**. With a cap, the eviction pressure would have been **confined to that container's cgroup** and the box's
management stack would have survived.
⚠️ But the "size" a user chooses is the box's size, so putting `MemBytes` in directly makes the cap equal to
the whole box and **leaves no headroom for the management stack**. It only means anything as
**"the box's memory minus the management stack's share"**. (That containment is out of scope for decision 27
and is raised as a separate decision.) And with all three layers behaving correctly, the result was **a
deadlock nobody could recover from**: ECS cannot stop a task whose agent is gone → the task ENI is never
detached → the sweep correctly backs off with "do not stop a box with an ENI" (decision 3-3) → Start reuses the
same box by affinity and waits forever for registration → **the ingress cuts it at 60 seconds and the user gets
a 504.** Four presses gave four 504s, and the CP's log says only `500 1m0.0s` (two sides of the same event).

- **Add ECS registration to the `deferred` check.** The homeless path looked at `running && registered` from the
  start, but the affinity-reuse branch looked only at EC2's `running`. `deferred` is the flag deciding "run it
  on the caller's thread", and Start is inside a 60-second ingress. **On a healthy pool a running box is
  registered, so this asymmetry is completely invisible in normal operation** — it only appears in an incident.
- **Once the grace (`AF_ECS_EC2_SLOT_LOST_AFTER_SEC`, default 5 minutes) has passed and EC2 still says
  `running`, the box is not slow but lost.** `pending` boxes are excluded — destroying a box that is still
  booting turns "a slow Start" into "a broken Start", and the cost of being wrong is asymmetric (waiting too
  long only costs a few minutes).
- **Decision 20's teardown cannot be reused.** That was written on the premise that "nothing is running yet and
  the kernel is alive". On a box whose OS is dead, every step quietly does the wrong thing — `DetachVolume`
  against a running box never returns, an ordinary `StopInstances` waits for an ACPI shutdown nobody is
  listening for, and **stopping with a task ENI attached loses egress via multi-ENI (decision 3-3)**.
  So the order is reversed and one step is added at the front: **tag → force deregister → wait for the ENI to
  disappear → force stop → a normal detach after it has stopped.** Force deregistering is permissible because
  one slot means one home means one workspace, so **what disappears is only the caller's own (already
  unreachable) task**.
- **Automate through re-placing home. The budget is one attempt.** Quarantine alone gives the user nothing here,
  unlike decision 20 (whose purpose was "stop it taking others down", with the person rescued by their next
  Start). Here the Start the person pressed is the thing failing, so we re-place and bring it back up.
  ⚠️ **The budget of one is the crux** — if the whole pool is broken for a reason this code does not
  understand, an unlimited budget would quarantine one box per Start and **turn "a degraded deployment" into
  "an empty deployment"**. After two boxes it gives up and returns to `stopped` (a state the user can retry
  from).
- **The claim is not dropped at quarantine time** (the only place this is the reverse of decision 20).
  `State()` looks at the claim and answers `starting`. Dropping it mid-re-placement would make the Console show
  `stopped` for a moment and invite a double Start. It is dropped when giving up, and if that is missed it
  expires on the claim TTL.
- ⚠️ **No state is added to the CP.** The grace is a poll count, the judgement is queries to EC2 and ECS, and
  the result is a tag — ADR 0012 as before.

⚠️ **The hole that remains is being unable to prove afterwards why memory ran out.** The slots have no host
memory metrics (neither CWAgent nor Container Insights is installed), and the journal is on tmpfs so dmesg and
any OOM killer record vanish the moment the box is stopped. Even this time, memory exhaustion remained
**an inference from the shape of exploding EBS reads with zero writes**.

## Decision 28 — **detach the workspace from the box**. The cap exists not to stop a runaway but to **stop the collateral damage** (2026-08-27)

Decision 8 said "an EC2 slot is used by one person, so the task reserves nothing", and decision 21 made size
"one axis, memory, with the box it lands on as a result". Both are right, but **as a consequence of the two the
workspace's container can take the whole box** (`Memory: nil`). The incident in docs/64 §64.40 happened there —
something inside the workspace held several GB of anonymous memory, and with zero swap the kernel's only reclaim
was shaving the page cache, so **the box's management stack (dockerd / containerd / the ECS agent / SSM / efs
stunnel) kept re-reading its executable pages and all died within 15 seconds.**

★ **What we fix is not "stopping the runaway".** A user making their own workspace heavy is normal use of this
product and is not to be stopped. **What must be stopped is only the collateral damage.**

- **Put a cgroup limit (`Memory`) on the container.** It works because *where the pressure goes* changes:
  reclaim on exceeding the cap happens **inside that cgroup**. The management stack keeps its pages come what
  may, and a runaway is OOM-killed inside the cgroup and **visible from outside as `OOMKilled=true`** (a
  diagnosis rather than a mystery).
  ⚠️ **Put it on the container, not the task** — per task it would include the Service Connect sidecar ECS
  injects, whose size is not something the product may predict.
- ⚠️ **Protecting the management stack with `MemoryMin` cannot be the foundation** (judged on real hardware).
  docker's cgroup driver is systemd, so **the container also lands under `system.slice`**, and protecting the
  slice protects the workspace along with it. Per unit would work, but **the ECS agent is itself a container**
  and so falls into a transient scope where a systemd drop-in cannot be applied. It can help, but it cannot be
  the foundation.
- **The reserve is one fifth of nominal (clamped to 1024–2048 MiB).** ⚠️ **"Subtract 1 GiB" is not enough** —
  an m7i.large nominally at 8192 MiB actually has only 7784 MiB of `MemTotal` (measured), and the reserve must
  absorb **both** that ~5% and the management stack's measured 1.2–1.4 GiB. The real figure is available from
  `DescribeContainerInstances`, but **the task definition is assembled before the slot's registration is
  confirmed**, so using it would mean reordering `launch()` — a precision that a thicker reserve absorbs.
  If a rung is too small to share, **do not apply a cap at all** (leaving that deployment as it was is better
  than creating an unusable workspace).
- **Make the size display honest.** Before the cap, "8 GiB" meant both the box and the workspace and was honest
  as one number. Afterwards they are different numbers, so **showing only the box promises the person memory the
  cgroup will not give them**. `usable_mem_mib` is added, and the Console shows **what they can use first, with
  the box in parentheses**. ⚠️ **Omit it on deployments with no cap** — there the box is the answer, and showing
  two would be the lie.
- **The means of noticing only works once the cap is in.** The agent already returns `mem_used` / `mem_max` /
  `oom_kill_total`, and **`mem_max` is implemented to be deliberately dropped when the cgroup is unlimited**
  (docs/63 §63.9's "do not fill an unmeasurable axis with 0"). So on ecs-ec2 **only memory had no
  denominator**. Adding the cap **lights it up with the wiring already in place.**
  ⚠️ And **monitoring has to live outside what it monitors** — during the incident the agent was frozen, so
  self-reporting was useless. With the cap it does not freeze, which makes self-reporting trustworthy
  information.
- **The operator-facing alarms live in the substrate (CFN), off by default.**
  ⚠️ **What was missing was not measurement but an alarm** — `VolumeQueueLength` jumped from 0.005 to 2.0 in the
  incident's first minute, and `EBSByteBalance%` fell from 99 to 70. **The metrics were all there; nobody was
  looking.** ⚠️ But **CloudWatch cannot scope this to the stack** (the dimensions are VolumeId / InstanceId, and
  Metrics Insights cannot filter by tag), so it covers **the whole account**. That is why it is off by default.
  **Having the CP's sweep read CloudWatch is not adopted** — it needs a permission on the CP task role and
  introduces "a layer that interprets AWS metrics" into the product.

## Impact

- **Implement it** (P0's scope is decision 10-2). Add `control-plane/runtime_ecs_ec2.go` and add the `ecs-ec2`
  profile to `newRuntimeFactory` in `control-plane/runtime.go`. **`runtime_ecs.go` (Fargate) is not changed.**
- `deploy/aws/ecs/cfn/` — the slots' launch template, the instance profile (SSM plus joining ECS), user-data
  (`af-mount` / `af-umount`, `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION`), and EC2/SSM permissions on the CP task
  role.
- `workspace/workspace-notes.md` — the description of the persistence model (home becomes EBS, and `/scratch`
  does not exist).
- [docs/62](../log/62-ecs-start-latency.md) §62.5 — revise (d)'s rejection reasons (done).
- [docs/63](../log/63-workspace-sizing.md) §63.5.5 — a link to the conclusion of "to be considered in a separate
  session" (done).

**Surfaces added by decisions 23–25 (2026-08-26)**

- `deploy/aws/ecs/cfn/30-ingress.yaml` — `Ec2SlotTerminateAfterSec` (decision 23, default 0) and
  `Ec2HibernateAfterSec` (default 0). ⚠️ For the latter, **decision 4's feature existed from the start but had
  no CFN parameter, so on an ECS deployment there was no way to set the deployment default** (hand-editing the
  task definition is erased by the next deploy). "Default 0" was not a default but the only value that
  deployment could hold. A tenant's `home_hibernate_after` has been settable in the Console from the start, and
  that needs no deployment (docs/64 §64.36).
- `deploy/aws/ecs/cfn/20-platform.yaml` — `ec2:TerminateInstances` on the CP task role. Without it, every sweep
  is an AccessDenied that appears only in the CP's log while the bill keeps growing.
- `control-plane/limits.go` — `poolBudget` (decision 25). The tenant caps and the pool cap live in **the
  database and env** respectively and know nothing of each other, so the reconciliation can only live in the CP
  layer.

**Surfaces added by decision 27 (2026-08-27)**

- `control-plane/runtime_ecs_ec2.go` — `slotLostAfter` / `errSlotLost` / `abandonLostSlot` / `converge`'s retry
  budget. **No CFN parameter was added**: it is in the same family of "safe-side timeouts" as `CLAIM_TTL_SEC`,
  `WAIT_SEC`, `SWEEP_SEC` and `GHOST_AFTER_SEC`, a different lineage from the capacity and cost knobs
  (`Ec2SlotSleepSec` and friends). Enabled by default.
- No additional CP task role permission is **needed** — `ecs:DeregisterContainerInstance` has been there from
  the start for decision 3-2.

**Surfaces added by decision 28 (2026-08-27)**

- `control-plane/runtime_ecs_ec2.go` — `hostReserveMiB` / `workspaceMemCapMiB` / `slotRungFor`, and
  `container.Memory` in `buildTaskDef`. `AF_ECS_EC2_HOST_RESERVE_MB` = `auto` (default) | `off` | `<MiB>`.
  ⚠️ **An unreadable value falls back to auto** — this knob decides how much memory the user gets, and a typo
  must not be the reason "the cap disappeared" or "it will not start".
- `control-plane/workspace_sizing.go` / `console/.../adminShared.ts` — `usable_mem_mib` and `slotMemLabel`.
  The i18n key is `admin.ws_slot_usable`.
- `deploy/aws/ecs/cfn/40-ec2-pool.yaml` — `PoolAlarmEmail` (default "" = no alarms) plus an SNS topic and two
  alarms.
- ⚠️ **The cap goes into the task definition's fingerprint**, so the first Start on a deployment that changed
  the reserve registers a new revision and replaces the running task (the same behaviour as changing the image
  or the env).

## Decision 29 — cleaning up reserved workspaces follows **tags**, even with no database row (a real deployment, 2026-08-28)

Decision 9's bake (seed → snapshot → probe → promote) finishes by destroying the **workspace rows** of the seed
and the probe. On a real deployment that cleanup was missed (docs/64 §64.29.5) — the golden was published, yet
the seed's ECS service (desired 0) and its 50 GiB home remained on AWS, and **not one line appeared in the
log**.

★ **The cause is that the only entrance to cleanup was the database.** The things on the AWS side can be found
uniquely by tag (`af-workspace` / `af-tenant=af-golden` / `af-role=home`), yet cleanup can only act when it can
resolve membership → a workspace row. **The moment the row is gone, the tagged entities become findable by
nobody.** Since the row and the entity can break independently, a cleanup reachable from only one of them
creates a permanent leak.

- **Log every early return in `destroy`.** "There is no row (`!ok`)" and "it could not be read (`err`)" both
  returned silently, so **"cleaned up and there was nothing"** and **"could not clean up"** were identical in
  the log. It runs every tick, so a repeating failure is emitted once while the message stays the same.
- **When the row cannot be fetched, clean up by tag (`sweepOrphans`).** Limited to the reserved names
  (`af-golden-seed` / `af-golden-probe`, with the architecture suffix), the service and home are found by tag
  and deleted.
  ⚠️ **The drift sweeper cannot be trusted with this** — it walks homes by tag but **never deletes**. A
  detached home is indistinguishable from "a stopped workspace", and the only thing that can distinguish it is
  the baker, which holds the reserved names.
- **Safety is guaranteed in two tiers.** The caller confirms "a reserved name plus no row in the database", and
  the cleanup side refuses **a service carrying a task** and **a home that is attached or has a snapshot in
  progress**. EBS will happily let you delete a snapshot's source, so the latter is **our refusal**, not an API
  constraint.
- **Do not recreate under the name of a service being deleted (`DRAINING`).** Neither `UpdateService` nor
  `CreateService` works in that state, and throwing the latter gives `Create service is not idempotent`, which
  **reads as a bug in the caller**. It says "the previous service has not gone yet" and makes the existing retry
  wait.

⚠️ **A general rule**: when the entity is on AWS and the name is in the database, **a cleanup reachable only
from the database turns "only the row was deleted" into a permanent leak** — and while it leaks, the log looks
exactly like normal operation.

Code: `control-plane/golden_bake.go` (`destroy` / `sweep` / `warn`),
`control-plane/runtime_ecs_ec2_golden.go` (`sweepOrphans` / `snapshotInProgress`),
`control-plane/runtime_ecs_ec2.go` (`DRAINING` in `upsertService`).

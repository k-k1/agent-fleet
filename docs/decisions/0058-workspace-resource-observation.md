# 0058. Measuring a workspace's resources is one thing — reading your own cgroup from inside — and the Runtime interface does not grow

English | [日本語](0058-workspace-resource-observation.ja.md)

- Status: **adopted and implemented** (2026-08-25). The design and the background are in [docs/63 §63.9](../log/63-workspace-sizing.md#639-リソースの実測値はランタイムを問わず中から読む2026-08-25).
- See also: [0044-workspace-sizing.md](0044-workspace-sizing.md) (the decision on **specifying** the three axes; this ADR is the **observing** side of the same three) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 21 (the same three axes mean different things per runtime; disk = the persistent home's EBS) /
  [0029-usage-accounting.md](0029-usage-accounting.md) (the precedent for the same "the CP asks the Agent" shape)

## Context

"Workspace resources" (memory / CPU / disk) on the member detail were **all three showing "–"** on an
`ecs-ec2` configuration. The running indicator did appear, so as a screen it read "it is running and
nothing can be measured".

There is exactly one cause. `containerStats` in `control-plane/metrics.go`

1. looks up the container ID with `docker inspect`,
2. reads the host's `/sys/fs/cgroup/system.slice/docker-<id>.scope`, and
3. for disk, runs `du -sb <dataDir>/home` against the CP's local filesystem

— **a way of reading that presumes the CP and the workspace are on the same host**. An ECS task has
neither the docker binary, nor the target cgroup, nor home's path. It is the same on Fargate and on
`ecs-ec2`; it is not an `ecs-ec2`-specific problem.

Only `running` had already been dealt with (docs/64 §64.27, the force-stop button that could never be
pressed), but that merely overrode it with `rt.State()`; nobody was filling in the three gauges.

## Decision 1 — put the source of the observations in exactly one place, inside the workspace

**Read `/sys/fs/cgroup` from inside the container.** Thanks to the cgroup namespace, `/sys/fs/cgroup`
as seen from inside is remapped to that workspace itself. So **whatever the runtime, memory and CPU
come from reading the same two files**. Disk likewise: `statfs` on the filesystem home sits on gives
usage and capacity in one system call.

This is not a new idea; there is a precedent that already reads this way:
`status.OOMKillCount` has long counted its own `oom_kill` like this (docs/27 §10.2-2). This adds axes
to that, with the implementation collected in `workspace/agent/internal/resources` and
`status.OOMKillCount` reduced to a thin delegate.

## Decision 2 — do not add `Stats()` to the `Runtime` interface

At first glance "it differs per runtime, so add a method to the runtime" looks right, but **adding one
produces no branching**. docker and native can already be read from the host side, and the three ECS
variants have no way to get a cgroup from an AWS API, so **all of them collapse into the same HTTP
call**. An interface where four of five implementations write the same one line is duplication, not
abstraction.

Instead `metrics.go` gains `workspaceStats(ctx, mgr, rt, state)`, in order of cost:

1. read the cgroup on the host (this completes it on docker / native; existing behaviour does not
   change by one byte),
2. if that comes up empty, confirm `running` with `rt.State()`, and
3. only while running, ask the Agent's `GET /workspace/stats`.

`Runtime` still has six methods (Start / Stop / State / Endpoint / Token / Name).

## Decision 3 — drop an axis that could not be measured, key and all. Do not fill it with 0

`cpu_pct: 0` (genuinely idle) and "CPU cannot be measured" are different facts. Collapsing them into a
zero value makes the screen draw the unmeasurable as 0%, so that **nobody can see it is broken** — as
this incident's "–" showed, a blank at least asserts an anomaly. 0% asserts nothing.

So the Agent's JSON uses a pointer per axis (`omitempty`), and the CP decodes into pointers too.
`oom_kill_total: 0` passes through as "a present zero" for the same reason.

## Decision 3.5 — read cgroup v2 primarily and v1 as a fallback

What this way of reading demands is not something in the image (`statfs` is a system call, and
`syscall.Statfs` goes through neither libc nor an external binary). What matters is **the version of
`/sys/fs/cgroup`**.

`ecs-ec2` is settled as v2 — the slots' AMI is pinned to the `amazon-linux-2023` ECS-optimized one
(`deploy/aws/ecs/cfn/40-ec2-pool.yaml:21`). Fargate, on the other hand, **is not passed a
`PlatformVersion`**, so we are not choosing what is underneath.

An implementation that reads only v2's filenames would, on a v1 host, produce the symptom "memory and
CPU silently show –" — **exactly the appearance this ADR was supposed to fix**. Falling back into that
over nothing but different filenames and units is not worth it, so each axis is read v2 first, then
v1.

⚠️ There are two traps on the v1 side. **"No limit" is readable as a number** (`9223372036854771712`;
it does not conveniently fail to parse the way v2's `"max"` does), so it is rejected by a threshold;
and **the unit is ns, unlike v2's µs**, so they are aligned. Tests confirm that missing either
produces a literal 8 EiB and 50000%.

⚠️ **Only the v2 side was measured on real hardware**; the v1 side stops at fixtures.

## Decision 4 — call `State()` once per tick

`ecs-ec2`'s `State()` is **a real API call (DescribeVolumes + DescribeServices) with no cache**. The
`/api/events` tick is four seconds, and `workspacePayload` already fetches State there. Adding a second
State would simply double the AWS calls per subscriber. The value does not change within a tick, so it
is fetched once and passed to both.

Callers that have not yet fetched State — such as the admin screen's polling path — are passed a
**thunk** (`sync.OnceValue`) rather than a value. On a configuration where the host-side read succeeds
(docker), State never has to be fetched at all.

## Decision 5 — for disk, the denominator of the percentage is the measured capacity

`user_limit.disk_gb` is a setting, not a measurement. On `ecs-ec2` in particular it **only takes effect
at creation time** (changing the number later does not resize the EBS — ADR 0045 decision 21). So when
a measured `disk_total` is available, that becomes the denominator, falling back to the setting only
when it is not.

On configurations where the host-side `du` is available, `du`'s value (the size of the home tree
itself) continues to take precedence. It is the only number readable while the workspace is stopped,
and stocktaking needs it.

## Options discarded

- **Have the CP call the ECS API and pull CloudWatch metrics.** The granularity is coarse (one minute
  by default; Container Insights costs money), and on docker configurations it would still be a
  different path. "The same tile on the same screen means different things per configuration" is the
  thing this surface must avoid most.
- **Walk the disk with `du` on the Agent side too.** It gets more expensive the bigger home is, which
  is why the CP side caches for 60 seconds. From inside, home is your own volume, so one `statfs` does
  it — and on `ecs-ec2` it even gives **the capacity** (`du` has no denominator).
- **Ride the gauges on `GET /healthz`.** Making the liveness response heavier changes what a timeout
  means when monitoring is slow. It gets its own endpoint.

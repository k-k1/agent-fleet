# 0053. Make the CP image a two-architecture index and let **the operator choose** the running architecture (redundancy is separated out as a different matter)

English | [日本語](0053-cp-arch-and-availability.ja.md)

- Status: **adopted** (2026-08-23). The record of the investigation is [docs/72](../log/72-cp-arch-and-availability.md).
- See also: [0044-workspace-sizing.md](0044-workspace-sizing.md) decision 3
  (**a feature shipped off by default might as well not exist** — decision 4 of this ADR defies that
  by saying "ship it off by default", so the reason has to be stated) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 8
  (`ImageTag` is shared by the CP and the workspace, i.e. **they cannot use different tags**) /
  [0037-registry-policy.md](0037-registry-policy.md) (images are distributed via a registry; the
  air-gap tar is a hand-off to one machine and is out of scope for manifest lists)

## Context

[docs/70](../log/70-slot-instance-classes.md) made it possible to choose the architecture of the box a
workspace runs on. The same question remains for the CP itself — **the CP is only built for amd64**,
so the option of putting Fargate on Graviton does not exist at all. Meanwhile there is not a single
architecture-dependent line in `control-plane/Dockerfile`; **it is purely a question of how it is
built**.

## Decision 1 — keep one tag and make its contents a manifest list

`ImageTag` is shared by the CP and the workspace and they cannot use different tags (0045 decision
8), which is where the idea of a tag per architecture dies. **With an index, both ECS and local docker
pull the right one for the host's architecture**, so the reference can stay singular.

## Decision 2 — build the CP by **cross-compiling** (do not compile under QEMU)

The `console` (Vite) and `build` (Go) stages are pinned to `--platform=$BUILDPLATFORM`, with
`GOARCH=$TARGETARCH` passed to Go. The only thing that goes through the emulator is the runtime
stage's `apt-get`.

- The Console's output is JS/CSS and **architecture-independent** — there is no point building it
  twice.
- With `CGO_ENABLED=0`, cross-building Go **requires no preparation at all**.
- The QEMU tax that [docs/70](../log/70-slot-instance-classes.md) §70.9.2 measured (about 5× real
  hardware for arm64 alone) came out at that figure **because the contents are I/O-bound**, as that
  same section states explicitly. **The CP is on the compile-heavy side it warns about**, so the
  workspace's numbers must not be reused.

✅ **Measured (2026-08-23)**: amd64 only **95 seconds** / two architectures by cross-compiling **166
seconds** (an increment of **+71 seconds**) / **664 seconds with the pins removed** (an increment of
+569 seconds). **Building it naively is 4.0×, or 8.0× measured on the arm64 increment.** Against the
workspace's +593 seconds, the CP is **+71 seconds**, so the expectation that **making the CP
multi-arch is cheaper than the workspace** held too. Without a mechanism that builds the counterfactual
alongside it, those eight minutes would have been written off as "that is just how it is".

⚠️ **This optimisation does not break anything if it fails.** Drop one pin and the resulting image is
still correct; the only difference is build minutes. So (a) the reason is written in the Dockerfile's
header, and (b) the measurement workflow also builds **the counterfactual with the pins removed** so
the difference is recorded as a number. **Do not let "we cross-compile" stay a belief.**

## Decision 3 — `WS_PLATFORMS` and `CP_PLATFORMS` are **independent**

They are not merged into one flag. The two images answer different questions — the workspace's arm64
because "a slot may be Graviton", the CP's arm64 because "we want to put the service itself on
Graviton". **A deployment that wants only one of them is perfectly normal**, and merging them
guarantees paying for the build time of the one you do not use.

## Decision 4 — `control_plane_arm64` is **on by default** (initially off; flipped once P3 passed)

**Ordinary releases now ship a two-architecture index CP.** `CpArch=arm64` can be used with **an
ordinary release**, not a specially built one.

⚠️ **`workspace_arm64` stays off, and that asymmetry is the substance of this decision.** They look
like the same binary choice but the prices differ by two orders of magnitude: **the CP is +71 seconds**
(because it cross-compiles — decision 2) and **the workspace is +593 seconds** (because it installs
per-architecture binaries and cannot cross-compile). 71 seconds is noise against a 90-minute timeout,
so **there is no reason to ask the operator.**

**The reason changed twice on the way to this default, so it is recorded:**

1. Initially off. The reason: "we have not measured the build-time tax".
2. We measured it (+71 seconds), so **that reason vanished** — but **a different, heavier reason**
   remained: turning it on would put **an arm64 surface that has never been started** onto the tag
   every user gets. **The same shape as [0045](0045-ec2-persistent-workspace.md) decision 9-1's "do
   not publish a golden whose startup has not been confirmed".**
3. **In P3 that surface came up on real hardware** (see the verification below), so both reasons were
   gone → on.

⚠️ **Point 2 is the crux.** Flipping it the moment point 1 was resolved would have meant shipping an
arm64 that "should work". **The question of the tax and the question of "have you checked?" are
different questions, and the latter settles later.**

⚠️ And leaving it off was not an option: it would have become 0044 decision 3 itself (**a feature
shipped off by default might as well not exist**).

## Decision 5 — state `RuntimePlatform` **explicitly**, even at its default value

`CpArch`'s default is `x86_64`, identical to today's behaviour, but it is written in the template
anyway.

Fargate fills in `X86_64` when `runtimePlatform` is omitted. **⚠️ But it fills it in when the task
starts, not at registration** — so `describe-task-definition` returns `null` and "which architecture
is this CP running on" is **written down nowhere**. Stating it explicitly also puts it into the task
definition's identity, so moving `CpArch` reliably produces a new revision.

⚠️ The EC2 launch type **does not share this default** (omitted, it stays `null`). It is the asymmetry
recorded in [docs/70](../log/70-slot-instance-classes.md) §70.8: "omitting it worked on Fargate, so
omitting it is fine on EC2" does not hold.

## Decision 6 — reject a mismatched combination **before deploying**

Applying `CpArch=arm64` to a tag with no arm64 manifest does not even produce a
`CannotPullContainerError` — the symptom is **`desired=1 / running=0`, unplaceable**, with no pull
error in the logs.

⚠️ Flipping decision 4 on **reduced this danger but did not remove it**. Releases from now on carry a
two-architecture index, but **releases before 0.10.0 are single amd64**, and a version built locally
with `docker build` is a single host-architecture image too (unless `CP_PLATFORMS` is passed).
**"It is a new release, so it is fine" is not a reason to skip the pre-check.**

A pre-check is added to `update.sh`. ⚠️ **It fails only when it can prove the problem** — with an index
it reads the architecture list and can be certain; with a single manifest it cannot read the contents,
so it fails only when arm64 is being requested. `AF_CP_ARCH_CHECK=0` disables it. **This is an update
pre-check, not a publishing gate**, so [docs/35](../log/35-packaging.md) §35.7.5's "a gate that did
not run has not passed" does not apply (applying it would stop an operator's update path over
differences in permissions or tooling).

## Decision 7 — redundancy is **out of scope for this ADR** (but the reality is recorded)

The CP is `DesiredCount: 1`, has no autoscaling, and its RDS is `MultiAZ: false` (not even a
parameter). **This is not an accident, but it had never been written down as a decision either**, so it
was recorded in [docs/72](../log/72-cp-arch-and-availability.md) §72.7.

⚠️ **"The CP is one instance" does not mean "the code may assume one instance".** With
`minimumHealthyPercent=100`, **two CPs, old and new, overlap for about 51 seconds on every upgrade**
(measured). That overlap is what makes the deployment zero-downtime.

⚠️ Even so, `DesiredCount` is not raised, because with two running permanently **specific things
break**: scheduled execution **advances the ledger after firing**, so two replicas can double-fire the
same slot (the code itself says "double delivery of the prompt"), and the GitHub device flow lives in
**process memory**.

⚠️ **And the weakest link is not the CP's instance count.** Even with two, both look at the same
single-AZ RDS. The order is "make `MultiAZ` a parameter → fix the two items above → expose
`CpDesiredCount`", and **doing only the last one first is the worst** (it adds a setting that reads as
redundancy while actually only adding a new failure mode, double firing).

## Verification on real hardware (2026-08-23, the development deployment) — decisions 5 and 6 were borne out

With `ImageTag=0.10.1-dev-d7e0173c` / `CpArch=arm64`, **an arm64 CP came up** and looked after
**both x86_64 and arm64 workspaces end to end** (the golden seed → snapshot → probe → publish ran to
completion for both architectures, and every slot was returned).

- ✅ **Decision 5's grounds appeared in reality.** Revision 18, before the switch, returned
  `runtimePlatform: null`, while the running task's attribute was `ecs.cpu-architecture: x86_64` —
  **production was running on a value written neither in the template nor in the task definition.**
- ✅ **A rolling deployment across architectures is not refused** (which had been a worry). An x86_64
  revision and an arm64 revision **coexisted for 51 seconds** and swapped over with no downtime.
- ✅ **Decision 6's pre-check was confirmed in both directions**: a two-architecture index passes, and
  applying `CpArch=arm64` to a single manifest exits 1.
- ⚠️ **The route that actually worked was the Cloud Map fallback, not Service Connect's alias.** But
  **the same log appears on the pre-switch x86_64 too**, so it is not a regression (a workspace's
  service is created after the CP task and so does not get onto the alias). **On this deployment the
  route to a new workspace is always the fallback**, so the constraint "it only works on routes that
  go through the shared Transport" should be read as **a constraint on the everyday route**, not as
  rare insurance.

## Impact

- Existing deployments **change nothing**. `CpArch` defaults to `x86_64`, the same value as Fargate's
  implicit default, and although `control_plane_arm64` is now on by default, **the index has an amd64
  surface**, so existing deployments are unaffected (both ECS and local docker pull the right one for
  the host's architecture).
  ⚠️ What does change is that **a release's CP image becomes a manifest list**, and a path that goes
  through local docker such as `release-ecr.sh` cannot carry that (fixed as part of decision 6).
- The native package (C/R) stays amd64 ([docs/35](../log/35-packaging.md) §35.3.1). The air-gap images
  tar stays single-architecture too — a manifest list cannot be `docker save`d (0037). `--save` and
  `CP_PLATFORMS` are made mutually exclusive.
- ⚠️ Because **`ImageTag` is shared by the CP and the workspace** (0045 decision 8), replacing only
  the CP still requires putting the workspace side on the same tag. Re-tagging inside ECR keeps the
  contents identical (digest match confirmed), but **the golden's `af-image` tag holds a reference
  string, not a digest**, so the CP's goldens were re-baked from scratch for both architectures (about
  10 minutes, two slots). Nothing was broken, but **we paid to recreate a byte-identical home.**
- Cost on arm64 is **−20.0%** (measured against the Pricing API, ap-northeast-1), but the CP is
  0.5 vCPU / 1 GB, so that is **$4.5 a month**. ⚠️ **Do not make this a story about saving money.**

# 0068. Move the container base to Debian 13 (trixie) — for Chromium's clock, not for rtk

English | [日本語](0068-debian-13-base.ja.md)

- Status: **accepted — ①–④ done and green on hosted CI, ⑤ and ⑥ outstanding** (2026-09-04).
  The measurements are recorded under "Order of work" below. The motivation for moving the
  base and the comparison of the five options (A–E) are in
  [docs/70 §70.9.3](../log/70-slot-instance-classes.md); the 🔴 correction there is what
  triggered this ADR.
- Related: [0018-container-browser-pane.md](0018-container-browser-pane.md) (the record of
  choosing Debian's `chromium` package — pinned down to the revision — over Playwright's
  distribution; this ADR's deadline comes from there) /
  [0026-kiro-agent-kind.md](0026-kiro-agent-kind.md) (why the musl variant was chosen on
  arm64) / [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (the
  persistent `~` and architecture swaps; the Python migration does **not** ride that
  self-healing path) /
  [0053-cp-arch-and-availability.md](0053-cp-arch-and-availability.md) (the CP two-arch index)

## Context

The Workspace and Control Plane images have been on `node:22-bookworm-slim` (Debian 12,
glibc 2.36) from the start, and moving the base came up exactly once — when rtk failed to
start on arm64 with `GLIBC_2.39 not found` (docs/70 §70.9.2). Of the five options compared
then, A (go to trixie) was rejected as "effective, but the price is large and unrelated to
rtk", and two prices were named: **Chromium drops from 151 to 150**, and **Python goes from
3.11 to 3.13**.

**Re-measured on 2026-09-04, the first one never existed.** The two sides of the comparison
were not the same: the bookworm figure came from the **security** index and the trixie figure
from the **main** index. Measured against the same index there is no gap (security carries
`152.0.7977.75` in both suites on both architectures; main carries `150.0.7871.100` in both).

At the same time the clock became concrete. Bookworm **left regular security support on
2026-07-12 and was handed to the LTS team**, with LTS running to 2028-06-30. The browser pane
is a product feature, so this question changed from "we'll do it eventually" to "by when".

## Decision

**Decision 1. Move — but not for rtk.** The motivation is Chromium's deadline (Decision 2);
arm64 rtk starting to work is a **side effect**. ⚠️ The distinction is worth preserving: if
rtk were the motivation, the right answer would still be B (build musl ourselves), which is
surgical and breaks nothing else. **Migrating the whole fleet to make one tool work on one
architecture remains the wrong trade.** Conversely, if this migration slips, rtk is not a
reason to pull it forward.

**Decision 2. The deadline is not the end of LTS (2028-06-30) — it is the day Chromium falls
out of `bookworm-security`.** Since [0018](0018-container-browser-pane.md) commits to Debian's
`chromium` package (revision-pinned, with the setuid sandbox verified), the browser pane's
lifetime *is* that package's lifetime. ⚠️ **That day arrives as a build failure, not as an
announcement.** As of 2026-09-04 Chromium is not on `debian-security-support`'s unsupported
list, but:

- In bullseye it was cut **before LTS even began** (Debian bug #1061268, 2024-01, stopping at
  `120.0.6099.224-1~deb11u1`).
- In bookworm it has already failed once — in DLA 4749-1 (2026-08-21) **only the bookworm
  builds failed and only trixie shipped** (the LTS team's answer: "possibly the new rustc
  version in bookworm").

**Do not read "LTS until 2028" as slack.** Starting the migration after the trigger fires
means doing the work that breaks every member's Python while carrying a Chromium whose
security updates have stopped.

**Decision 3. The Chromium version gap was not a price — it was a measurement error.** Record
the reason so it is not re-litigated: **a security index was compared against a main index.**
Generalised: **when comparing Debian versions, read the same index on both sides.** A security
suite carries only the current build and points at a different version than main, so reading
one side from security and the other from main **manufactures a version gap that does not
exist.** What remains is redoing the pin and the setuid-sandbox verification (the Debian
revision does change, `~deb12u1` → `~deb13u1`) — but **there is no version regression to pay
for.**

**Decision 4. The one remaining real price is Python 3.11 → 3.13, and it is handled by
"detect and tell, do not reinstall".** It is an ABI break against the persistent `~`. Measured:

- The contents of `~/.local/lib/python3.11/site-packages` do **not disappear — they become
  invisible** (3.13 looks at `python3.13/site-packages`). Extensions carry a
  `cpython-311-…so` ABI tag, so moving the directory across does not work either; a reinstall
  is required.
- The `#!/usr/bin/python3` launchers in `~/.local/bin` **remain and still start**, then fail
  immediately under 3.13 with `ModuleNotFoundError`. The symptom is "it worked yesterday",
  and the cause appears nowhere.
- A member's own `uv tool install` (`~/.local/share/uv/tools/`) breaks the same way. The
  venv's `bin/python` links to **`python3`, not `python3.11`**, so the link does **not**
  dangle — it silently starts 3.13 and fails to import. That is precisely the shape a
  `test -x` check passes.

The mechanism is the same as the architecture stamp in
[0045](0045-ec2-persistent-workspace.md) (stamp the Python major into `~`, report when it
changes), but **unlike the existing self-healing, which deletes and reinstalls what the
product installed, nothing is deleted here.** Three reasons: it would require network at
boot, it takes minutes, and it silently resolves different versions. The entrypoint's job
ends at printing **the list of orphaned distributions and the single command to redo them.**

⚠️ **This is not an architecture event.** Every member on amd64 crosses it too, once, at the
first start on the new image — so it cannot ride the existing arch-change path at all. It
needs its own announcement in the release notes.

⚠️ **"Do not reinstall" applies to this Python case only. The architecture case IS repaired
automatically** (`af-arch-repair`). Treating the two the same way was sloppy — they are not
equally hard.

| | Architecture change | Python major change |
|---|---|---|
| The fix | the **same version**, a different wheel / binary | that version **may not exist** for the new Python |
| Resolution | identical to what was there | **can silently become a different version** |
| Blast radius | only dists with compiled extensions (measured: **8** of 35) | **everything** (the whole directory goes invisible) |
| Detection | **exact** — the arch is in the `.so` filename (`cpython-311-x86_64-linux-gnu.so`) | trivial: a version change breaks all of it |

So the architecture case is an operation that reproduces the previous state exactly, which
removes the last of Decision 4's three reasons (network, time, different resolution). The
other two already have precedent: the JDK and node are reinstalled over the network at
startup today. ⚠️ **On a boot where both changed, the pip reinstall is skipped**
(`AF_REPAIR_PY=0`) — the same version may not exist for the new Python.

**Decision 5. Rejecting Amazon Linux (option E) still stands.** Re-measured, AL2023 still has
no `chromium` package and its glibc is 2.34 — **older** than Debian 12's. **If the base moves,
Debian 13 is the only candidate.**

**Decision 6. Moving kiro's arm64 back from musl to gnu is not mixed into this migration.**
trixie's glibc 2.41 should satisfy the gnu variant, but the musl variant works today and is
verified. Folding it in makes the change impossible to bisect when something breaks. **Do it
as a separate change if a reason appears.**

**Decision 7. Verification means "baked and started", and a green e2e is not enough.**
[e2e.yml](../../.github/workflows/e2e.yml) only builds the `BAKE_OPTIONAL_TOOLS=1` branch, so
**the lean branch (for the native rootfs) is never exercised by e2e** — and it contains
something that fails on trixie for certain (step ④ below). On arm64, QEMU only answers
"does it load".

## Order of work

Premise: **do not build images on the dev host** (it OOMs the whole fleet). Hosted CI is the
authority for real images.

| | Do | What it tells you |
|---|---|---|
| **①** | Swap the base tags, dispatch `dev-image.yml` with `platforms=linux/amd64` and `bake_agent_clis=true` | apt resolution, Python, everything baked. **Cheapest signal first** |
| **②** | The same with `linux/amd64,linux/arm64` | **Whether rtk aarch64 starts.** The Dockerfile already runs `rtk --version` after baking and deletes it on failure, so the image answers by itself |
| **③** | `gh workflow run e2e.yml --ref <branch>` | L1 smoke (pins, the Chromium revision, the setuid sandbox, the Japanese screenshot, two-page CDP) → L2 → L3 |
| **④** | One build with `BAKE_OPTIONAL_TOOLS=0` | **The branch e2e never reaches.** The t64 renames (below) surface here |
| **⑤** | Re-bake the golden snapshots on both architectures | Golden is per-architecture (docs/70 §70.6). ⚠️ Do not repeat §70.14.7, where golden was selected on image identity alone |
| **⑥** | Real Graviton hardware, if rtk on arm is actually wanted | ②'s QEMU only answers "does it load" (see Consequences) |

**Measurements for ①–④ (2026-09-04, hosted CI, all successful):**

- **① amd64** (run 33843064097): python `3.13.5-1`, git-delta `0.18.2-4+b1`,
  `Chromium 152.0.7977.75 built on Debian GNU/Linux 13 (trixie)`. The Dockerfile's own
  `test`s passed for the pin match and the setuid sandbox's `0:0:4755`.
- **② arm64** (run 33843699642, QEMU): 🔴 **rtk's aarch64-gnu build started.**
  `+ /usr/local/bin/rtk --version` → `err=rtk 0.47.0` — meaning the Dockerfile's "remove it
  and record why if it cannot run" branch was **not** taken (no `rtk-unavailable` file).
  glibc 2.41 ≥ 2.39 is doing its job. Chromium, Python and git-delta match amd64.
- **③ e2e** (run 33843817457): **the L1 smoke reported zero NG and `== smoke OK ==`**.
  `chromium 152.0.7977.75-1~deb13u1`, `tmux 3.5a`, `rtk 0.47.0`, all nine CLI versions,
  every `versions.json` key, the Japanese screenshot, the setuid helper being the only
  setuid executable, and two simultaneous CDP Pages all passed. L2 and L3 (ui-e2e) also
  succeeded. L4 (live-smoke) was skipped because it spends quota.
- **④ the lean branch** (run 33843815539, `BAKE_OPTIONAL_TOOLS=0`): succeeded. The six t64
  renames (`libasound2t64`, `libglib2.0-0t64`, `libatk1.0-0t64`, `libatspi2.0-0t64`,
  `libatk-bridge2.0-0t64`, `libcups2t64`) were confirmed in the log as actually resolved
  and unpacked. ⚠️ **This branch was previously unreachable from CI**, so
  `BAKE_OPTIONAL_TOOLS` was threaded through `release.sh` and exposed as a `dev-image.yml`
  input (default 1, so every existing caller is unchanged).

The tags to swap — **eight files, not four**:

- `workspace/Dockerfile` (`:8` builder, `:19` runtime), `control-plane/Dockerfile`
  (`:33`, `:43`, `:62`), `deploy/release/native/Dockerfile.afcp` (`:7`),
  `deploy/release/native/Dockerfile.console` (`:6`)
- `workspace/jvm.Dockerfile` — the base at `:7` **plus the literal suite name in the adoptium
  apt line at `:12`** (adoptium does publish a trixie suite, amd64 and arm64)
- `deploy/aws/ecs/harness/probe-agy-arm64.sh` (four places) — the probe **deliberately mirrors
  production's base**, so if it is not moved the check stops meaning anything
- **`NOTICE` (two places) is a licence document** — the GPL corresponding-source clause says
  `apt-get source` "against the bookworm suite recorded in the image". If it does not move
  with the base it **becomes false**
- `CHROMIUM_VERSION` → `~deb13u1`. ⚠️ **The comment in `workspace/Dockerfile` explaining which
  index to read is what produced the error in Decision 3**, so fix that sentence too, not just
  the suite name

🔴 **The t64 renames break the lean branch for certain.** Of the 21 Chromium runtime libraries
the `BAKE_OPTIONAL_TOOLS=0` branch lists explicitly, **six do not exist in trixie**:
`libasound2`, `libatk-bridge2.0-0`, `libatk1.0-0`, `libatspi2.0-0`, `libcups2`,
`libglib2.0-0` (all `…t64`). apt fails loudly, so it will not be a silent accident — but
**e2e never builds this branch**, which is why ④ exists.

## Consequences

- **git-delta becomes available.** trixie's main has `git-delta`. The "not in bookworm, so
  intentionally omitted" note in `workspace/Dockerfile` goes away; this moves from the cost
  column to the benefit column.
- **arm64 members are on track to get rtk.** rtk's aarch64-gnu build needs exactly two
  `GLIBC_2.39` symbols (`pidfd_getpid`, `pidfd_spawnp`), and trixie is 2.41; in ② a real
  arm64 image under QEMU ran `rtk --version` successfully (see the measurements above).
  ⚠️ But **what QEMU answered is "does it load and report a version"** — `pidfd_spawnp` is
  exactly the kind of syscall QEMU's user mode may not implement, and the path where rtk
  actually spawns a child has not been exercised. **Say nothing stronger than "should work"
  until real Graviton hardware (⑥) confirms it**, so as not to repeat docs/70 §70.9.4
  ("documented as supported" and "actually runs" are different claims). ⚠️ Likewise, do not
  retract §70.3.3's "arm members cannot use rtk" until ⑥ passes.
- **Nothing changes about pushing upstream on rtk.** Waiting on PR #3318 stands, and **this
  migration is not grounds to withdraw it as "solved"** — the missing musl variant is still an
  upstream gap for everyone else.
- **Every member pays the Python reinstall exactly once** (Decision 4). Not just arm64.
- ⚠️ **Unverified:** glibc 2.41 needs a seccomp profile that permits `clone3` and
  `faccessat2`. ECS (AL2023) and Fargate are fine, but **an old Docker on an on-prem compose
  deployment** is the classic failure. This cannot be confirmed from here, so **treat it as a
  minimum-Docker note in the release notes.**
- Versions that move up (all forward): git 2.39 → 2.47, tmux 3.3a → 3.5a, ripgrep 13 → 14,
  fd 8.6 → 10.2, bat 0.22 → 0.25, jq 1.6 → 1.7.1, subversion 1.14.2 → 1.14.5, fonts-noto-cjk
  2022 → 2024. ⚠️ One place parses the shape of tmux's version string
  (`workspace/agent/internal/sessionx/session_handlers.go`) — check it.
- **The native rootfs is unaffected.** `deploy/release/native/Dockerfile.tools` builds bwrap,
  git and zstd as static musl binaries on `alpine:3.22`, and the rootfs carries its own glibc.
- The baked MCP servers (cloudwatch-mcp, mcp-proxy-for-aws) live **in the image, not in home**,
  so a rebuild recreates them on 3.13, and both declare 3.13 support. ⚠️ However
  `mcp-proxy-for-aws` has **only a `test -x` check and no import check**, which cannot catch
  the "silently starts 3.13 and fails to import" shape from Decision 4. **Add an import check
  before migrating.**

## Options rejected

| | What it does | Why rejected |
|---|---|---|
| **B. Build rtk as musl ourselves** | Add a Rust builder stage producing `aarch64-unknown-linux-musl` | **Still the right answer if rtk were the motivation.** But the motivation is Chromium's deadline, and this does not address it. ⚠️ If B is ever done, **switch amd64 to our own musl build too** — building only one side means a different rtk runs per architecture |
| **C. Get upstream to ship aarch64 musl** | Wait for PR #3318 | **Effective if it lands, but with no guarantee of landing in time.** Untouched since 2026-08-22. The policy of not pushing further stands (a sixth issue and a "+1" comment are both pure noise) |
| **D. Do nothing (status quo)** | Keep shipping arm64 without rtk | Holds up for rtk, but **answers nothing about Chromium's deadline**. Per Decision 2 that deadline arrives without warning |
| **E. Switch to Amazon Linux** | Base on AL2023 | Decision 5. No Chromium package, and an **older** glibc (2.34 < 2.36) |

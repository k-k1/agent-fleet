# 0025. Auto-update for the host-resident `af` — staging is automatic, applying (the restart) is explicit

English | [日本語](0025-native-auto-update.ja.md)

- Status: **adopted and implemented**. The design is [docs/42](../log/42-native-auto-update.md).
- See also: [docs/35](../log/35-packaging.md) (packaging / install.sh / the `af` launcher) /
  [docs/34](../log/34-native-runtime.md) (the native runtime)

## Context

The native package (`AF_RUNTIME=native`) is used by keeping `af start` resident on a host such as
WSL2. Updating was manual — rerun install.sh, which swaps the version directory and re-points the
`~/.local/bin/af` symlink. Users want auto-update, "it just gets newer while I ignore it", but
native usually runs as a systemd **user** service, and automating the update naively runs into two
structural problems.

1. **A running af-cp does not switch over just because the disk was swapped.** Swapping the version
   directory and re-pointing the symlink leaves the already-started `af-cp` (and the console assets
   it has loaded) exactly as they were. **The new version only takes effect once the unit is
   restarted.**
2. **A unit cannot safely restart itself.** Calling `systemctl --user restart` from inside af-cp
   sends SIGTERM to itself (the main PID), and the restart command can be killed part-way through.

On top of that, this fleet has always cared about **not carelessly killing running agent
sessions**. A restart takes down af-cp and the workspaces under it (systemd stops the whole unit
cgroup), so an unconditional automatic restart is not an option.

## Decision

**Separate the two: fetching the update (stage) is automatic and on by default; applying it (the
restart) is explicit — from the Console, by hand, or when idle.**

- **Staging is contained in `af update`**: install.sh's logic is built into the `af` launcher.
  Resolve latest (the `releases/latest` redirect, so it does not depend on the API rate limit) →
  download → **verify sha256** (against the release's SHA256SUMS) → stage the version directory and
  atomically swap it → re-point `~/.local/bin/af`. **The running af-cp is untouched.** An
  `AF_VERSION` pin is honoured (never exceeded). `WS_DATA` is unchanged.
- **On by default means automatic staging from a timer**: if systemd user services are available,
  install.sh enables `agent-fleet-update.timer` plus a `.service`
  (`ExecStart=af update --yes`). The updater is **outside the target unit**, so it is not killed by
  the later restart (avoiding problem 2). Opt out with `AF_NO_AUTOUPDATE=1`. The timer goes **as far
  as staging** and does not restart.
- **Applying is explicit**: the CP gains `GET /api/update/status` (comparing the running
  `buildVersion` against the `VERSION` behind the symlink) and `POST /api/update/apply`. The
  Console (settings → environment) offers "apply by restarting" and **warns about the number of
  running sessions** before restarting. Under systemd the restart runs **detached** as
  `systemd-run --user --collect systemctl --user restart <unit>` (so the restart completes even if
  we are SIGTERMed); in the foreground the launcher replaces itself with `syscall.Exec` (the symlink
  points at the new version). The unit name reaches af-cp via `Environment=AF_SYSTEMD_UNIT=%N` in
  the sample.
- **The rootfs cascades naturally**: when a package update brings a new `rootfs.json`, the first
  start after applying has af-cp fetch the new rootfs lazily (keeping the old one). Running
  workspaces stay on the old rootfs until they are next restarted.

### Options rejected

- **Restart automatically as soon as an update is detected**: the simplest, but it cuts off every
  running workspace/agent session without asking. That contradicts a stance that takes false-idle
  and session-kill seriously, so it is not adopted. The default stops at stage → notify.
- **In-place self-update (re-exec) inside the af-cp process**: an in-place exec of a systemd main
  PID is delicate to get right and fragile once the launcher, the console assets and the rootfs
  handshake are involved. Staging is external (timer/CLI) and applying is an explicit restart.
- **install.sh generating and enabling the main service too**: install having the side effect of
  starting af on the host by itself is too much. The main service is left to the README procedure as
  before, and only the timer (staging only) is on by default.

## Consequences

- The addition is the `af` launcher (the `update` subcommand, dist metadata, passing self-info
  through env), `build.sh` (generating `dist.json`), `install.sh` (the timer on by default, with an
  opt-out), the native README (`AF_SYSTEMD_UNIT=%N` in the systemd sample, plus an update section),
  the CP's `update.go` (status/apply, gated to native) and the Console's `EnvTab` (the update
  section). The existing start/reset/status are unmodified.
- **Native only**: the status/apply routes are registered only when the launcher passes
  `AF_SELF_LINK`. On Docker/ECS (updated by image) and dev builds they are not registered, so the
  Console hides them automatically.
- **Running against a pin**: put `AF_VERSION` in both the main unit and the update unit, and
  `af update` targets that version and then does nothing.
- **A deliberate limit**: because systemd stops the whole unit cgroup, applying (restarting)
  interrupts running sessions. That is exactly why applying is not automated and is limited to the
  Console with a warning, by hand, or when idle. Decoupling long-lived workspaces from the restart
  (a separate slice/scope) is future work.

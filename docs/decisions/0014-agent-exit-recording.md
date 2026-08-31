# 0014. Recording why an agent process exited — a pane wrapper, OOM attributed from our own cgroup, per-container read directly by the CP

English | [日本語](0014-agent-exit-recording.ja.md)

- Status: decided (2026-07-12); Phase 1 (CP) and Phase 2 (Agent) implemented the same day
- See also: [26-agent-exit-recording.md](../log/26-agent-exit-recording.md) (the design proper) /
  [0012-go-internal-refactor.md](0012-go-internal-refactor.md) (extracting `internal/status`)

## Context

Agents (claude/codex/opencode) are started with `tmux new-session -d <program>`, so their parent
is the tmux server. That leaves the Go side with no route to `cmd.Wait()`/`WaitStatus`:
termination is detected only by polling for "has the tmux session gone?", and **nothing was
recorded about why it died** (clean exit, crash, OOM kill). OOM on a shared host is a known risk
(the [host-oom-fleet-risk] note), and operators and users want to know **which session was killed
by OOM**. The CP already reads cgroup v2 straight from the host (`metrics.go`) but never looked
at `memory.events`, and docker's `.State.OOMKilled` was unused.

The three questions: how to catch the pane's exit status, at what granularity to attribute OOM,
and how not to mistake a deliberate stop for a crash.

## Decision

1. **Capture the per-session exit code with a pane wrapper** (the tmux-hook and sub-cgroup
   approaches were rejected). Append
   `; __af_ec=$?; workspace-agent record-exit '<name>' "$__af_ec"` to the pane program, and
   record the `$?` the shell picks up after the agent CLI exits (a signal kill is `128+N`). It
   does not depend on the tmux version and is contained in the program string.
2. **No "deliberate stop" flag** (demonstrated to be unnecessary). Verified on tmux 3.3a:
   **`tmux kill-session` takes down the wrapper's shell too, with SIGHUP, and record-exit never
   runs.** So a deliberate stop through Stop/Halt/Archive/Recreate is structurally never
   recorded, and a normal stop cannot be mistaken for a crash. On top of that, graceful signals
   (SIGINT/TERM/HUP = 130/143/129) are interpreted as `stopped` in the interpretation layer — safe
   twice over.
3. **OOM attribution is completed on the Agent side** (aggregating it on the CP was rejected).
   record-exit compares `oom_kill` in **its own container's** cgroup
   `/sys/fs/cgroup/memory.events` against **the baseline recorded when the session started**, and
   concludes `oom` only when the code is `137` (SIGKILL) *and* the counter has increased (no
   increase means `killed`). No round trip to the CP, and the judgement is made in the same
   context as the session ending.
4. **Exit information is persisted in a file separate from Meta** (`ExitInfo` in
   `internal/status`, keyed by session name). record-exit and the API handler write state
   concurrently, so a single-JSON Meta would have them clobbering each other. Split into a
   dedicated file for the same reason as the existing per-sid status store. Writing the baseline
   at start-up doubles as clearing the previous death record, so a resumed session starts clean.
5. **Per-container OOM is detected by the CP reading cgroup directly** (no rebuild required).
   `metrics.go` gains `memory.events.oom_kill` (OOM of a child process inside the container — the
   container survives, which makes this the one signal docker cannot give) and, for stopped
   containers, docker's `.State.OOMKilled`/`.ExitCode` (the whole container being OOM-killed),
   exposed at `/api/workspace/stats`. It takes effect immediately, even on running containers.
6. **Sub-cgroups (a cgroup per session) are deferred.** They would give precise per-session
   `memory.events` and a memory cap, but need cgroup delegation privileges and are heavy.
   Decisions 1–3 are enough for attribution.

## Consequences

- Phase 2 (Agent; needs an image rebuild): `record_exit.go` plus `status.ExitInfo`, the wrapper
  and baseline recording in `startSessionTmux`, the exit reason attached in `wireSession`, and
  cleanup on Stop/Archive/Recreate. The Console shows an abnormal exit (oom/killed/crashed) in
  the left pane as a warn chip with a tooltip.
- Phase 1 (CP; no rebuild required): `oomTracker` (flags `oom_recent` for 5 minutes across
  polls, with the first sample used as a baseline to avoid a false positive) plus inspecting
  stopped containers. The WsBar presents it as a crit on the memory tile and a warn on the state
  chip.
- Interpreting `exitReason`: `0` → exited / `137` + OOM → oom / `137` without OOM → killed /
  SIGINT/TERM/HUP → stopped / other signals and non-zero non-signals → crashed. Verified by unit
  tests and against real binaries.
- Rejected: tmux `remain-on-exit` plus the `pane-died` hook (the session stops being destroyed
  automatically, which then needs cleaning up); a deliberate-stop flag (unnecessary, since
  kill-session records nothing); aggregating OOM attribution on the CP (the Agent can do it from
  its own cgroup); sub-cgroups (not worth the privileges and complexity).
- Remaining: rebuilding the real fleet and looking at it on real hardware.

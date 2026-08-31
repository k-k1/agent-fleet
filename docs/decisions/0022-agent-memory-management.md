# 0022. Version agent memory in a git bare repo on the agent side, and move it between environments as a bundle

English | [日本語](0022-agent-memory-management.ja.md)

- Status: **adopted** (2026-07-27. Designed 2026-07-23; the four open points were settled at
  their default values). The implementation plan is [docs/39](../log/39-agent-memory-management.md).
- See also: [0010 (the internal git provider)](0010-internal-git-provider.md),
  `workspace/agent/cleanup_archive.go` (the gz safety net in cleanup — it has no design document of its own),
  `control-plane/runtime_docker.go` / `runtime_ecs.go` / `runtime_native.go` (the claude-config mount),
  `workspace/agent/routes.go` + `control-plane/routes.go` (the REST dual allowlist).

## Context

There are two local instantiations of the persistent memory agents accumulate: claude's
auto-memory (`projects/<slug>/memory/*.md`, on by default) and codex's memories workspace
(the .md files under `~/.codex/memories/` plus a derived-state sqlite; the feature flag is stable
but **off by default**, i.e. the mechanism exists but this fleet does not use it). Investigating
all eight kinds showed the rest have no local instantiation: opencode has no native
implementation (an upstream issue is open), the agy CLI has no confirmed first-class memory,
copilot (Copilot Memory) and cursor (the old Memories were removed; what exists now is for
Automations) are managed server-side, and kiro has no automatic memory (only steering .md plus a
derived-state knowledge index; global steering at `~/.kiro/steering/*.md` is a candidate root for
the future).

These memories sit somewhere that does not get erased, but **they have no history**. There is no
way to roll back a bad lesson or a bad rewrite, no way to name a date and see the state as it was,
and no way to take it to another agent-fleet environment. The only existing backup is an ops-layer
tar of the whole DATA_DIR, which has no per-person or per-project granularity.

## Decision

1. **The history engine is git.** All four requirements — viewing diffs, resolving a date to a
   point in time (`rev-list --before`), path-scoped restore (`checkout <rev> -- <dir>`) and
   single-file transfer (`git bundle`) — are covered by standard features, and it can connect to a
   future CP mirror (reusing 0010's bare + http-backend) with no change of format.
2. **The workspace-agent does the work, and the repo is a bare repo inside the claude-specific
   mount** (`/var/lib/af/claude/af-memory.git`). It looks the same from the agent on every runtime
   (Docker/ECS/native), and it does not run into the ECS constraint that the CP has no direct file
   access to user data. The CP does only REST proxying (the dual allowlist).
3. **No `.git` in the live tree — allowlist copy, then a staging commit.** The material is
   structurally limited to an allowlist of roots (claude: `*/memory/**`; codex: `memories/**` with
   `.git` and the like excluded), so there is no path by which transcripts, credentials or the
   derived-state sqlite get swept in. The agent itself is never shown that the repository exists.
   For codex the staging approach is also a hard requirement for avoiding interference, because its
   integration pipeline uses its own `~/.codex/memories/.git` as a diff baseline.
4. **A rollback is not a rewrite of history — it is one more restore commit on top.** A
   pre-restore snapshot is taken automatically before applying, so undoing an undo is always
   guaranteed. The scope is whole-tree or per-project for claude (memory is self-contained per
   project, so the index does not break either) and whole-workspace only for codex (its project
   division is by entries within a file). Codex's derived state (the sqlite and its own `.git`) is
   not restored; its diff-driven integration pipeline re-digests it as an external change.
5. **Transfer between environments defaults to a git bundle (full history)**, with tar.gz
   (latest only) alongside. An import is taken in as an independent lineage under
   `refs/imports/<ts>`, and applying it is "replace = a new commit" with per-project selection. We
   do not do a 3-way merge of .md, because semantic conflicts cannot be resolved mechanically.
   **5-b (added 2026-08-25): add "relocate" as a way to apply.** The default (replace) uses only
   the latest tree, so the past the bundle carried stays buried in `refs/imports` and gets pruned —
   there was no way to satisfy the most natural expectation of all, "move house, history and all".
   Relocate **re-points** main at that lineage (neither a merge nor a rewrite), so decision 4's "do
   not rewrite history" holds and the existing listing, diff and rollback features work on the
   other side's history as they are. The main that was swapped out is parked at
   `refs/premigrate/<ts>` rather than deleted. The scope is **fixed at whole-tree** — replacing
   only part of it would leave the history (the other side's lineage) and the live tree (a mixture)
   inconsistent, and the meaning of any later rollback would be impossible to explain.
6. **Memory roots are a declarative table, and v1 declares two: claude and codex.** codex is
   enabled automatically by detecting that `~/.codex/memories/` exists. Whether the fleet turns the
   memories feature on is a separate judgement; **P4 added the wiring for it** (a Console toggle for
   `features.memories` plus a `[memories]` seed that keeps the cost down), but the default stays off,
   exactly as it is in codex itself — enabling it costs tokens in the background, so the user
   chooses. kiro's global steering is a third candidate root (watch), opencode/agy are watched
   pending an upstream implementation, and copilot/cursor are out of scope because they are managed
   server-side with no local instantiation. That codex upstream is developing a feature to import
   Claude Code's memory layout directly (`external_agent_memory_import`), and that Gemini CLI v0.40
   moved to a fully local hierarchical .md memory, both support the soundness of "a general root
   design that treats an .md directory as canonical".

## Results (expected, and the constraints accepted)

- What we get: the full change history of memory (when, and which project changed), rollback by
  date or by history entry and per project, transfer between environments in a single bundle file,
  and an operation surface with an audit log.
- Constraints accepted: **it can only be operated while the workspace is running** (because the
  agent does the work). The original premise was that "the P4 CP mirror will resolve this", but the
  P4 investigation found that the internal git provider's authorisation **has no per-user ACL
  within a tenant** (`authorizeGitRepo` in `git_http.go`: read is open to every active member of
  that tenant). Mirroring personal memory as is would let colleagues clone it, which conflicts with
  this ADR's premise that memory is personal data and export is the person's own operation — so
  **the mirror was not implemented and was moved to a future track with preconditions** (either
  introduce an owner-only repository concept into the internal git provider, or build a per-user
  mirror area outside the ledger with a dedicated API; either is worth its own ADR). So **viewing
  while stopped is not possible for now.** Imports do not merge (selective replacement only).
  Codex's rollback granularity is whole-tree only. opencode/agy have no native memory and are out
  of scope; copilot is server-side and out of scope.
- Risks and their handling: an import is external input (a size cap, traversal defence and bundle
  verification are mandatory). An export may contain personal data (restricted to the person, with
  audit and a warning in the UI) — and in addition, **v1 chooses a plaintext download but makes a
  secret scan of the export path a hard requirement** (a detection blocks by default and passes only
  on an explicit ack from the person who has reviewed the content; the bundle carries the full
  history, so the scan reads the full history too). Repo growth is slow given the append-only nature
  of .md, and a periodic `git gc --auto` should suffice.

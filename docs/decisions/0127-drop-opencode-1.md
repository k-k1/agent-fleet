# 0127. Drop opencode 1.x and support opencode 2 only

English | [日本語](0127-drop-opencode-1.ja.md)

- Status: **accepted** (2026-11-01). The user took decisions A–C below, the recommended option in each.
- Issue: #3132
- Related: [docs/log/119](../log/119-opencode-caller-session-plugin.md) (the opencode 2 port, the opt-in and
  the cut-over) / #2101 (opencode 2 support) / #2105 (the cut-over)

## Context

The image has pinned opencode 2 (`@opencode/cli`) since the cut-over. The pin moved from 2.0.29 to 2.2.5
without a rollback. The `opencode` kind still carries two kinds of 1.x support, and nothing uses either one:

- **A rollback path.** Every surface that differs between the majors branches on one of three checks: the
  probed serve major (`isV2()`), the installed major (`installedMajor()`) or a launch's major
  (`LaunchSpec.Major()`). The surfaces are the serve API, the launch line, the service config, the
  instruction file, the plugins, the store reader, the npm package and the CI. The point of the branches was
  that setting `OPENCODE_VERSION=1.19.0` alone rolls the image back.
- **The opt-in** (`install-opencode2`, `opencode-major`, `AF_OPENCODE_MAJOR`). It installed opencode 2 into a
  private prefix with its own database, so that a workspace on the 1.x pin could try it. `PinnedV2()` makes it
  inert while the pin is 2.x, so it has had no job since the cut-over.

The rollback path is weaker than it looks. Measured on the cut-over (docs/log/119): a 1.x opencode on a
database that 2.x has migrated sees only the conversations from before the upgrade. Everything created on 2.x
is invisible to it. So a rollback loses every conversation made since the upgrade.

The defaults also lean the wrong way now. `serveMajor()` answers 1 until a probe says otherwise, and
`pathMajor()` answers 1 when neither the package nor the pin can be read. So code that runs before the first
probe takes the 1.x branch on an image that has no 1.x.

## Decision

### 1. opencode 2 is the only supported major

The `opencode` kind supports opencode 2 only. The code paths for 1.x are removed. An opencode whose major is
not 2 or later is refused at launch with an error that names the version found, instead of being driven
through a 1.x path.

### 2. A rollback is an earlier 2.x pin

**Decision A** (taken: accept the loss of the 1.x rollback). `OPENCODE_VERSION=1.19.0` stops being a supported
rollback. A bad opencode release is handled by pinning an earlier 2.x release, which reads the same database.

### 3. The 1.x → 2.x store migration stays

A home that has not started opencode since the cut-over still holds a 1.x `opencode.db`. The first opencode 2
process that opens it imports the 1.x conversations in place, and that import keeps running after the server
answers. `EnsureMigrated` (`migrate.go`) keeps af from treating a conversation as missing before the import
ends, so it stays.

**Decision B** (taken: migrate at Agent start, then drop the 1.x reader). The reads of the 1.x tables
(`transcript.go`) exist for one case: history shown before opencode 2 has opened a 1.x database. Once the
Agent finishes the migration when it starts, that case is gone, and the reads are removed in the same change.

### 4. The opt-in's leftovers are not merged

**Decision C** (taken: leave them, and document how to remove them). A member who used the opt-in has a
private install under `~/.local/share/agent-fleet/opencode2` and a database under
`~/.local/share/agent-fleet/opencode2-data`. Its conversations were never merged into the main database, and
the cut-over already stopped af from reading them. Nothing deletes them automatically. The guide says what
they are and that they can be removed by hand. The state-directory names (`opencode-major`,
`opencode-serve.json`) stay listed in `statemig`, so the drift check still accounts for the files an old
workspace may hold.

### 5. What is not 1.x support

- `--standalone` (`AF_OPENCODE_TUI_STANDALONE=1`) is an opencode 2 launch mode and stays.
- The one-way removal of a stale `opencode-ai` package from `~/.local` in `entrypoint.sh` stays. A home that
  self-updated on the 1.x pin still has one, and it shadows the image's opencode on `PATH`.

## Consequences

- The `opencode` package loses the major switch. Code that runs before the first probe no longer defaults to
  1.x.
- The opencode contract CI runs 2.x only. The tests that skip on 2.x (`skipOnOpencode2`) and pin a 1.x tree
  (`useMajor(t, 1)`) are deleted.
- A standalone opencode slot is always opencode 2, so the Console's question keys can use the opencode 2 form
  contract for it as well.
- The work lands in phases, one PR each, listed in #3132. The opt-in goes first: nothing reaches it.

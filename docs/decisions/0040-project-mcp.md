# 0040. Treat project-scoped MCP as "editing on the user's behalf", and write only as a one-shot

English | [日本語](0040-project-mcp.ja.md)

- Status: adopted, not implemented
- See also: [56-project-mcp.md](../log/56-project-mcp.md) (the design) /
  [48-mcp-registry.md](../log/48-mcp-registry.md) §8.2 §8.4 (the rules whose scope this ADR clarifies) /
  [0031-mcp-registry.md](0031-mcp-registry.md) (the registry decision — this ADR is a **different axis** of it)

## Context

ADR 0031 / docs/48 let AF distribute MCP server definitions, but only to **one place, each CLI's
user/global scope**; the project scope on the repository side (`.mcp.json` / `opencode.json` /
`.codex/config.toml` / `.cursor/mcp.json`, …) was deliberately left alone as "the user's" (§8.2).

A hole opened outside that in real use. `~/repos/novel-lab` registers the same single MCP server
**twice**, in `.mcp.json` (claude / copilot) and in `opencode.json`, and fixing one leaves the other
to rot. AF can neither see nor fix this.

Measurements on 2026-08-09 (docs/56 §2) showed that a simple "copy" is not enough:

- **Placeholder syntax differs per CLI.** claude uses `${VAR}`, opencode `{env:VAR}`, cursor both
  (`${VAR}` and `${env:VAR}`), and **codex expands nothing**. Putting `${env:VAR}` into opencode
  leaves the `$` behind and yields a different path, `$/home/…` — a silent error, not a startup
  failure.
- **It does not take effect immediately after writing.** claude's project `.mcp.json` sits at
  `⏸ Pending approval` and **does not even start the probe**; cursor shows
  `not loaded (needs approval)`. codex gates on trust.
- Therefore **the novel-lab example cannot be carried over to codex at all unless the values are
  expanded to real paths.**

## Decision

1. **Split into two axes.** The "distribution axis" (registry → user/global, written **automatically**
   by af, with an ownership ledger) and the "management axis" (project scope, written by af on the
   user's behalf **only on an explicit action**, and **not owned**). docs/48 §8.2 is not revised;
   one section is added clarifying that **what af writes automatically is user/global only**. That
   `Materialize*` never touches the project scope is pinned by a test.
2. **Do not put an af ownership marker in a project file.** A marker is machinery for "af will touch
   this automatically later", and with no automatic touching it has no purpose. Beyond that, a
   project file is **shared, committed and read by colleagues**, and mixing in AF-specific keys
   leaves traces in the repository of people who do not use AF.
   **The audit trail is git's job (`git diff`).** After writing, direct the user to the SCM pane.
3. **A pure one-shot.** No record of a canonical copy, no continuous sync, no drift tracking. There
   are zero triggers for automatic application (it is called from neither session startup, nor agent
   startup, nor registry CRUD).
4. **The user decides each merge (conflict) individually.** When a name already exists in the
   destination, AF falls to neither overwrite nor skip automatically. **But AF does compute and show
   the dialect-conversion candidate** — finding the difference and offering the options is the
   tool's job; choosing is the user's.
5. **Two stages, plan → apply, with optimistic locking on `planHash`.** The working copy is shared
   with other sessions and with the user's editor, so a person can edit between the read and the
   write. A mismatch is a 409.
6. **A text edit that does not destroy the formatting.** Re-emitting the whole file with
   `json.MarshalIndent` is not adopted. In a committed file, a diff that touches every line is **a
   diff nobody can review**, and the feature goes unused. JSON follows the same thinking as codex's
   line editing of TOML: replace only the byte range of the entry concerned.
7. **Do not carry file permission 0600 over.** git records only `100644` / `100755` as the mode, so
   0600 cannot be preserved in principle. **Use "the user-scope safety valve does not apply here" as
   the grounds for the secret warning.**
8. **In v1 the source of a copy is a project file only.** The AF registry (user / tenant / builtin)
   is not a source. Emitting a tenant-distributed value into a repository would put the tenant's
   credentials into git; builtins are container-specific and meaningless in someone else's
   environment; and duplicating a user-origin entry increases double registration (the very disease
   this feature is meant to cure). **This one constraint removes new secret leakage in principle.**
9. **The default for secrets is "do not write the value".** Carrying a value across requires an
   explicit per-server checkbox, and **the strength of the warning is decided by the difference in
   tracked status between source and destination** (untracked → tracked is red — the only route by
   which something newly enters git's control).
10. **The Console never receives a value.** The snapshot API returns values as `***`, and `apply`
    takes **only a flag**, `withSecrets`, with the Agent reading the real values from the file and
    writing them. This is the only shape that implements "allow the copy after a warning" without
    breaking docs/48 §5.1.
11. **Do not perform gates on the user's behalf.** claude's approval, `cursor-agent mcp enable` and
    codex's trust are all the user's trust judgement, and if AF signs for them, **merely using AF
    means the trust judgement was skipped**. Present the command; the user runs it. The result is
    reported on two separate lines, "**written**" and "**in effect**" — claude and cursor do not
    start before approval, so AF cannot even verify the write's success with `mcp list`.
12. **The columns on screen are files, not kinds.** `.mcp.json` applies to two kinds, claude and
    copilot, so counting by kind causes the accident of writing the same file twice.
13. **The entrance is the repo row's context menu → a modal.** The settings modal is the place for
    the whole workspace (the user scope), and mixing them **removes from the screen which scope you
    are touching**. The settings → MCP tab gets one line noting "the project scope is not handled
    here", so it is not a dead end.
14. **The target is the one selected working copy only.** Other worktrees of the same repository are
    neither listed nor edited (do not create uncommitted changes on another session's desk). For a
    worktree, note that "this does not reach the other working copies until you commit".
15. **Do not share the type with `mcpreg.ServerDef` (add a new `internal/mcpproj`).** `ServerDef` is
    "what AF distributes", comes with `Origin` / `Targets` / the ledger, and its `Validate()`
    **rejects** `af`-family names. On the project side the job is the opposite — **find that name and
    show it in red** (the hijack detection in docs/48 §8.4). Using the same type would let it creep
    into the composition of the effective registry, leaving room to **distribute it to the user
    scope**. However, **the per-kind spelling (the serialisers) is shared with `mcpreg`** — defining
    them twice guarantees drift.
16. **kiro is read-only in v1, and agy is out of scope.** kiro's project-scope contract is unverified
    (everything under `mcp` requires a login), and agy has no project scope. **Both still appear as
    rows** (removing them silently looks like an oversight, and the same question gets asked again).
17. **The reverse direction (importing project → the AF registry) is not built in v1.** The registry
    is the user scope, i.e. it applies to every repository, so importing would start a server meant
    for one repository everywhere. Building it would first require designing a repo scope into
    `ServerDef`, which is a separate decision.

18. **Adding to git's ignore settings is a feature, defaulting to `.git/info/exclude`.** §7.2's
    warning gets a way to fix it on the spot. The default is the recoverable one, which adds no
    commit; `.gitignore` (which imposes the same judgement on colleagues) is offered only after
    stating its effect.
    ⚠️ **`.git/info/exclude` is not per worktree** (it lives in the common dir and applies to the
    parent clone and every worktree — measured), so always display the blast radius. Global excludes
    are out of frame (undecided).
19. **"Add to ignore" and "stop tracking" are separate operations with separate confirmations.**
    `.gitignore` **has no effect on already-tracked files** (measured), so the motivating examples
    (`.mcp.json` / `opencode.json`, already tracked) do not change at all from adding an ignore.
    Actually removing them needs `git rm --cached`, and **once that is committed and pushed the file
    disappears from colleagues' working copies** — far too heavy as the consequence of "I ignored
    it". **AF goes as far as changing the index and does not commit.** `--assume-unchanged` /
    `--skip-worktree` are not adopted (they only hide the problem and break inscrutably).
20. **State explicitly that whether a whole file may be ignored differs by kind.** `.mcp.json` /
    `.cursor/mcp.json` / `.kiro/settings/mcp.json` / `.github/mcp.json` are MCP-only and may be
    ignored. **`opencode.json` and `.codex/config.toml` hold settings other than MCP**, so ignoring
    the whole file loses those too. opencode can escape by splitting, because **`opencode.json` and
    `opencode.jsonc` are both read and merged at the project level too** (measured), but **codex has
    no escape**. claude cannot split (`mcpServers` in `.claude/settings.local.json` has no effect —
    measured), but it does not need to, since `.mcp.json` is MCP-only.

## Options not taken

### Continuous sync (fix a canonical kind and follow automatically)

It looks like the most direct answer to "fixing one leaves the other to rot". The reason not to take
it is that **project files are under git and are meant to be committed.** Automatic following grows
a diff in the working tree at times the user is not touching it. It could write the moment you
switch branches, or another session edits that file, or you `git stash` — and cleaning up the
conflict falls on the user, not AF. It also requires permanently managing "which is canonical".
The cost of **someone else's working tree getting dirty by itself** outweighs the convenience.

### Only show the difference; AF does not write

It touches §8.2 not at all and leaves the responsibility entirely with the user. But it barely
reduces the motivation (the labour of double registration) — the work of transcribing the dialect
conversion by hand remains, and that is exactly where mistakes happen. **Computing and showing the
conversion but refusing to write it** is half-hearted, and a screen that can be read but not fixed
goes unused.

### Promote the project scope to something af manages (a per-repository ownership ledger)

It could be unified with the user scope's read → merge → rename and the implementation would be
straightforward. The reason not to is that there is nowhere to put the ledger: in home, it is
orphaned when the repository is deleted; in the repository, **it adds one more thing to commit** (a
file that needs explaining to colleagues who do not use AF). And the ledger's purpose — deciding
which lines af may delete automatically — is unnecessary in a design that never deletes
automatically.

### Rewrite the whole file with `json.MarshalIndent` (reusing the existing `materialize_json.go`)

The code already exists and is genuinely sufficient for the user scope. It is not taken for project
files because **the diff becomes unreviewable**. Nobody reads diffs of configuration files under
home, but repository files appear in a PR. A tool that changes 200 lines when you add one entry goes
unused.

### Always copy the values / never copy the values

The former makes putting secrets into git the default. The latter leaves manual work every time in
**how it is actually used** — "the same value is already written in both files" (which is exactly
novel-lab). Not writing by default, writing on an explicit checkbox, and **varying the warning's
strength by the difference in tracked status**, matches reality.

### Have AF grant codex's trust and run cursor's `mcp enable`

It would let us say "applied" without qualification. But trust is the very trust boundary at which
the agent asks the user "may I trust the code and commands in this directory?", and **if AF signs
for it, merely going through AF means the trust judgement was skipped**. An MCP server can start
arbitrary commands (the same reason ADR 0031 decision 2 forbade tenant-distributed stdio), and
automating that approval opens the same hole by the back door.

### Have "add to ignore" go all the way to `git rm --cached` / have AF commit as well

Reaching "it will not be committed any more" in one button looks helpful. The reason not to is that
the result of that one button is **the file disappearing from colleagues' working copies the moment
it is pushed**. Adding an ignore is recoverable; untracking changes other people's environments
(even though it stays in history) — not a weight that belongs behind the same confirmation. AF does
not commit either, because charter 2 leans on "the audit is git's diff", so **the user must not be
deprived of the chance to look at that diff**.

### Fold it into the settings modal's "MCP servers" tab

There is something to the argument that having MCP settings in one place makes them easy to find. It
is not taken because that tab is **a listing for the whole workspace (the user scope)** and its
design promise is to **show the effective registry exactly as it is** (docs/48 §11.1). Putting a
table whose contents change per repository next to it erases "which scope am I looking at" — which
is the direction that makes the §8.4 hijack incident unreadable. Instead, one note connects the
route.

# 0042. User instructions are one body of text owned by AF, distributed to each CLI as "an AF-only file plus a reference" wherever possible

English | [日本語](0042-user-instructions.ja.md)

- Status: adopted; **P0–P2 implemented** (2026-08-13. The design is docs/60. Decisions 4/6/7 changed
  from the first edition as a result of measurement, and decision 4's copilot route and decision 5's
  entrypoint side changed again as a result of implementation. P1 measured kiro's global steering and
  settled that **cursor is the only kind we cannot distribute to**. P2 made the fleet policy reach
  agy / copilot / kiro through the same distributor.)
- See also: [60-user-instructions.md](../log/60-user-instructions.md) /
  [57-project-tools.md](../log/57-project-tools.md) (the distribution axis / management axis split) /
  [0031-mcp-registry.md](0031-mcp-registry.md) (the distribution axis's ownership ledger) /
  [0040-project-mcp.md](0040-project-mcp.md) (the management axis, i.e. the not-owned side) /
  [0022-agent-memory-management.md](0022-agent-memory-management.md) (a third place — agent memory)

## Context

The instructions an agent always reads come in only two layers: the fleet policy (baked into the
image) and project instructions (committed to the repository). There is nowhere for the layer in
between — "how this person works".

Measurement (2026-08-13) further showed that the middle layer is not merely absent but **impossible
to create**.

- `workspace/entrypoint.sh:566-575` overwrites `~/.codex/AGENTS.md` and
  `~/.config/opencode/AGENTS.md` with `cp -f` on every start. Anything the user appended vanishes
  silently.
- claude's destination `/etc/claude-code/CLAUDE.md` is root-owned and `dev` inside the container
  cannot write it.
- `~/.gemini/AGENTS.md` is 450 B (the rtk block only) — **agy does not even read the fleet policy**.
  The same goes for copilot (its 15.4k-token system prompt contains no fleet policy). cursor and kiro
  have no distribution route at all.

Of the two traps suspected at design time, measurement eliminated one and reshaped the other.

- **Trap B (codex's byte budget) is refuted.** Measured with `codex debug prompt-input` (which emits
  the model-visible prompt as JSON with no API charge), `project_doc_max_bytes` (32 KiB by default)
  applies **only to the total of the project-document chain**; `$CODEX_HOME/AGENTS.md` is outside the
  budget and uncapped (a 42 KB global passed intact). The suspected existing bug does not exist.
- **Trap A survives in a different shape.** claude reads `$CLAUDE_CONFIG_DIR/CLAUDE.md` and does not
  read `~/.claude/CLAUDE.md` (settled with a canary). opencode did not pick that up either (its
  bundle has a path but the actual behaviour refutes it; the conditions are unidentified). So putting
  it in the latter **has no effect on any kind**.

And the discovery that changed the design most was that **many kinds can be reached without writing
into someone else's file**.

- opencode: adding one AF-only file to the `instructions` array in `opencode.json` works (measured).
- copilot: passing an AF-only directory in `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` works (measured; no
  file ownership).
- claude: the user memory file does not exist by default, so AF can own it outright.
- codex / agy: there is no setting that points at an extra instruction file, so composition is the
  only means.
- cursor: no user layer exists locally (User Rules are `aiserver.v1.UserRules`, i.e. server-side).

## Decision

1. **Introduce user instructions as an artifact AF owns.** The canonical copy is
   `~/.config/agent-fleet/user-notes.md`. `.config` is in `homeKeep`
   (`control-plane/runtime_docker.go:396`), so it survives both a recreate and a "clean home". It is
   not put in the CP's database.
2. **One body of text, with a per-kind checkbox for where it applies.** Per-kind bodies would create
   N copies of the same prose to maintain, so they are not adopted. Kinds that cannot be reached
   still appear as rows. **Only cursor applies, and the reason is "there is no user layer locally"** —
   a structural conclusion rather than something waiting to be implemented, so it is not made to look
   like "not supported yet (support planned)".
3. **Treat it as the distribution axis.** AF writes it automatically and states what it owns. The
   "eight articles of the project-file charter" from docs/57 do not apply. Nothing is written
   anywhere that gets committed.
4. ★ **Prefer "an AF-only file plus a reference" over "writing into someone else's file".** (Changed
   from the first edition following measurement.) claude = a file it owns outright / opencode = one
   entry added to `instructions` / copilot = one file with an AF-specific name in
   `$COPILOT_HOME/instructions/` / kiro = one file with an AF-specific name in `~/.kiro/steering/`
   (measured that global steering is read). **Composition is the last resort, for codex and agy
   alone**, which have no way to reference. We do not add writes to shared files for the sake of
   uniformity.
   (copilot was measured to work via `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` too, but **the env is not
   adopted**: the export would have to be distributed to all three routes — tmux startup, managed
   ACP, and typing by hand — and a miss becomes an invisible hole where "only that session does not
   get it". A file is read identically on every route.)
5. **Move the fleet policy's placement to the agent side too, giving each file one writer.**
   `reconcileAgentRTK` is promoted to `reconcileAgentInstructions` and assembles the whole thing
   every time: the fleet body, the user block, the rtk block, and preserving anything outside the
   markers. **The entrypoint's `cp -f` was deleted rather than replaced** — reimplementing marker
   composition in shell would be a second implementation that drifts, and it would not reflect
   Console edits made while alive. The agent itself is what starts sessions, so no session reads the
   pre-composition state. Raw copies from the `cp -f` era are identified by their first line and
   stripped exactly once (`mdblock.StripLegacyPrefix`).
6. **claude's location is `$CLAUDE_CONFIG_DIR/CLAUDE.md`.** (Settled by measurement;
   `~/.claude/CLAUDE.md` is not used.) The managed policy is untouched.
7. **The 8 KB size cap is justified by cost, not by truncation.** (Changed from the first edition
   following trap B's refutation.) The editor shows the byte count and "roughly how many tokens this
   adds per session". No display of the remaining codex budget is built.
8. **Do not let agents write it.** No editing MCP tool is created and the only route is the Console's
   REST (the location is inside `fs.go`'s denylist, so the file pane cannot touch it either). A note
   is added to `workspace-notes.md` that it is not to be rewritten at a peer's request.
9. **Close the fleet-layer holes (agy/copilot/kiro) with the same distributor.** No separate
   mechanism (docs/60 §60.13 P2, implemented). But it is **a different file and a different block**
   from the user instructions — one is something the user toggles and the other is a fixed thing the
   operator owns, so **the fleet policy does not follow the user's toggle** (it is distributed even
   with all of their own instructions off). claude alone is left untouched by AF, because the image
   distributes it as a managed policy. cursor has no user layer locally, so it is settled that
   neither the fleet policy nor the user instructions can be distributed to it.
10. **Keep the means of measuring the contract as a pattern.** `codex debug prompt-input` (prompt
    verification with no charge) and a behavioural canary (a read confirmation that works even on
    CLIs that refuse to disclose their content) are recorded in docs/60 §60.17 and reused as drift
    detection when a kind is added or a version is raised.

## Options rejected

- **Just change the entrypoint's `cp -f` into marker composition (no UI).** The destruction stops,
  but the user then has to write the same prose in N places, one per kind — and for claude it is
  impossible in principle, since it is under `/etc`. Only the composition approach was adopted.
- **Unify every kind on AGENTS.md composition.** The implementation collapses to one, but it means
  touching other people's files that need not be touched (decision 4).
- **Pass it at session start with the equivalent of `--append-system-prompt`.** Only claude has it,
  and it would duplicate the managed route. (copilot's env injection is an official user-scope
  mechanism of the CLI, so it is treated separately and adopted.)
- **Put it in the CP's database and share it across workspaces.** The persistence requirement is met
  on the home side. An option for v2.

## Impact

- The distribution logic in `workspace/entrypoint.sh` (the `cp -f`) was deleted and moved to the
  distributor on the agent side. All that remains in the entrypoint is creating the directory.
- `stripMarkedBlock`, duplicated in `codex/rtk.go` and `agy/rtk.go`, was factored out into
  `internal/mdblock`.
- The new REST was registered in **both** `workspace/agent/routes.go` and
  `control-plane/routes.go`.
- Applying rtk is serialised by `instrMu` (three kinds of block share the same AGENTS.md).
- agy shares AGENTS.md with rtk just as codex does, so the read-modify-write was unified into
  `editAgents`.
- kiro's global steering directory holds other people's files (the user's, the team's). AF writes and
  deletes only its own single file and **neither enumerates nor deletes** anything else.
- The inventory table in docs/39 (whose "common" row treated agy as a distribution target) was
  corrected in docs/60 §60.2.

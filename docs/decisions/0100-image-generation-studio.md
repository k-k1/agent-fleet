# 0100. Image generation studio — a session's agent edits the draft, a person presses Generate

English | [日本語](0100-image-generation-studio.ja.md)

- Status: **proposed** (drafted 2026-09-23). Rationale and history are in
  [docs/log/113](../log/113-imagegen-studio.md) (five rounds of design plus a read-only review by
  another session, [113-review](../log/113-review.md) — 7 red, 12 yellow — already folded in).
  Nothing is implemented.
- Number: `develop` tops out at 0098; 0099 is taken by two unmerged branches (`temp/sidv2bw`,
  `temp/sjys6nk`), hence 0100.
- Related: [0081](0081-image-generation-pane.md) (today's image-generation pane; this ADR overturns
  decisions 6 and 7 and keeps 1–5 and 8–12) / [0069](0069-image-generation-providers.md) (decision 8,
  "never call a model the user did not press for", still holds) / [0015](0015-agent-managed-driver.md)
  (the Managed execution method; claude and agy do not have one) / [0022](0022-agent-memory-management.md)
  (git snapshots of memory — the future shape of decision 12) / [0094](0094-instruction-edit-image-models.md)
  (instruction edit; its revision dropped the centre crop, the premise of decision 11) /
  [log 111](../log/111-inpaint-mask-canvas-p2.md) §10 (the mask canvas) / [log 33](../log/33-chat-context-usage.md)
  stage 5 (a "plan" kept verbatim beside a conversation — the ancestor of the draft)

## Background

The ADR 0081 pane mass-produces pictures "without an LLM in the loop". Its prompt help is decision 7's
layer B: one pressed, one-shot question with no conversation, so the second ask knows nothing of the
first. The user's request goes further — **talk with an assistant to refine the prompt, have the draft
update as you talk, press Generate yourself, tune while looking at the results, keep a history**. Round
two settled the container: **a session, not the assistant chat**. Only a session offers **deep
reasoning** (kind, model and effort chosen on the spot) and **building the prompt from documents in
the repository** (a cwd that is the repo). The assistant chat runs in `chat-wd` (not a repository),
with Bash and Edit disallowed, on the chat's default model with no effort knob
(`chat_providers.go:2210, 2085`).

Facts that shaped the design (verified on the 2026-09-23 tree from `e55feb37`; the claims that turned
out wrong were corrected by the review):

- The Agent's request vocabulary (`jobs_http.go:28-59`) — prompt / negativePrompt / size / count /
  inputs / mask / model / loras / seed / strength / params / label / out_dir / jobs / seed_policy /
  trial / full_steps — is enough. **This ADR adds no word.** But `inputs`/`mask` have **no path
  guard**: `spec()` copies them through and `comfy.go:1186-1187` uploads whatever `os.ReadFile` returns.
- The draft lives in `localStorage` (`draft.ts:106`); the server does not know it. History is the
  Agent's in-memory job list (500 finished, lost on restart) plus per-picture sidecars.
- Family knowledge is split: the knobs a family reads live in the Agent (`comfy.go:329-358`), the
  dialect and quality prefixes in Console i18n (`families.ts:42-177`).
- **Managed drivers exist for 7 kinds** (`session_turn.go:33-41`). **claude and agy have none.**
- **A Managed prompt has more than one entry point** (`/turn` calls `h.Send` directly; bridge,
  `/input`, `initial_prompt`) — there is no single place for the Agent to prepend anything per turn.
- **The session-side af MCP server knows its own session through `AF_SESSION_NAME` only for Terminal
  (all kinds), fresh codex Managed threads and lcpp.** opencode / copilot / cursor / kiro Managed and
  muse guess from cwd (`mcp_stdio.go:3309-3359`); opencode's af child is shared per directory.
- tools/list is **rebuilt per request** and `list_changed` is sent every minute
  (`mcp_stdio.go:286-298, 480-545`).
- The chat assistant cannot call `generate_image` (an owning session is required, `mcp_stdio.go:166-170`).
- Of the Managed turn's `attachments` (`POST /sessions/{name}/turn`), only opencode, codex and muse read
  them; copilot / cursor / kiro / lcpp drop them silently.
- `~/.config/agent-fleet` is on the Files pane denylist (`fs.go:126`) and the workspace policy tells
  agents not to touch it.
- The centre crop of instruction edit is gone (ADR 0094 revision #907, implementation #913, acceptance
  #914). The mask canvas can start on log 111 §10's design.

## Decisions

### Decision 1 — The LLM only edits the draft. The generation path does not change

"Without an LLM in the loop" (0081) still holds for generation. The agent touches the **draft**; a
person presses Generate; what runs is 0081's job queue unchanged. The vocabulary, validation, jobs,
trials, groups, cancel, EMA, sidecars, `props`, usage rows and the CP's seven relay lines stay as they
are. **The one exception** is decision 4's guard (path validation of `inputs`/`mask` added to `spec()`).

### Decision 2 — A "studio" lives in the Agent, has its own id, and binds one session

The Agent keeps a **studio** (`~/.config/agent-fleet/imagegen/studios/<id>.json`): `id / title /
draft / locks / versions[] / session / agent_trial / mask_strokes / created_at / updated_at`. **The
studio is the truth for the draft**; `localStorage` only remembers the last opened studio id.

- Why not the session name: swapping the agent (sonnet→opus, claude→codex), a session too old to
  resume, deleting a session to tidy the list — in every case the draft and versions must survive.
  The studio is primary; the session is "who it is bound to right now".
- The session meta gets a back-reference `Studio` (next to `Origin`). **Fork does not inherit it**
  (one studio, one session). The field has to be carried through the three `Meta{…}` literals
  (create / fork / recreate), the Agent's `wireSession`, **the CP's `sessionWire`** and the stopped-
  session DB mirror — an unlisted field is dropped silently.
- Keep the body small; write it under an in-memory lock (the chat store's `LockConv` shape) with
  tmp→rename (`fstore` has neither locking nor atomicity). The edit log (decision 9) is a separate file.
- Without a session the pane keeps working on today's `localStorage` draft; the first message creates
  the studio and moves the draft in. **Nothing an ADR 0081 user has today is lost.**

### Decision 3 — The contract is four tools on the session-side af MCP server. `generate_image` is not advertised

| Tool | What it does |
|---|---|
| `get_image_studio` | draft, locks, **since last call** (human edits, rewinds, new results — at most 5 plus "N more"), model facts (family, knobs read, sizes, defaults, LoRAs with trigger words), the knowledge "summary" section, version summary. 8 KB cap |
| `set_image_draft` | **partial update**: only the fields written change, `null` clears. Validated by the same `spec()` as enqueue. Locked fields are dropped with a reason |
| `run_image_trial` | **takes no arguments**. Runs a trial of exactly the draft the pane shows (one picture, head of the queue, the family's trial steps, at most 3 waiting). Returns path, seed, warnings, elapsed. Also appears in the trial slot |
| `add_image_knowledge` | appends to decision 12's "record" section (scope, key, note, evidence) |

- Advertised **only while the owning session is bound to a studio** (`Studio` on the meta that
  `mcpOwningSession()` resolves). tools/list is per request and `list_changed` fires every minute, so
  re-binding takes effect within a minute on kinds that honour the notification (which kinds do is
  unmeasured — the acceptance run measures it). The decision reads only the meta, never `status`,
  so a slow Agent cannot make the tools flicker.
- **`generate_image` is not advertised to a studio session** — "a person presses Generate" is
  guaranteed by the advertised set, not by a promise in the persona. **No tool enqueues N pictures.**
  The agent may run a trial because the user's complaint about `generate_image` was "the prompt is
  invisible": with an argument-less tool, what runs is always what is on screen. Per studio, "let the
  agent run trials" (default on).
- When identity is ambiguous (decision 8) the tools **refuse with a reason** rather than vanish.
- MCP over a fenced block in the transcript: every session kind speaks af MCP, transcript parsing is
  nine kinds wide, a tool call lands mid-turn in real time, and the description cost is paid only by
  studio sessions.

### Decision 4 — Which fields the agent may write, and which only a person may

| Agent | Person only |
|---|---|
| `prompt` `negative` `params{steps,cfg,sampler,scheduler}` `size` `loras` `strength` `op` `inputs` (once the guard is in) | `model` `seed`/`seed_policy` `jobs` `count` `out_dir` `label` `mask` |

- `model` stays with the person: switching it switches the family — knobs, sizes and negative
  support all change — and a cold engine costs minutes on the first picture. The agent may write
  `suggest_model`; the pane shows a proposal card.
- `op` and `inputs` go to the agent: "change the sign in this picture to CLOSED" is one move across
  op, reference and instruction. **Precondition: the enqueue-side guard** (inside the browse root plus
  the Files pane denylist). Not opened before the guard lands.
- `mask` is person-only (painted by hand). The agent may set `needs_mask: true`; the pane shows the way.
- **Per-field locks** (🔒). Writes to a locked field are dropped with a reason. No automatic locks.

### Decision 5 — The agent pulls context. The Console appends one visible cue line. Nothing is prepended

The persona says "on every message, call `get_image_studio` first". The Console appends one line
(≈30 tokens) at the **end** of the message:
`[studio v4 · draft changed · 2 new results · rewind #9 → get_image_studio]`. The call does not
depend on the cue (a message typed in a Terminal pane has none).

- Why nothing is prepended: there is no single place to insert (`/turn` calls `h.Send` directly), no
  single place to strip (`splitPastedImages` handles only the trailing attachment note; auto-title reads
  the first 400 characters), and it would stay in the transcript and be re-sent every turn (on lcpp,
  most of the window within ten turns).
- Why the end: it does not reach auto-title. Strippers live in two places — the transcript model layer
  (`mirror/transcript/model.ts`) and the Go side (reply suggestions, branch names) — sharing one
  constant; the heading must not start with `<` (`isNoise`).
- The "last read position" ledger is keyed by (studio, session); keyed by studio alone a re-bound
  agent would miss the facts.
- lcpp rebuilds its system prompt every turn, so on lcpp alone the persona and the "summary" section
  can go there (P1).

### Decision 6 — "Looking" has two tiers: the person always, the agent only on a press

The person always sees result cards and the trial slot; fields the agent changed are outlined until
the person touches them. **"Show this picture to the agent"** hands the picture over only when
pressed (ADR 0069 decision 8). How it is handed over follows the kind's capability: opencode, codex
and muse read first-class Managed attachments; every other kind and TUI get the path woven into the
text, as today. Kinds or models without vision get the button disabled with a reason. The check is a
new attachment flag next to `agent.caps.imagePaste`, and the capability table joins the
`guideTable.test.ts` cross-check.

### Decision 7 — Family dialect, quality prefixes and recommended ranges move into the Agent's family table

`comfyFamilyRow` gains `Dialect`, `QualityPrefixes`, `StepsRange`, `CFGRange`, surfaced through
`modelStatus` in `GET /imagegen/status`. The Console's family card merely renders them (only
translations stay in i18n). `get_image_studio`'s "model facts" come from the same row. This extends
0081 decision 4 ("the family decides what it reads and says so") beyond knobs. ADR 0098's
qwen-image-2.1 touched both sides in two commits — that duplication is folded. Layer B
(`PromptHelpModal`, `prompthelp.ts`) is removed: "write the prompt for me" is the first message of
the conversation.

### Decision 8 — A session only when needed. Managed and TUI in the same P0. Worktree on by default

Opening the pane creates no session. "Attach an agent" is built from the launch dialog's parts
(`ModelPicker` / `EffortPicker` / `SubdirPicker` / `BranchList`): kind, model, effort/thinking,
execution method, repository as cwd, subdir, worktree (default on), permission skip. No prompt field.

- **claude is TUI-only.** Meeting "deep reasoning" with claude (Opus) puts TUI in P0. With decision 5
  pulling, Managed and TUI differ only in how attachments travel and in launch/resume. The kind list
  comes from `managedDrivers`.
- **Worktree on by default** is the only way to make cwd-based identity unambiguous for the kinds that
  guess. Kinds that receive `AF_SESSION_NAME` (claude, fresh codex, lcpp, Terminal) may turn it off
  (to read uncommitted material).
- **opencode Managed is excluded from studios** (Terminal is fine): its af child is shared by every
  session in the directory, so `set_image_draft` could run from another conversation. Delivering
  `AF_SESSION_NAME` to copilot / cursor / kiro / muse children is P0 prerequisite work; until then
  those kinds are Terminal-only.

### Decision 9 — Three histories: edits, versions, pictures

- **Edit log** `draft_log`: one entry per draft change (time; author = agent turn / human /
  `rewind ← #n`; changed fields with before/after; the full draft at that point). Append-only JSONL in
  a sibling file (`studios/<id>.log.jsonl`). In the transcript the `set_image_draft` tool card renders
  as "draft updated: cfg 7→5" with **"rewind to here"** (`POST …/rewind`; nothing is deleted, locks
  are untouched, the next `get_image_studio` reports it in `since`).
- **Versions**: the draft as it was when a Generate button was pressed (a person's trial or batch, or
  the agent's trial). A version is the edit-log entry carrying the "pressed" mark.
- **Picture history**: `GET /imagegen/history?studio=&before=&limit=` backed by
  `generated/console/history.jsonl` (one line appended with each sidecar; rebuilt from sidecars if
  missing). Actions: "restore these settings" (the same `rewind`), "use as reference", "show", "compare"
  (up to four). Retention as in 0081 decision 3.

### Decision 10 — Several studios; the pane holds a studio id

`{ kind: "imagegen"; studioId: string | null }`; `sameTarget` compares studio ids (`null` equals
`null`). A bound session shows in the session list with a wand icon and opens the imagegen pane.
Opening the same session in a mirror pane is redirected to the imagegen pane (composer drafts,
attachment drafts and send echoes would otherwise overwrite each other). Deleting a studio deletes
its draft and versions; pictures stay.

### Decision 11 — Inpaint completes inside the studio. A person paints the mask, the agent writes the instruction

Entry points are "fix this part" on result cards and in history (`op=inpaint`, `inputs[0]`, the
canvas). The canvas follows **log 111 §10** (no crop band, no ratio table; export at native size,
shrinking only above the canvas area cap; brush width relative to the long edge; EXIF orientation
measured) and sits in the middle column. Strokes persist in the studio's `mask_strokes`. Inpaint
without a mask is refused exactly as enqueue refuses it.

### Decision 12 — Knowledge per model and per family. One document per model, in a visible folder

Four layers: 0 family facts (the Agent's table, decision 7) / 1 tenant notes (catalogue row
`prompt_notes`, 0081 open item 2, P1) / **2 workspace knowledge (this decision)** / 3 the studio's
conversation.

- Layer 2 is **`~/imagegen-knowledge/{families,models}/<key>.md`** (outside the denylist, visible in
  the Files pane, survives recreate). One file per model and per family, **four sections** (summary
  ≤1 KB, settings, prompts, record = append-only). The record is written through
  `add_image_knowledge`; the other sections through the agent's Edit and the person's Files pane.
  **Only a person deletes.** The agent writes only when told "remember this" or when the person judges
  a result. `get_image_studio` returns the summary every time; the rest on request.
- Sharing into a repository is an **explicit export** (a per-studio write target inside the repository
  was rejected: it pollutes other sessions' `git status`).
- **Later, add the folder to ADR 0022's snapshot roots** (the user's request: "diff-managed in git,
  like claude's memory"). Diff, restore, export and import come for free (P2).

## Options rejected

- **The assistant chat as the container.** No repository, no Bash/Edit, no effort knob (Background).
- **A fenced block at the end of the reply, parsed from the transcript.** Nine parsers, and it lands
  only at the end of the turn.
- **The Agent prepending a state block to Managed turns.** No single place to insert or strip; it
  accumulates in the transcript (decision 5).
- **Giving the agent `generate_image`.** Generation is the person's button; not advertising it makes
  that structural.
- **Studio = session name.** Swapping the agent loses the draft.
- **The live draft as a repository file.** The working copy is permanently dirty; validation and locks
  cannot be enforced.
- **Letting the agent write `model`.** Family switches and cold starts would happen unnoticed.
- **A version per edit.** What is compared is the "pressed" unit; edits go to the edit log.
- **Knowledge under `~/.config/agent-fleet`.** The Files pane refuses it and policy forbids agents.
- **Managed only.** claude could not be chosen.
- **Rewriting the pane without MirrorView.** Thinking, tool cards, attachments and resume would be lost.

## Consequences

- **Agent**: `internal/imagegen/studio.go` (store, lock, JSONL edit log, versions, rewind, knowledge
  read/write, trial endpoint), the guard in `spec()`, four `comfyFamilyRow` fields and `modelStatus`,
  `history.jsonl`, `session.Meta.Studio` (five places), four tools in `mcp_stdio.go` (names as string
  literals), `AF_SESSION_NAME` delivery for copilot / cursor / kiro / muse, the Go-side cue stripper,
  routes and golden.
- **CP**: relays for studios, history and knowledge, the `studio` field in `sessionWire`, `routes.golden`.
- **Console**: `features/imagegen/` as three columns (embedded MirrorView, launch parts, locks,
  highlights, edit log, history, knowledge notes, studio settings), `studioId` on the pane kind, the
  wand and branch in the session list, the transcript-model stripper, the attachment capability flag,
  removal of `FAMILY_CARDS` and `PromptHelpModal`, i18n, the screenshot stub.
- **Docs**: the image-generation section of `guide/member/04-files`, `guide/ref/features`, af-usage
  knowledge.
- **Tests**: pure (draft validation, locks, rewind, since folding) / DOM (highlights, locks, cards) /
  Go (negative controls for the guard, locks, JSONL, advertising toggle, tools/list fingerprint) / one
  live run (one Managed and one TUI kind; `list_changed` honouring measured per kind).

## Phases

- **P0 prerequisites**: ① the `inputs`/`mask` guard, ② `AF_SESSION_NAME` delivery for copilot /
  cursor / kiro / muse, ③ the cue-line strippers (transcript model layer and Go).
- **P0**: decisions 1–5, 7, 8, 10, 12 (layer 2), and decision 9's edit log and versions. The three-column
  pane. Removal of layer B. Instruction edit (reference, no mask) arrives with decision 4's opening.
- **P1**: decision 9's picture history and "compare", decision 6's "show", the model proposal card, the
  wand icon, the ledger `Ref`, the lcpp system-prompt persona, draft save/load as files, **decision 11's
  inpaint** (canvas per log 111 §10).
- **P2**: sweeps from the conversation (a proposed matrix the person enqueues), promotion to layer 1,
  **git diff management of knowledge** (ADR 0022 roots), a claude Managed driver (separate ADR).

## Unresolved

1. **Which kinds honour `list_changed`** (unmeasured beyond claude). Where it is not honoured,
   re-binding and the trial toggle wait for a resume. Measured in the P0 acceptance run.
2. **The trial cap of 3 is workspace-wide.** Whether to limit the agent to one trial per studio; the
   429 wording needs studio context; codex's 600 s MCP limit is short of a cold engine's 16 minutes.
3. **Tier-2 (workspace stop) fires during generation** — the reaper does not see imagegen jobs. A hole
   since 0081; filed separately.
4. **The summary cap (1 KB) and the 8 KB `get_image_studio` cap are guesses.** Tuned on the live run.

# 0100. Image generation studio — a session's agent edits the draft, a person presses Generate

English | [日本語](0100-image-generation-studio.ja.md)

- Status: **proposed** (drafted 2026-09-23). Rationale and history are in
  [docs/log/113](../log/113-imagegen-studio.md) (five rounds of design plus a read-only review by
  another session, [113-review](../log/113-review.md) — 7 red, 12 yellow — already folded in).
  Nothing is implemented.
  Status update (2026-09-24): P0 is implemented. The prerequisites and the frozen contract landed as #924; the three lanes as #925 (CP relay), #926 (Agent, `workspace/agent/internal/imagegen/studio*.go`) and #927 (Console pane); and the integration with revision 8 as #932, merged on 2026-09-23. The P0 acceptance run on a deployment is #959; P1 is #960.
- Follow-ups: #949, #956, #959, #960
- **Revision 1 (2026-09-23)**: folds in the ADR review by another session, `semvs2b` (codex /
  gpt-6-sol), [113-adr-review](../log/113-adr-review.md) (9 red, 12 yellow, 1 blue). Decisions 3, 4,
  8, 9 and 12 gained their missing contracts, a minimal picture history moved into P0, and the
  prerequisites were regrouped. The changes are listed at the end under "Changed in revision 1".
- **Revision 2 (2026-09-23)**: folds in the same reviewer's second pass ([113-adr-review](../log/113-adr-review.md)
  §5 — 7 new red, 3 yellow). Fixed: the call right after re-binding, the guard's coverage, the trial
  tool's wait, `needs_mask`, the press write order, studio creation order, the source of the kind list.
  Appended to the table at the end.
- **Revision 3 (2026-09-23)**: folds in the third pass ([113-adr-review](../log/113-adr-review.md) §6 —
  4 new red, 5 yellow). Reference pictures are **copied at enqueue** so no provider (child processes
  included) ever sees the original path; the trial tool gets a heartbeat; the version id is reserved
  before enqueue; the generated root becomes an allowed source; decision 1 has two exceptions.
  Appended to the table at the end.
- **Revision 4 (2026-09-23)**: folds in the fourth pass ([113-adr-review](../log/113-adr-review.md) §7 —
  4 new red, 3 yellow). Fixed copies are keyed by an **input-set id, not the job id**, and `Request`
  gains record-only origin fields; a press is **a full-draft line before enqueue plus a result line
  after**; first-turn delivery is a meta field the pane reads; TUI candidates are
  `terminalDriver !== false`. Appended to the table at the end.
- **Revision 5 (2026-09-23)**: folds in the fifth pass ([113-adr-review](../log/113-adr-review.md) §8 —
  3 new red, 3 yellow). Origin fields move from `Request` to **the queue record (`jobRec`)**; decision
  9's `press` field list is aligned with the execution order; `InitialPromptState` gains `pending` and
  no resend is offered while delivery is in progress. Appended to the table at the end.
- **Revision 6 (2026-09-23)**: folds in the sixth pass ([113-adr-review](../log/113-adr-review.md) §9 —
  2 new red, 2 yellow). Origins travel as **record-only `JobSpec` fields that `Enqueue` copies into each
  `jobRec`**; at Agent startup a leftover `pending` is reclaimed as `unknown`; the fixed-copy location
  wording; folding duplicate `press_result` lines.
- **Revision 7 (2026-09-23)**: closes with the seventh pass ([113-adr-review](../log/113-adr-review.md)
  §10 — **0 new red**, 2 yellow). Fixed copies and uploads always live in different places; a partial
  trailing JSONL line is truncated by the writer and skipped by readers. The reviewer's verdict:
  presentable as proposed.
- **Revision 9 (2026-09-24)**: the user's decision. Prompts are written differently per model
  (SDXL-family, anima and Qwen-Image are built differently), so **the studio's conversation does not
  start until a model is chosen**. Decisions 2, 3 and 5 changed.
- **Revision 8 (2026-09-24)**: fixes a contradiction the P0 implementation review
  ([114-adr-0100-impl-review](../log/114-adr-0100-impl-review.md) §4 🟡B1, §7 🔵A4) and the P0
  integration found. **muse has no Terminal** (Managed only), so decision 8's "Terminal-only until
  then" does not apply to it: in P0 muse cannot be attached to a studio at all.
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

- The Agent's request vocabulary (`jobs_http.go:28-59`) — provider / op / prompt / negativePrompt /
  size / aspectRatio / background / count / inputs / mask / model / loras / seed / strength / params /
  label / out_dir / jobs / seed_policy / trial / full_steps — is enough. **This ADR adds no word.**
  But `inputs`/`mask` have **no path guard**: `spec()` copies them through and `comfy.go:1193` uploads
  whatever `os.ReadFile` returns. The Files pane reads through the FD-based, symlink-refusing
  `openat2NoSymlinks` (`fs_fd_linux.go:192`) — the model for the guard.
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
- The chat assistant cannot call `generate_image` (the chat-side af server has no `--self-report`, so
  `mcpImageGenEnabled` is false, `mcp_stdio.go:166-170`). On the session side the ui-prefs "image
  generation" toggle arrives as `--image-gen`, and `mcpImageGenAdvertise()` (`:1470`) resolves the
  owning session before advertising.
- Of the Managed turn's `attachments` (`POST /sessions/{name}/turn`), only opencode, codex and muse read
  them; copilot / cursor / kiro / lcpp drop them silently.
- `~/.config/agent-fleet` is on the Files pane denylist (`fs.go:126`) and the workspace policy tells
  agents not to touch it.
- The centre crop of instruction edit is gone (ADR 0094 revision #907, implementation #913, live
  acceptance in [log 112 §14](../log/112-kontext-crop-necessity.md) = #914). The mask canvas can start
  on log 111 §10's design. A mask-by-path field already exists in today's pane (PR #854,
  `GenerateForm.tsx:468-476`; the "cannot press without a mask" check is at `:94-100`).

## Decisions

### Decision 1 — The LLM only edits the draft. The generation path does not change

"Without an LLM in the loop" (0081) still holds for generation. The agent touches the **draft**; a
person presses Generate; what runs is 0081's job queue unchanged. The vocabulary, validation, jobs,
trials, groups, cancel, EMA, sidecars, `props`, usage rows and the CP's seven relay lines stay as they
are. **Two exceptions**: decision 4's guard (`inputs`/`mask` validated and copied at enqueue — the
queue record `jobRec` gains record-only origin fields and "copy before enqueue" enters the
job-building order) and decision 9's `POST …/press` (one more entry point calling the same queue
function). Neither the wire vocabulary nor the provider argument type `Request` changes.

### Decision 2 — A "studio" lives in the Agent, has its own id, and binds one session

The Agent keeps a **studio** (`~/.config/agent-fleet/imagegen/studios/<id>.json`): `id / title /
draft (including provider and model) / locks / session / agent_trial / mask_strokes / created_at /
updated_at`. **The studio is the truth for the draft**; `localStorage` only remembers the last opened
studio id. Versions and the edit log live in a separate file (decision 9).

- Why not the session name: swapping the agent (sonnet→opus, claude→codex), a session too old to
  resume, deleting a session to tidy the list — in every case the draft and versions must survive.
  The studio is primary; the session is "who it is bound to right now".
- The session meta gets a back-reference `Studio` (next to `Origin`). **The studio's `session` is
  the truth of the binding**; the meta is a copy for advertising — the two cannot be written
  atomically, so a tool call is also checked on the studio side ("is the caller the current
  binding?") and refused otherwise. **Fork does not inherit it** (one studio, one session). The field has to be carried through the three `Meta{…}` literals
  (create / fork / recreate), the Agent's `wireSession`, **the CP's `sessionWire`** and the stopped-
  session DB mirror — an unlisted field is dropped silently.
- Keep the body small; write it under an in-memory lock (the chat store's `LockConv` shape) with
  tmp→rename (`fstore` has neither locking nor atomicity). The edit log (decision 9) is a separate file.
- Without a session the pane keeps working on today's `localStorage` draft. **The studio is created
  the moment "attach an agent" is pressed** (revision 2), in this order: ① create the studio (moving
  the `localStorage` draft in; `provider` is the ready row id the pane resolved; **`model` is required** — without one
  "attach" cannot be pressed, revision 9) → ② `GET …/persona` → ③ create the session with `studio` in the request; the Agent writes
  `Studio` into the meta and binds the studio's `session` **before launch**, then delivers
  `initial_prompt`. If ③ fails the studio remains unbound (the draft is not lost). Delivery of the
  first turn (the persona) is reported through **a meta field `InitialPromptState`** (`pending` /
  `delivered` / `failed` / `unknown`) that the pane reads (revisions 4, 5): creation writes `pending`
  and the deliverer writes one of the other three when it finishes. Managed sends inside the create
  handler, so it settles synchronously. TUI delivers from a goroutine independent of the response
  (`session_handlers.go:1050`, `session_io.go:848-920`): it waits up to 30 s for the pane and 30 s for
  the composer before typing, then tries confirmation twice for 12 s each (`session_delivery.go:41-47,
  108-141`) — **the field stays `pending` until that finishes**, the pane shows "delivering the
  persona" and **offers no resend** (flipping to `unknown` on a timer would let the original
  goroutine send later and run the persona turn twice). Only `failed` and `unknown` (finished but
  unconfirmed) show "persona not delivered / unconfirmed — resend" (a resend is the first turn
  again). **At Agent startup every session whose meta says `pending` is reclaimed as `unknown`**
  (revision 6: the delivery goroutine dies with the process while the meta persists on disk,
  `session/meta.go:17-43`; without reclaiming it the pane would say "delivering" forever). A resend
  after reclaiming may deliver the persona twice; the persona is a role statement, harmless when read
  twice, and better than never being deliverable. **Nothing an ADR 0081 user has today is lost.**

### Decision 3 — The contract is four tools on the session-side af MCP server. `generate_image` is not advertised

| Tool | What it does |
|---|---|
| `get_image_studio` | draft, locks, **since last call** (human edits, rewinds, new results — at most 5 plus "N more"), model facts (family, knobs read, sizes, defaults, LoRAs with trigger words), the knowledge "summary" section, version summary. 8 KB cap |
| `set_image_draft` | **partial update**: only the fields written change, `null` clears. **Validation on save is per field** (types, allow-lists, caps, locks, the path guard) and **an incomplete draft is allowed** (an empty prompt can be saved). Whole-request validation (`spec()`'s `bad_prompt` etc.) runs at enqueue and trial time. Locked fields are dropped with a reason. **With no model chosen the whole write is refused** (409 `no_model`, revision 9) |
| `run_image_trial` | **takes no arguments**. Runs the draft stored in the studio (including `provider` and `model` — exactly the row the pane selected; **never** falls back to the Agent's default provider or warm model). Refuses with a reason when `model` is missing ("choose a model"), when `op=inpaint` with an empty `mask`, or when `spec()` fails. One picture, head of the queue, the family's trial steps, at most 3 waiting. **Waits at most 120 s** (a warm engine answers in 8–21 s; shorter than codex's 600 s) and sends the same **10-second progress heartbeat** as `generate_image` (opencode cuts a silent call at 60 s, `mcp_imagegen.go:222-228`): in time it returns path, seed, warnings and elapsed; otherwise the job id and "the result arrives in `get_image_studio`'s since". The job and the version remain and show in the trial slot |
| `add_image_knowledge` | appends to decision 12's "record" section (scope, key, note, evidence). **The key is only the studio's model (scope model) or its family (scope family)**; refused with no model chosen (revision 9) |

- Advertised **only while the owning session is bound to a studio** (`Studio` on the meta that
  `mcpOwningSession()` resolves). tools/list is per request and `list_changed` fires every minute, so
  re-binding takes effect within a minute on kinds that honour the notification (which kinds do is
  unmeasured — the acceptance run measures it). The decision reads only the meta, never `status`,
  so a slow Agent cannot make the tools flicker.
- **`generate_image` is not advertised to a studio session** — `mcpImageGenAdvertise()` excludes it
  when the owning session's meta carries `Studio`. But the call-side check reads **the last
  remembered tools/list** (`mcp_stdio.go:547-565`), so between a re-binding and the next tools/list
  the old set still contains it. **The boundary is a re-check at call time**: before running
  `generate_image` the server re-reads the owning session's meta and refuses with a reason if
  `Studio` is set (exclusion from advertising is what the user sees; the re-check is the guarantee).
  The fingerprint is computed by the MCP child from tools/list (`mcp_stdio.go:445-476`), so the Agent
  has no way to bump it; advertising refresh is what the one-minute watcher gives. **The guarantee covers
  the af path** (enqueuing N pictures into the studio's queue, and `generate_image`); a CLI's own
  built-in image tools (codex's `image_gen` etc., ADR 0069) are outside the advertised set and this
  ADR does not restrict them. **No tool enqueues N pictures.**
  The agent may run a trial because the user's complaint about `generate_image` was "the prompt is
  invisible": with an argument-less tool, what runs is always what is on screen. Per studio, "let the
  agent run trials" (default on).
- Three identity states: owning session **resolved and bound to a studio** → advertise; **resolved
  and not bound** → do not advertise; **unresolvable** (ambiguous cwd guess, decision 8) → advertise
  the studio tools and **refuse with a reason when called** (if they vanished, the agent would only
  say "no such tool").
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
  op, reference and instruction. **Precondition: the enqueue-side guard** (reshaped in revision 3) — a
  string check in `spec()` is not enough on its own, because a symlink can be swapped after the
  check (TOCTOU). And a provider does not necessarily read the file itself: codex hands the reference
  path to a **separate process** via `-i` (`codex.go:173-180`) and agy passes it inside the prompt
  (`agy.go:449-458`), so however the Agent pins its own open, the child opens the original later.
  Therefore **before enqueue** (revision 4: the job id is assigned inside `Enqueue`, `jobs.go:297-327`,
  so copies cannot be per job), each `inputs`/`mask` entry is read through a root-pinned open (the
  `openat2NoSymlinks` shape) and copied into an **input set**
  `~/.cache/agent-fleet/generated/console/inputs/<set>/` (set id assigned by the Agent; an absolute path
  under the generated root; user uploads go to the browse-root-relative `generated/console/inputs/`,
  `fs.go:475-496`, `InputPicker.tsx:16`, which even with the default browse root = home resolves to
  `~/generated/console/inputs/` — **always a different place**, with a different sweep scope). **`Request` (the
  provider argument type, `imagegen.go:53-85`) carries only the copies** (`Inputs`, `Mask`). How the
  origins travel (revision 6): **`JobSpec` gets record-only fields the provider never receives —
  `InputSet`, `InputOrigins`, `MaskOrigin` — and `Enqueue`, when it builds each `jobRec` from
  `JobSpec.Request` (`jobs.go:214-229, 318-326`), copies those three fields into the `jobRec`**. The
  worker hands `jobRec.req` (copies only) to `prov.Generate`; the sidecar is written from `jobRec`'s
  origin fields (today one `Request` value serves both, `jobs.go:477-511`). Origin fields on `JobSpec`
  do not reach the provider; on `Request` they would (revision 5, `jobs.go:426-437`). No provider ever sees the original path (not comfy's pre-read
  `comfy.go:1045`, its upload `:1193`, `openai_compat.go:404`, codex nor agy). Allowed sources are
  **the browse root and the generated root** (the default output lives outside the browse root,
  `store.go:28-34`, so "use as reference" must not refuse the user's own pictures where
  `AF_BROWSE_ROOT` is not home); the denylist is shared with the Files pane. Cleanup: delete
  synchronously when enqueue fails, delete when the group's last job ends (**cancelling a queued job
  or a group bypasses `q.finish`**, `jobs.go:701-726, 781-800`, so both cancel paths get the end check
  too), and delete every set under `inputs/` at startup (the queue is in memory, nothing survives;
  today's sweep looks only at `trial/`, `store.go:170-195`, so this is added). Not opened before the
  guard lands.
- `mask` is person-only (painted by hand). **`needs_mask` is not a stored flag but a derived value**
  (`op=inpaint` and `mask` empty) that `get_image_studio` reports read-only — it clears the moment a
  person places a mask (revision 2). The agent only writes `op=inpaint`; the pane shows the way. In P0
  that way is **the existing mask-by-path field** (PR #854 — a person points at an existing mask
  image); the canvas is P1 (decision 11). So `op=inpaint` can be written in P0, but nothing runs until
  a person has placed a mask (the person's buttons keep stopping on an empty `mask`, as today).
- **Per-field locks** (🔒). Writes to a locked field are dropped with a reason. No automatic locks.

### Decision 5 — The agent pulls context. The Console appends one visible cue line. Nothing is prepended

The persona says "on every message, call `get_image_studio` first". **The persona comes from the
Agent** (`GET /imagegen/studios/{id}/persona`, composed in the user's language); the Console passes it
verbatim as `initial_prompt` in the "attach an agent" create request (in decision 2's order: studio
first, `Studio` on the meta, then the first turn — otherwise `get_image_studio` is not advertised on
that turn). On re-binding it is sent as the new session's first turn; on resume it is not sent. The Console appends one line
(≈30 tokens) at the **end** of the message:
`[studio v4 · draft changed · 2 new results · rewind #9 → get_image_studio]`. The call does not
depend on the cue (a message typed in a Terminal pane has none).

- Why nothing is prepended: there is no single place to insert (`/turn` calls `h.Send` directly), no
  single place to strip (`splitPastedImages` handles only the trailing attachment note; auto-title reads
  the first 400 characters), and it would stay in the transcript and be re-sent every turn (on lcpp,
  most of the window within ten turns).
- **A model switch means a rewrite** (revision 9): when the user changed `model` since the last
  `get_image_studio`, its note tells the agent to rewrite the prompt for the new model (its dialect,
  quality prefixes and knowledge) rather than edit the old one. SDXL-family, anima and Qwen-Image
  prompts are built differently; touching up the old prompt does not get there.
- The cue is added only to messages sent from the mirror's composer (not to memo delivery, peer
  messages or scheduled runs). On TUI it is added **after** `buildImagePrompt` weaves attachment paths
  in, so it stays last.
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
translations stay in i18n). The dialect takes three values — `tags`, `sentences` and `mixed` (tags and
sentences together, Anima) — and the agent also gets `dialect_how`, the dialect as an instruction
(revision 9: `mixed` alone does not say what goes where). `get_image_studio`'s "model facts" come from the same row. This extends
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
  has **a different source per execution method** (revision 2): Managed candidates come from
  `managedDrivers`, TUI candidates from the Console's `repoLaunchKinds` (`agents/registry.ts:760`)
  filtered by **`terminalDriver !== false`** minus shell (the field is optional and absent means
  allowed; only lcpp and muse carry `false`, `registry.ts:134-148, 566, 614` — filtering on truthiness
  or presence would drop claude and agy). A single list would drop claude. **lcpp and muse never appear
  among the TUI candidates** (revision 8).
- **Worktree on by default** is the only way to make cwd-based identity unambiguous for the kinds that
  guess. Off is allowed only for **kind × execution-method pairs where `AF_SESSION_NAME` arrives on
  every path**: Terminal (all kinds) and lcpp. **codex Managed is not one of them** — a fresh thread
  receives it, but a thread resumed after the Agent's daemon was replaced falls back to the cwd guess
  (`mcp_stdio.go:3311-3340`). The launch UI states that a worktree cannot see uncommitted material.
- **opencode Managed and muse are excluded from studios** (opencode is fine in Terminal): opencode's af
  child is shared by every session in the directory, so `set_image_draft` could run from another
  conversation. Delivering `AF_SESSION_NAME` to copilot / cursor / kiro children is P0 prerequisite
  work; until then those kinds are Terminal-only. **muse is Managed-only** (`managedDrivers` in
  `session_turn.go`, `terminalDriver: false` in `registry.ts`) and scrubs its MCP child's environment,
  so the af server answers 401 — with no Terminal to fall back on, **it cannot be attached in P0**
  (revision 8; the Agent refuses the create-time binding and bind with 409
  `studio_kind_unsupported`, and the launch dialog blocks it with the reason). Opening it takes both
  `AF_SESSION_NAME` delivery and an environment that reaches the af child (P1).

### Decision 9 — Three histories: edits, versions, pictures

- **Edit log** `draft_log`: one entry per draft change (time; author = agent turn / human /
  `rewind ← #n`; changed fields with before/after; the full draft at that point). Append-only JSONL in
  a sibling file (`studios/<id>.log.jsonl`). In the transcript the `set_image_draft` tool card renders
  as "draft updated: cfg 7→5" with **"rewind to here"** (`POST …/rewind`; nothing is deleted; the
  next `get_image_studio` reports it in `since`). A rewind is a **person's** action, so it restores
  every field including locked ones while the lock flags stay set (locks guard against the agent).
  The rewind entry's before/after are "now" and "the restored point".
- **Versions**: the draft as it was when a Generate button was pressed (a person's trial or batch, or
  the agent's trial). Appended to the same JSONL as **two independent events**: `kind: "press"`
  (version id, full draft, seed policy, author, time pressed — only what is known before enqueue) and
  `kind: "press_result"` (version id, job/group id, or `error` — what is known after) — pressing twice
  without editing yields two press entries. With a studio, a press is one Console call,
  `POST /imagegen/studios/{id}/press {trial|enqueue…}`. The Agent's order (revision 4): ① **write the
  `press` line** (version id, full draft, seed policy, author, time pressed — everything "restore
  these settings" needs lives here) → ② fixed copies (decision 4) → ③ enqueue through today's queue
  function with `studio` and `version` on the `JobSpec` (the worker may start before the response,
  `jobs.go:297-342`; the sidecar takes `version` from the `JobSpec`) → ④ **write the `press_result`
  line** (version id, job/group id, or `error`). If ④ fails (the enqueue succeeded and the worker is
  running) the response says `recorded: false`, the pane shows that version as "record pending", and
  the Agent retries the write once immediately. Several `press_result` lines for one version id are
  possible (retry, startup reconciliation), so **readers take the first `press_result` per version id
  and ignore the rest**. So that a retry after an append that failed mid-JSON never glues a fragment
  to the new line, **the writer truncates the file back to the last newline before rewriting, and
  readers drop any line that does not parse without counting it** (`studios/<id>.log.jsonl` is new in
  this ADR; no such handling exists yet). A version's state is derived from the presence and
  content of its `press_result`. If the Agent dies after ① a version without `press_result` remains —
  at startup the sidecars are scanned (independently of whether `history.jsonl` exists or is
  truncated): if a picture with that `version` exists a `press_result` is synthesised, otherwise a
  `lost` one is written. Sidecars carry no full draft (`props.go:42-90`; the negative is the composed
  value), so the restore source is always line ①. The vocabulary, validation and queue of
  `POST /imagegen/jobs` are unchanged (decision 1's second exception). Without a studio a press still
  goes to `/imagegen/jobs`. No "pressed" mark is ever added to an edit entry.
- **Picture history**: `GET /imagegen/history?studio=&before=&limit=` backed by
  `generated/console/history.jsonl` (one line appended with each sidecar; rebuilt from sidecars if
  missing). The sidecar and that line carry `studio` and `version` (the press id), so **the link
  between picture and version is durable** across Agent restarts. Actions: "restore these settings"
  (the same `rewind`), "use as reference", "show", "compare" (up to four). Retention as in 0081
  decision 3. **The list and "restore" are P0** (the user's "keep a history" includes pictures);
  "compare" is P1.

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

- Layer 2 is **`~/imagegen-knowledge/{families,models}/<key>.md`** (fixed directly under home;
  outside the denylist; recreate deletes only `~/repos`). **Durability wins over visibility**
  (revision 2): deriving the location from the browse root would delete the knowledge along with a
  browse root pointed under `~/repos`. Where the browse root is not home the folder is not shown in
  the Files pane — there **the pane's notes view edits all four sections** and the agent reads and
  writes through Read/Edit (only direct editing in the Files pane is lost; the "person's Files pane"
  below applies when the browse root is home). One file per model and per family, **four sections** (summary
  ≤1 KB, settings, prompts, record = append-only). The record is written through
  `add_image_knowledge`; the other sections through the agent's Edit and the person's Files pane.
  **Only a person deletes.** The agent writes only when told "remember this" or when the person judges
  a result. `get_image_studio` returns the summary every time; the rest on request.
- Sharing into a repository is an **explicit export** (a per-studio write target inside the repository
  was rejected: it pollutes other sessions' `git status`).
- **Later, add the folder to ADR 0022's snapshot roots** (the user's request: "diff-managed in git,
  like claude's memory"). The **machinery** for diff, restore, export and import is reusable, but 0022's
  glob allowlist, symlink exclusion, import range check and full-history secret scan need **this root
  declared** (take only `families/*.md` and `models/*.md`, define the restore scope and the UI name) —
  filed as a P2 revision of 0022.

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

- **Agent**: `internal/imagegen/studio.go` (store, lock, JSONL edit log and press events, rewind,
  knowledge read/write, trial endpoint, persona endpoint), the fixed copy at enqueue (root-pinned
  open, two allowed roots, input sets, `jobRec` origin fields, cleanup including the cancel paths),
  the exclusion in `mcpImageGenAdvertise` plus the call-time re-check, the meta's
  `InitialPromptState`, four `comfyFamilyRow` fields and `modelStatus`,
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

- **P0 prerequisites**: ① the `inputs`/`mask` guard (root-pinned open and fixed copy at enqueue, every
  provider) — precondition for decision 4's `op`/`inputs` opening; ② the cue-line strippers
  (transcript model layer and Go) — precondition for emitting the cue. `AF_SESSION_NAME` delivery for
  copilot / cursor / kiro is **not a P0-wide prerequisite but the condition for opening that
  kind's Managed mode to studios** (Terminal-only until then) — claude TUI, fresh codex Managed,
  Terminal of every kind and lcpp close the P0 loop on their own.
- **P0**: decisions 1–5, 7, 8, 10, 12 (layer 2), and decision 9's edit log, press events and **the
  picture-history list with "restore"**. The three-column pane. Removal of layer B. Instruction edit
  (with references) and inpaint with a mask placed through the existing path field arrive with
  decision 4's opening. **The P0 agent does not see pictures** (the person's observations travel as
  words) — showing is P1 (decision 6).
- **P1**: decision 9's "compare", decision 6's "show", opening copilot / cursor / kiro / muse Managed
  (for muse, also an environment that reaches the af child — revision 8),
  the model proposal card, the wand icon, the ledger `Ref`, the lcpp system-prompt persona, draft
  save/load as files, **decision 11's canvas** (log 111 §10; ComfyUI families only; acceptance once each
  on Chromium and iOS Safari; the `openai_compat` path stays unmeasured and out of scope).
- **P2**: sweeps from the conversation (a proposed matrix the person enqueues), promotion to layer 1,
  **git diff management of knowledge** (filed as an ADR 0022 revision), a claude Managed driver
  (separate ADR).

## Unresolved

1. **Which kinds honour `list_changed`** (unmeasured beyond claude). Where it is not honoured,
   re-binding and the trial toggle wait for a resume. Measured in the P0 acceptance run.
2. **The trial cap of 3 is workspace-wide.** Whether to limit the agent to one trial per studio; the
   429 wording needs studio context; codex's 600 s MCP limit is short of a cold engine's 16 minutes.
3. **Tier-2 (workspace stop) fires during generation** — the reaper does not see imagegen jobs. A hole
   since 0081; filed separately.
4. **The summary cap (1 KB) and the 8 KB `get_image_studio` cap are guesses.** Tuned on the live run.
5. **A CLI's built-in image tools other than `generate_image`** (codex's `image_gen`, agy) remain
   usable in a studio. Restricting them is a per-kind configuration matter, outside this ADR.
6. **Repair when only one side of studio/meta got written** (decision 2): the studio is the truth and
   the two are reconciled at startup. The procedure is decided in implementation.

## Changed in revision 1 (2026-09-23, [113-adr-review](../log/113-adr-review.md))

| Finding | Change |
|---|---|
| 🔴1 an empty draft cannot be partially updated | decision 3: per-field validation on save, whole-request validation at enqueue/trial |
| 🔴2 the argument-less trial is not guaranteed to match the screen | decisions 2, 3: the draft carries `provider` and `model`; no fallback to defaults; refusal conditions and the timeout answer defined |
| 🔴3 wiring and scope of hiding `generate_image` | decision 3: exclusion in `mcpImageGenAdvertise`; the guarantee covers the af path; CLI built-ins are unresolved 5 |
| 🔴4 "version = pressed mark" conflicts with append-only JSONL | decision 9: press events are independent entries |
| 🔴5 no durable picture history in P0 | decision 9, phases: list and "restore" in P0; picture↔version link durable via sidecars |
| 🔴6 worktree off is ambiguous on codex resume | decision 8: off only for Terminal and lcpp |
| 🔴7 no persona injection contract | decision 5: the Agent composes it, the Console passes `initial_prompt`; re-binding sends it as the first turn |
| 🔴8 the guard lacks symlink/TOCTOU handling | decision 4, prerequisite ①: root-pinned open where the file is read |
| 🔴9 `op=inpaint` writable in P0 but the mask UI is P1 | decision 4: P0 uses the existing mask-by-path field; canvas in P1 |
| 🟡1–12 | cue scope; rewind vs locks; binding truth; three identity states; vocabulary list; line refs; browse root; prerequisites regrouped; the P0 agent does not see pictures; canvas acceptance; 0022 revision; direct reference to log 112 §14 |

## Changed in revision 2 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §5)

| Finding | Change |
|---|---|
| 🔴A right after re-binding the old advertised set still allows `generate_image` | decision 3: re-read the owning session's meta at call time and refuse; exclusion is UX, the re-check is the guarantee |
| 🔴B the root-pinned open covered only comfy's upload | decision 4: every request-path read goes through one helper; pinned by an AST test |
| 🔴C an answer after the 600 s cap never reaches the client | decision 3: wait at most 120 s, then return the job id; the result arrives in `get_image_studio`'s since |
| 🔴D nothing clears `needs_mask` | decision 4: derived, not stored (`op=inpaint` and empty `mask`) |
| 🔴E a press line cannot hold the id and the failure | decision 9: one `POST …/press` request enqueues first, then writes (decision 1's second exception) |
| 🔴F studio creation vs `initial_prompt` order | decisions 2, 5: create the studio on "attach" → persona → create the session with `studio` → bind before the first turn |
| 🔴G a kind list from `managedDrivers` alone drops claude | decision 8: separate sources for Managed and TUI |
| 🟡A–C | mask field line ref; knowledge root fixed under home (durability first); on migration `provider` is the resolved row id and an empty `model` only refuses the trial |

## Changed in revision 3 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §6)

| Finding | Change |
|---|---|
| 🔴H a reference path handed to a CLI child bypasses `openRequestFile` | decision 4: copy at enqueue; no provider (child processes included) sees the original path; enforced by type |
| 🔴I 120 s exceeds opencode's 60 s cap | decision 3: the trial tool sends the 10-second heartbeat too |
| 🔴J a press line written after the response cannot reach the picture | decision 9: reserve the version id before enqueue and carry it on `JobSpec`; synthesise on recovery |
| 🔴K a repo browse root refuses the user's own pictures as references | decision 4: allowed sources are the browse root plus the generated root |
| 🟡E–I | fingerprint is computed by the child → rely on the watcher; the notes view edits everything when the root is not home; TUI candidates from `repoLaunchKinds` with `terminalDriver`; first-turn send failure → `warning` + resend; decision 1 has two exceptions |

## Changed in revision 4 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §7)

| Finding | Change |
|---|---|
| 🔴L per-job copies cannot exist before the id is assigned; provider and record share one field | decision 4: input-set id, copied before enqueue; `Request` gains record-only origins (`InputOrigins`, `MaskOrigin`); named in decision 1's exception |
| 🔴M a `recovered` line cannot be built from sidecars | decision 9: a press is a full-draft line before enqueue plus a result line after; restore always reads the former; sidecars scanned at startup |
| 🔴N a TUI first-turn failure cannot ride the create response | decision 2: the deliverer writes `InitialPromptState` on the meta; the pane reads it (resend on `unknown` after 60 s) |
| 🔴O filtering on `terminalDriver` truthiness drops claude and agy | decision 8: `terminalDriver !== false` minus shell |
| 🟡J–L | copy cleanup (synchronous on failure, at group end, all at startup); absolute path under the generated root; ja/en parity confirmed |

## Changed in revision 5 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §8)

| Finding | Change |
|---|---|
| 🔴P origin fields on `Request` reach the provider | decisions 4, 1, consequences: origins on the queue record `jobRec`; `Request` carries copies only |
| 🔴Q the `press` field list still holds post-enqueue values | decision 9: fields split between `press` (pre-enqueue) and `press_result` (post-enqueue) |
| 🔴R resending on a 60 s `unknown` during TUI delivery doubles the turn | decision 2: `pending` added; no resend until delivery finishes |
| 🟡M–O | consequences aligned with the type; cleanup covers both cancel paths; a failed `press_result` write answers `recorded: false` and shows "record pending" |

## Changed in revision 6 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §9)

| Finding | Change |
|---|---|
| 🔴S no path for the origins to reach `jobRec` | decision 4: record-only `JobSpec` fields (`InputSet`, `InputOrigins`, `MaskOrigin`) that `Enqueue` copies into each `jobRec` |
| 🔴T a restart leaves `pending` forever, no resend | decision 2: reclaim `pending` as `unknown` at startup; a doubled persona is harmless |
| 🟡P, Q | the parents coincide only when the browse root is home; duplicate `press_result` lines fold to the first per version id |

## Changed in revision 7 (2026-09-23, [113-adr-review](../log/113-adr-review.md) §10, 0 red)

| Finding | Change |
|---|---|
| 🟡R even the default browse root puts fixed copies and uploads under different parents | decision 4: corrected to "always a different place" (`~/.cache/agent-fleet/generated/…` vs `~/generated/…`) |
| 🟡S a retry after a partial append corrupts a JSONL line | decision 9: the writer truncates to the last newline on failure; readers drop unparsable lines |

## Changed in revision 8 (2026-09-24, [114-adr-0100-impl-review](../log/114-adr-0100-impl-review.md) §4, §7)

| Finding | Change |
|---|---|
| 🟡B1 muse is not "Terminal-only" but unattachable in P0 (contradicting decision 8's own "only lcpp and muse carry `false`") | decision 8: states that lcpp and muse never appear among the TUI candidates; adds muse to the excluded Managed kinds with the reasons (Managed-only; its scrubbed MCP environment cannot reach the af server) and the P1 condition for opening it. Phases: muse leaves the prerequisite line and P1 gains the condition |
| 🔵A4 the refusal for muse tells it to use Terminal | decision 8: names the Agent's refusal (409 `studio_kind_unsupported`) and how the launch dialog blocks it (the Agent's wording already says "cannot be bound for now", PR #926) |

## Changed in revision 9 (2026-09-24, the user's decision, PR #932's walkthrough)

| Trigger | Change |
|---|---|
| In the walkthrough the conversation went ahead with no model, and a "remember this" record used the provider's name (`agy`) as a family | decision 2: attaching needs a model (the Console disables the button, and holds a bound agent's composer with the reason if the model is cleared later). decision 3: `set_image_draft` answers 409 `no_model` with no model; `add_image_knowledge` keys only to the studio's model or its family (else 409 `wrong_key`; the member's records from the notes are not checked) |
| Anima is controlled more finely with sentences alongside the tags (the user) | decision 7: a `mixed` dialect, and Anima uses it; the agent gets `dialect_how` (tags for the subject and attributes, sentences for composition, positions and light) |
| Prompt structure differs by family | decision 5: when the user switches the model, the next `get_image_studio` note tells the agent to rewrite for the new model |

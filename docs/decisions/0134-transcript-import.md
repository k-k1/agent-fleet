# 0134. Import an external Claude Code / Codex transcript by dropping it on the Console: a quarantined upload, a durable import job, and a handover file

English | [日本語](0134-transcript-import.ja.md)

- Status: **proposed** (2026-11-10). Nothing is built. Limit values below are provisional.
- Tracking: #3029. Follow-ups outside this record: #3030 (upload from the external CLI), #3031
  (native resume research), #3027 (envelope on `create_session`).
- See also: [0115](0115-move-session-to-another-agent.md) (the handover this record reuses) /
  [0059](0059-repo-import-jobs.md) (the job shape) /
  [0039](0039-fork-at-message.md) (fork, the same-kind alternative) /
  [0113](0113-member-origin-mark.md) (the member-origin mark an upload route can require)
- Code references are `path:line` as of `main` at `fc11d17ac`. Paths are relative to the repository
  root; `workspace/agent/` is shortened to `agent/`.

## Context

A member who ran a Claude Code or Codex session on their own computer can continue it in the
workspace today only through the summary hand-off over `/mcp` (`create_session` with
`initial_prompt`, `guide/member/19-outside-access.md`). That carries what the outside model chooses
to write. ADR 0115 already turns a session's transcript into a file the target reads
(`Handover`, `agent/internal/sessionx/session_handover.go:104`), without a model in between. #3029
asks for the same with an external file as the source: the member drops the transcript on the
Console.

The source is **a file written on another machine by someone we cannot vouch for**. It may be a
real transcript of the member's, edited, or built to look like one. 0115 never had that problem: its
source is a session of this Agent. A three-round design review of a broader design (it also had an
external-CLI upload and native import) left six requirements that this record turns into decisions:
untrusted input, hard streaming limits, a durable job, storage and cleanup, secrets, cancellation.

### What the code does today, and why each point matters

- **Both normalizers take the whole file as `[][]byte` and have no context.**
  `claude.CollectAllTurns(lines [][]byte)` (`agent/internal/agents/claude/transcript.go:496`) calls
  `collectTurns` (`:534`) and `RewoundPrompts` (`agent/internal/agents/claude/rewound.go:17`), two
  more whole-file passes. Codex `parseRolloutFull(lines [][]byte)`
  (`agent/internal/agents/codex/transcript.go:109`) is unexported and feeds a `rolloutParser` line by
  line (`:172`). Neither can be cancelled or bounded in time.
- **Both drop records silently.** Codex `feed` returns on a line that does not decode (`:187`) and
  drops every message whose role is not `user` or `assistant` (`:658`, "developer/system
  instructions — noise"). Claude's `parseTurn` (`claude/transcript.go:692`) returns `ok=false` for
  tool results, summaries and bookkeeping. For a session of this Agent that is the right filter; for
  an upload it means a record can be refused or dropped with nobody told.
- **Reading is unbounded.** The claude reader is `os.ReadFile` plus a split
  (`claude/transcript.go:290`). A codex rollout "reached 147 MB in one hour" and one full parse
  "measured 3.9 s of CPU" (`codex/transcript.go:124-125`); that is the only parse-cost measurement we
  have, it is codex's, and it was taken on a trusted file.
- **0115's size target is soft.** `handoverSizeTarget = 256 << 10` (`session_handover.go:34`):
  user messages, the last 3 turns and the leading sections are never cut, so the file "can still
  exceed the target". `RenderHandover` (`:367`) builds the full string first and cuts afterwards.
  That is a quality knob, not a limit.
- **Secrets are scrubbed once, at render.** `renderHandover` takes `sc := bridge.ScrubSecrets`
  (`session_handover.go:478`). `ScrubSecrets(s string) string` (`agent/internal/bridge/fulltext.go:104`)
  returns text only and is documented "best-effort" (`:101`); it cannot say what it found.
- **`fstore` cannot carry a durable record.** `Store.Write` stages a temp file and renames it
  (`agent/internal/fstore/fstore.go:43`) with no `Sync` of the file or the directory; `Read` returns
  `ok=false` for a missing file, an empty file and a decode failure alike (`:67`). Both are right for
  its callers (a status hook that writes often and treats "no record" as idle) and wrong for a job
  that must survive a crash and must not read an unreadable record as absent. 0115 wrote its own
  synced writer for the handover file (`session_handover_file.go:152`) for the same reason.
- **0059's job is in-memory plus a marker.** `repoJobs` is a map (`agent/repo_jobs.go:106`); a marker
  under `~/repos/.af-repo-jobs` (`:116`) turns a survivor into `interrupted` at boot (`:149`). That
  suffices for a clone. It does not for a job that ends in a session: the marker dies with
  "recreate" (which deletes `~/repos`), and it records nothing about a created target.
- **`~/.af-work` is for sessions, not for jobs.** The boot cleanup walks `~/.af-work` and treats a
  directory no session is named after as a leftover: removed at boot when it carries
  `paths.SessionWorkDirMarker`, offered for removal in Settings when it does not
  (`agent/leftovers.go:406`; marker `agent/internal/paths/paths.go:268`). A job directory there is
  deleted or flagged for reasons unrelated to the job.
- **The existing upload route validates nothing.** `fsx.HandleUpload` (`agent/internal/fsx/fs.go:595`)
  writes the file into a browse-root folder with a per-file size cap (64 MiB,
  `fs.go:565`). The result is a user-visible file in a working copy; nobody parsed it.
- **A precedent for the transport exists.** `memberorigin.RequireUpload`
  (`agent/internal/memberorigin/memberorigin.go:255`) spools a multipart body to a file while
  hashing it, verifies a Control-Plane-signed mark over the hash, and refuses an unmarked request
  before writing anything. The memory-bundle import uses it
  (`agent/internal/memoryx/memory_handlers.go:247`); the Control Plane spools at most 130 MiB of an
  upload (`control-plane/member_origin.go:66`, which is why the Agent's own ceiling is 128 MiB,
  `agent/internal/memoryx/memory_import.go:53`).

## Decisions

### 1. Where it runs, and what carries it

1. **The Agent parses, renders and owns the job. The Control Plane only relays.** The normalizers live
   in the Agent module; the Control Plane never decodes the transcript. This is also why the Control
   Plane's upload limit (130 MiB, `member_origin.go:66`) is a hard ceiling on decision 5's body limit.
2. **The upload is a Console-only route.** `POST /api/transcript-imports` is added to
   `multipartUploadRoutes` (`control-plane/main.go:960`, "a new upload route has to be added here, or
   it is refused with 415"), relayed with `sign: signHash` (`control-plane/relays.go:34`, the way
   `relays_memory.go:114` relays the memory import), and registered on the Agent behind
   `memberorigin.RequireUpload`. So is **confirm** (`signBody`, as `relays_memory.go:115`). The Agent's
   HTTP API is reachable from any session shell in the container; the mark is what stops a session
   (or a process a session started) from importing a file or confirming one. This is a CP-issued
   mark, not a capability token handed to the member, so "no separate token" in #3029 holds.
3. **Rejected: writing the upload through `POST /api/fs/upload`.** It lands in a folder the member and
   every session can read and edit (`fs.go:595`), with no validation, and nothing would own its
   deletion.
4. **Rejected: parsing in the Control Plane.** Wrong module, and the CP would hold decoded untrusted
   content for every tenant.
5. **Rejected: one synchronous upload-and-create request.** The same reason as 0059: the response
   would outlive the ingress idle timeout, and closing the tab would lose the outcome.

### 2. Untrusted input (requirement 1)

1. **Quarantine.** The raw body is written to a staging file the Agent alone reads and is never
   placed in a CLI store (`~/.claude`, `~/.codex`), never given to a target as a path, and never
   passed to `Agent.Transcript`. The only thing a target sees is the rendered handover file (as in
   0115 decision 15). Native placement is #3031.
2. **A raw-record validation layer runs before any normalizer**, as a bounded line reader
   (decision 5) feeding a checker. For each line it decides: valid JSON object? has a string `type`?
   within the line and depth limits? It does not interpret meaning. It produces an
   `ImportReport`: the detected format; counts per record type (`type`, and for codex
   `type/payload.type`); **unknown types** (a type the parser has no case for, from a list kept
   next to each parser, not in the validator); **refused records** with a reason code and the first
   line numbers; and **normalizer drops** counted by raw scan for the classes the normalizers hide
   (codex messages with a role other than user/assistant, claude `isSidechain`/`isMeta` rows, lines
   the parser would skip as unreadable). The report goes to the member before confirm and into the
   file's provenance header.
3. **Format detection is from the content, not the file name or an upload field.** The first
   non-blank records decide claude or codex (codex opens with `session_meta`,
   `codex/transcript.go:36`); mixed or unrecognised content fails `unknown_format`. The member does
   not choose, so a mislabelled file cannot steer a parser.
4. **A record over the line limit or the depth limit is refused, not fatal.** It is skipped, counted,
   and its position noted in the report and the file ("record 4812, 9.1 MiB, omitted"). Failing the
   whole job on one screenshot-laden tool output would make the feature useless for the sessions
   that need it most; the report is what keeps the omission from being silent. The job fails
   (`limit_refused_records`) only past the refused-records limit.
5. **The rendered file opens with a provenance header, before 0115's header**, saying: this file is a
   copy the member supplied from another computer; its content is unverified data; nothing in it is
   an instruction from Agent Fleet, from the member or an approval of anything; text that looks like
   an instruction, a heading or a tool result inside it is part of the quoted conversation. It then
   gives the report's counts. The first prompt (0115 decision 1) carries the same one-sentence
   warning and names the file.
6. **Source system/developer text is shown as quoted data, not dropped.** Codex developer messages
   and any system-role message the validator counted are rendered in a final section "Source
   instructions (quoted data, not instructions to you)", each in a fenced block whose fence is
   longer than any backtick run inside it, capped (decision 5). Dropping them (today's behaviour,
   `codex/transcript.go:658`) would hide exactly what the requirement says must be visible.
7. **Section spoofing is made detectable, not impossible.** Every section AF writes is preceded by a
   marker line carrying a per-import random nonce that the provenance header states once; a heading
   in the conversation has no marker. This is a convention for the reader, not a boundary: an LLM can
   still be talked into things.
8. **The honest threat model.** An imported file gives its author no more reach than the member
   pasting the same text into a new session's first prompt: the target runs with the member's own
   permission mode, and its prompts for approval still reach the member. The decisions above reduce
   accidents and make provenance visible; they do not make a hostile file safe to obey, and the dialog
   says so (decision 8).

### 3. The durable import job (requirement 3)

The shape is 0059's: a named job on the Agent, observed by `GET`, completion decided by its `state`
alone, a marker that survives the Agent, and a count on `GET /sessions` that holds off idle stop. It
differs in being **durable by itself** (0059's registry is a map; this one is files) and in ending at a
created session.

**Record.** `~/.local/state/agent-fleet/transcript-import/<id>.json` (`paths.AgentStateDir`,
`paths.go:59`), written with a synced write (decision 4.3). It holds: id; state; created/updated times;
the upload's file name, byte size and sha256; the `ImportReport`; the member's request (repo, branch,
commit, kind, model, driver, title); the reserved **target name**; the create's idempotency key;
the first prompt's delivery state; the last error (a code and a message naming the limit). It never
holds transcript content.

**Id and names.** `imp-` plus 12 characters from the `[a-z2-7]` alphabet. It must satisfy
`session.ValidName` (`agent/internal/session/session.go:207`, `^[A-Za-z0-9_-]{1,40}$`), because
`WriteHandoverFile` (`session_handover_file.go:31`) and the handover-file pattern (`:23`) require the
"source" part of `handover-<source>.md` to. The handover file is `handover-<id>.md`.

**States.**

| State | Meaning | Terminal |
|---|---|---|
| `uploading` | body being received into staging | no |
| `queued` | received and hashed, waiting for the one parse slot | no |
| `parsing` | validate, normalize, render the candidate (cancellable) | no |
| `review` | candidate and report ready; waits for the member | no |
| `creating` | name reserved; worktree and session being created | no |
| `delivering` | session exists; first prompt not yet known to have arrived | no |
| `prompt_delivered` | the target's `InitialPromptState` is `delivered` (`session.go:877`) | yes (success) |
| `unknown` | outcome of `creating` or `delivering` cannot be told | no — kept until resolved |
| `failed` | a named reason; the message names the limit that tripped | yes |
| `canceled` | the member cancelled | yes |
| `expired` | `review` not confirmed within its TTL | yes |

Transitions:

```
(drop) -> uploading -> queued -> parsing -> review -> creating -> delivering -> prompt_delivered
              |           |         |         |          |            |
              |           |         |         |          +--> unknown <+  (restart, timeout, uncertain create)
              v           v         v         v                 |   ^
            canceled    failed    failed   expired/canceled     |   +-- check -> delivering | prompt_delivered | creating(retry, only if provably absent)
        (abort)                                                 +------> failed(abandoned) (abandon, any unknown sub-case, member's explicit word; never touches the target)
 cancel (route): allowed from queued, parsing, review -> canceled. An aborted upload is canceled (reason upload_aborted).
 cancel: refused from creating, delivering, unknown (the target may exist; see 3.5).
```

1. **`unknown` is a state, not a failure.** It is entered whenever the Agent cannot say whether the
   session exists or whether the prompt arrived: the Agent stopped during `creating`/`delivering`, a
   create timed out, the create's idempotency record is `unknown`
   (`agent/internal/sessionx/session_idempotency.go:23`), or the target's meta exists and cannot be read (3.4). A job *record* that cannot be read is not `unknown`: it is the separate `unreadable` row of 3.4.
   The Console shows it as "needs your attention", never as a failure, and never auto-retries it.
2. **The target name is reserved before creation and reused on retry.** At the `review`→`creating`
   transition the Agent allocates the target name (`allocSessionName`), writes it into the job
   record (synced), and only then creates, passing it as the create request's name
   (`session_create.go:566`; that path already refuses a name that has a meta,
   `:571`). The name is held against every allocation while the job is non-terminal, in memory and
   from the record after a restart, as 0115 holds a move's booked name
   (`agent/internal/sessionx/session_move.go:504`).
3. **The create is also covered by the durable idempotency ledger.** The job passes
   `IdempotencyKey: "transcript-import:<id>"` with `idempotency_durable`
   (`session_idempotency.go:14`, `session_create.go:93`). That answers "did this create run" across a
   restart ("done", "refused" or "unknown"), and only a record that proves absence ("reserved",
   "abandoned", "refused") lets the job create again. The ledger covers the create only; parse,
   review and delivery need the job record.
4. **Recovery after an Agent restart, at boot** (the analogue of `sweepRepoJobMarkers`,
   `repo_jobs.go:149`, but over files):
   - `uploading`: the body is partial; → `failed` (`interrupted`), staging removed.
   - `queued`, `parsing`: re-queued when the raw file exists and matches the recorded size and sha256
     (the record is written only after the body is synced), otherwise `failed` (`interrupted`).
   - `review`: unchanged.
   - `creating`, `delivering`: → `unknown`, then reconciled by looking at the target, in this order:
     read the target's meta. **A meta that exists and reads** is adopted: the job moves to
     `delivering` or `prompt_delivered` by its `InitialPromptState`. **A meta file that exists and
     does not read** keeps `unknown` and refuses retry (ADR 0115's addendum: "a meta file that exists and does not
     read is never taken for absent"). **No meta file and no pane or
     runtime under the name** lets the job offer "retry", which creates again under the same name.
     **No meta but a pane or runtime** is an orphan: the member is shown it and may stop it
     (the ordinary stop); `check` then finds nothing and offers "retry", or the member abandons the
     job (3.5).
   - A job *record* that exists and does not parse is not a state: it is listed as `unreadable`,
     never retried and never deleted by the sweep; the member dismisses it explicitly (`DELETE`).
     This is separate from the target's meta above, which keeps the job `unknown`.
5. **Cancel and retry.** Cancel reaches `queued`, `parsing` and `review` (decision 7; an upload is cancelled by aborting it).
   Once a name is reserved the target may exist, so cancel from `creating`/`delivering`/`unknown` is
   refused with the target's name; the member stops or deletes that session the ordinary way
   (the same stance as 0115's "cancel refuses while the target exists"). Retry exists only for
   `creating`/`unknown` with the target provably absent, and for a first prompt that was not
   delivered (`deliver`, below). **`unknown` always has a way out: `abandon`** (`DELETE` with
   `abandon=1`, marked) ends the job as `failed` (`abandoned`) in every sub-case, including an
   orphan and an unreadable meta, on the member's explicit word after the dialog has named the target.
   It deletes nothing and stops nothing; the name stays held while any meta or pane carries it, and
   the record then follows the 7-day retention. Without it three `unknown` jobs would fill the
   pending limit (decision 5) for good. A `failed` parse is retried by dropping the file again: the raw body
   is already gone (decision 4.4).
6. **The first prompt is never resent by itself.** As in 0115 (`session_move_deliver.go:30`), a
   delivery of unknown outcome stays `delivering` until the member presses "send again"; an unknown
   delivery may have arrived, and sending twice makes the target act twice.
7. **The session starts where the member said, and the prompt waits for proof.** The member picks the
   repository, branch and commit; the transcript's `cwd` and `gitBranch` are suggestions only. The
   Agent resolves the commit to a full SHA (fetching once if it does not resolve), creates the
   worktree from that SHA, and **checks `HEAD` equals it before the first prompt is typed**. A
   mismatch fails the job with `head_mismatch`; the session is left, named in the error. This is
   needed because `gitx.NewWorktreeStartPoint` keeps a local branch of the same name as is and does
   not fetch (`agent/internal/gitx/git.go:932`, "A local branch, tag, commit or full ref keeps the start point the bare name
   has always had"), so a branch name alone can start on a stale tip. The create therefore does not
   carry `initial_prompt`; the held prompt is delivered by the job after the check, through the
   mechanism 0115 uses for a move target's first prompt (`moveDelivery`, recorded by target until
   delivered, `session_move_deliver.go:30`). Phase 4 generalizes that record rather than copying it.
8. **Idle stop.** `uploading`, `queued`, `parsing`, `creating` and `delivering` are running work.
   They are counted in a new `importJobs` field on `GET /sessions` (`agent/internal/sessionx/session_handlers.go:280`,
   next to `repoJobs`; the Agent's `deps` carries it like `RepoJobsRunning`, `deps.go:64`), read by
   the Control Plane (`control-plane/agent_client.go:303`, `jobsRunning()` at `:322`), and shown as an
   idle-stop holder of its own kind (`control-plane/idle_forecast.go:90`, where `repojob` is). `review`
   is **not** running work: nothing executes, the raw body is gone, and a record waiting up to a day for
   a person must not keep a workspace warm (0059 refused the same thing for the Console's polling).
   The staged candidate is under the state directory, which a stop keeps.

### 4. Storage and cleanup (requirement 4)

1. **Layout.** The job record is `~/.local/state/agent-fleet/transcript-import/<id>.json`. The staged
   data is `~/.local/state/agent-fleet/transcript-import-data/<id>/` (mode 0700), holding `raw`
   and `candidate.md` (each 0600). Neither is under `~/.af-work` (boot cleanup, above), nor `~/repos`
   ("recreate" deletes it; a job outliving its working copies is the point), nor `/tmp`. The state
   directory is excluded from the session scan by construction. Both subdirectories join the list in
   `agent/internal/statemig/statemig.go:62` (`Entries`, an allowlist; `statemig_drift_test.go` fails when a store under `AgentStateDir` is not named there).
2. **Never logged.** No line of the body, no record content and no file name beyond the sanitized base
   name (`filepath.Base`, as `fs.go:647`) is logged or audited. The audit entry carries the id, the
   byte size and the outcome.
3. **`fstore` gains two additive methods, and its defaults do not change.** `WriteSynced` (file `Sync`,
   rename, directory `Sync`; every error returned) and `ReadChecked` returning
   `found | missing | unreadable`. The seven existing families keep `Write`/`Read`: syncing every
   `session-status` write would cost them for nothing, and changing `Read`'s contract would
   silently reclassify unreadable records they rely on today as absent-or-present. The job store is
   the first user.
4. **Raw body lifetime.** Written 0600 during `uploading`, hashed while written, and synced before
   the record moves to `queued`. **Deleted as soon as `parsing` has written `candidate.md`** (synced)
   — earlier than the issue's "once the handover is written", because the candidate is the
   scrubbed and bounded form and nothing after the review needs the original. A job that fails or is
   cancelled removes its raw body in the same step. A hard backstop: a boot sweep and an hourly timer
   remove any `raw` older than 2 hours whatever its job says, and any data directory with no record.
   `parsing` starts by checking the staged file against the recorded size and sha256; a mismatch is
   `failed` (`interrupted`) with the staging removed. The 0700/0600 modes keep other users out, not
   other processes of the Agent's uid (a session shell): that is the limit ADR 0113 documents for
   `RequireUpload`'s spool (`memberorigin.go:250`), inherited here, and the re-check turns a rewrite
   between hashing and parsing into a visible failure rather than a parsed substitute.
   Consequence: an edit of the choice of target after review does not re-render (4.5 keeps
   the candidate target-independent).
5. **The candidate is target-independent; the final file is assembled at write time.** It holds the
   provenance header, the 0115 sections that do not depend on the target, the conversation and the
   quoted source instructions. At confirm the Agent adds the target-dependent lines (0115 decision 14's
   project-instruction-file line) and writes `~/.af-work/<target>/handover-<id>.md` through
   `WriteHandoverFile` (`session_handover_file.go:31`: complete and synced before it returns).
6. **The handover joins the target's lifecycle for free, and a test pins it.** The file name matches
   the handover pattern (`session_handover_file.go:23`), so trashing the target archives it
   (`agent/cleanup_ops.go:395` → `ListHandoverFiles`, `:60`), restoring puts it back, and a later
   move from the target copies it forward (`CopyHandoverChain`, `:105`). Phase 4 adds the test that an
   imported target survives trash/restore and an A→B chain; it does not add code for it.
7. **Retention of the job record.** A terminal record is kept 7 days (the value of
   `archivedScratchTTL`, `leftovers.go:393`) so the outcome can be read later, or dismissed sooner.
   `unknown` and `unreadable` records have no TTL. `review` expires after 24 hours (→ `expired`,
   candidate removed).

### 5. Hard limits, enforced while streaming (requirement 2)

Separate from 0115's soft 256 KiB target, which keeps its meaning (a quality cut). **Every value is a
named constant in one file** (`agent/internal/transcriptimport/limits.go`, Phase 1), never an inline
number, so that one edit revises them after the first live runs. A tripped limit fails the job with a
code `limit_<name>` and a message that names the limit, its value and what was seen.

| Limit | Provisional value | Enforced | Over the limit |
|---|---|---|---|
| Body size | 64 MiB (ceiling 128 MiB; the CP spools ≤130 MiB) | counting reader on the upload; CP and Agent both cap | `limit_body_bytes`, 413 before staging completes |
| Upload receive time | 10 min | request deadline | `limit_upload_time` |
| Line length | 8 MiB | bounded line reader; the rest of the line is discarded unread | line **refused** (2.4), not fatal |
| Line count | 200,000 | counted as read | `limit_lines` |
| Nesting depth | 64 | string-aware scan of each line, no decode (5.1) | line **refused** |
| Refused records | 1,000 | counted | `limit_refused_records` |
| `ImportReport` size | 200 distinct record types, 64 characters per type name, 20 line numbers per refusal reason | counted as built; the rest folds into an `other` count | folded, not fatal |
| Parse time (validate + normalize + render) | 60 s | `context.WithTimeout`, checked every 256 lines and in the render loops | `limit_parse_time` |
| Rendered output (hard) | 8 MiB | counting writer under the render builder: the build stops at the limit | `limit_rendered_bytes` |
| Quoted source-instructions section | 32 KiB | cut with a visible marker | cut, not fatal |
| Concurrent parses per workspace | 1 (others `queued`) | semaphore | queue |
| Queued + parsing jobs | 3 | admission | 429 `limit_queue` |
| Staged bytes per workspace | 256 MiB | a reservation taken under one lock at admission (the declared length, or the body limit when none), trimmed to the real size when the body ends and released with the data; also checked while receiving | `limit_staged_bytes` |
| Non-terminal jobs per member (a workspace is one member's) | 3 (`review` counts, `unknown` counts) | admission | 429 `limit_pending_jobs` |
| `review` TTL | 24 h | sweep | `expired` |
| Raw body TTL (backstop) | 2 h | sweep | file removed |
| Terminal record retention | 7 days | sweep | record removed |

How they are held:

1. **One bounded line reader**, in a new package `agent/internal/transcriptimport`, replaces
   `os.ReadFile` + `bytes.Split` for imports: it reads with `bufio.Reader` + `ReadSlice`, never
   holds more than one line-limit of an over-long line, and counts bytes, lines and depth as it goes (the depth scan tracks whether it is inside a JSON string and honours backslash escapes, so brackets in a tool output's text do not count; it never builds a value),
   so a limit trips before the next line is stored. It produces the `[][]byte` the existing
   normalizers take, so the normalizers' input is bounded by the body limit by construction.
2. **The rendered output is bounded while it is built**, not after: `renderHandover`'s
   `strings.Builder` (`session_handover.go:476`) is replaced for imports by a writer that counts and
   returns `ErrRenderLimit`; the cut loop in `RenderHandover` (`:367`) checks the context between
   iterations. 0115's own move keeps its unlimited behaviour (its source is trusted); the limit is a
   parameter, zero meaning none.
3. **Memory.** Peak is roughly the body plus the split lines plus the turns, a small multiple of the
   body. The 64 MiB body and 256 MiB staged figures are the guess to be replaced by Phase 2's
   measurement of peak RSS on hostile fixtures at the body cap. The rule it applies: if that peak
   exceeds a quarter of `memory.max` on the smallest workspace size, parsing moves into a child
   process with its own limit; otherwise it stays in the Agent.
4. **Measured so far:** nothing for this feature. The 3.9 s / 147 MB figure (`codex/transcript.go:124-125`)
   is codex, trusted, and is the only basis for the 60 s parse time (about 15× on a file twice the
   body cap). Claude has no figure.

### 6. Secrets (requirement 5)

1. `bridge.ScrubSecrets` runs over the whole rendered file as in 0115 (`session_handover.go:478`),
   including the quoted source-instructions section and the provenance header's report (file names
   and record types cannot hold a secret, but the rule is "everything rendered").
2. **The review shows what was found.** Phase 2 adds `bridge.ScrubSecretsCounted(s) (string, []Finding)`
   next to `ScrubSecrets` (which becomes a wrapper), where a `Finding` is the pattern class
   (known provider token shape, env assignment, high-entropy token — the three passes at
   `fulltext.go:104`), the turn number and a length, **never the matched text**. The dialog shows the
   count per class and the turns, and a preview of the candidate's head. It does not offer to unscrub.
3. **The raw body has already reached the workspace** when the review appears: it crossed the
   Control Plane (which spools the upload while relaying it) and sat in the Agent's staging file until
   `parsing` ended. The dialog says this in a sentence, before the member drops the file where
   possible (a one-line note on the drop zone) and again on the review. The scrub is best-effort
   (`fulltext.go:101`: it can miss a secret and can over-redact a harmless token); the dialog says that
   too, so that "scrubbed" is not read as "safe to share".
4. Whether a finding must be acknowledged before confirm is a product decision (open question 3).

### 7. Cancellation (requirement 6)

1. **The job owns a `context.Context`** (`WithCancel`, plus the parse timeout of decision 5) created
   when it leaves `uploading`. `POST …/cancel` cancels it and the state follows from the runner
   returning, as 0059 does (`repo_jobs.go` `markRepoJobCanceled`/`finishRepoJob`).
2. **The normalizers get context-taking variants, and the old ones stay.** Phase 2 adds
   `claude.CollectAllTurnsCtx(ctx, lines)` (checking `ctx.Err()` every 256 iterations of the loop in
   `collectTurns`, and threading it into `RewoundPrompts` and `collectAnswers`, `claude/transcript.go:1023`),
   an exported `codex.ParseRolloutCtx(ctx, lines)` (`parseRolloutFull` is unexported today; the
   existing function becomes a wrapper with `context.Background()`), and a context check in
   `BuildHandover`/`RenderHandover`'s loops. A cancelled call returns `ctx.Err()` and the job removes
   its staging. The old signatures keep their callers (the mirror, usage, 0115's move) untouched.
3. **The upload is cancelled by the request.** The job record is written, synced, when the body
   starts arriving. A closed connection aborts the counting reader; the staging is removed and the
   record goes to `canceled` (`upload_aborted`). The client learns the id only from the `202`, so
   aborting the fetch is the one way to cancel an upload, and a network drop is indistinguishable
   from it; both end the same way.
4. **Cancel is a request, and says what it did.** After `creating` begins it is refused (3.5): a
   created session is not something a cancel can un-create without the member's explicit delete.

### 8. API and route shape

Control Plane routes (all under the cookie-authenticated `/api`; each relayed to the Agent at the same
path without the `/api` prefix, as `relays_sessions.go` does for the move). `confirm` goes through the
session-create quota check like move and fork (`control-plane/workspace_handlers.go:1228`, which also
starts a cold workspace, `:1230`); the upload starts it too, since the Agent must be up to receive.

| Route | Purpose | Mark | Audit |
|---|---|---|---|
| `POST /api/transcript-imports` (multipart: `file`) | stage, hash, enqueue; answers `202` `{id, state:"queued"}` after the body is received | `signHash` | `transcript_import.upload` (id, size) |
| `GET /api/transcript-imports` | list the member's jobs (non-terminal and not yet dismissed) | — | — |
| `GET /api/transcript-imports/{id}` | the job: state, report, suggestions (`cwd`, `gitBranch`), target, errors | — | — |
| `GET /api/transcript-imports/{id}/preview` | scrubbed head of `candidate.md` (bounded) and the finding counts | — | — |
| `POST /api/transcript-imports/{id}/confirm` `{repo, branch, commit, kind, model?, driver?, title?, ack_findings?}` | reserve the name, write the handover, create the session; `202` | `signBody` | `transcript_import.confirm` (id, target) |
| `POST /api/transcript-imports/{id}/cancel` | decision 7 | `signBody` | `transcript_import.cancel` |
| `POST /api/transcript-imports/{id}/check` | reconcile an `unknown` job (3.4) | `signBody` | — |
| `POST /api/transcript-imports/{id}/retry` | create again under the same name when provably absent | `signBody` | `transcript_import.retry` |
| `POST /api/transcript-imports/{id}/deliver` | send the held first prompt again, on the member's word (3.6) | `signBody` | `transcript_import.deliver` |
| `DELETE /api/transcript-imports/{id}[?abandon=1]` | dismiss a terminal or `unreadable` record; `abandon=1` ends an `unknown` job (3.5) | `signBody` | `transcript_import.dismiss` |

Every route that changes a job carries the mark, so a session shell cannot cancel, reconcile (which can start a create) or erase a member's jobs; the reads are open.

The `kind` is one of `moveTargetKinds()` (`agent/internal/sessionx/session_move.go:53`: the kinds that
declare a measured move target), exactly 0115's set, so cursor (`not_measured`) and lcpp stay out. A
kind outside it is refused `import_target_kind`. Errors use the move endpoint's shape
(`session_move.go`'s `moveErrWire`): a `code`, a `message` that names the limit, and for a target
problem the `target`. Console strings go through the i18n catalogues, English and Japanese.

## Options rejected

- **Placing the jsonl in the CLI store and resuming natively.** Out of scope (#3031) and the reason
  0115 decision 2 gives (signed thinking blocks, version-drifting stores) applies with more force when
  the file did not come from this machine.
- **Handing the raw file to the target as the document.** The target would parse an unvalidated file
  with its own tools; the normalizers and the report are the point.
- **An LLM summary of the upload.** 0115 decision 4; and it would put untrusted text in front of a model
  with no provenance step first.
- **A job held only in memory plus a marker (0059 as built).** Right for a clone, wrong for a job
  that creates a session: the marker lives in `~/repos` and says nothing about a target.
- **Making `fstore.Write` and `Read` synced and tri-state for everyone.** Costs the hot
  status-hook writers and changes what unreadable means to seven families. Additive methods instead.
- **Keeping the raw body until the session is created.** Nothing after the review needs it, and it is
  the least-scrubbed copy.
- **Parsing in a child process from the start.** Real isolation, at the cost of a second binary mode
  to build and test. Held in reserve behind decision 5.3's measured threshold.

## Phases

Each phase is one PR; none merges a half-working feature to users, because the Console entry point
(P5) is last. Tests named per phase are the ones the phase must add.

- **P1 — job store and limits file.** `fstore.WriteSynced` and `ReadChecked`; package
  `agent/internal/transcriptimport` with `limits.go`, the job record, the state machine with its
  transition table, the tri-state load, the boot recovery sweep and the TTL sweepers, the staging
  layout, `statemig` entries; `importJobs` on `GET /sessions` and the CP holder; read-only
  `GET` routes. *Tests:* every transition allowed and every other refused; a record cut mid-write is
  `unreadable` and never `missing`; an unreadable record is an `unreadable` row, not `unknown`; an aborted upload ends `canceled`; `abandon` ends an `unknown` job in every sub-case; restart in each non-terminal state lands where 3.4 says;
  staging with no record and raw older than 2 h are swept; a held job keeps the idle forecast busy and
  `review` does not.
- **P2 — safe parse and render.** The bounded line reader; the raw-record validation layer and
  `ImportReport` (format detection, type counts, unknown types per parser, refusals, normalizer-drop
  counts); `CollectAllTurnsCtx`, `codex.ParseRolloutCtx`, context checks in `BuildHandover` /
  `RenderHandover`; the bounded render writer; provenance header, section markers and the quoted
  source-instructions section; `ScrubSecretsCounted`. *Tests* on hostile fixtures: 70 MiB body,
  9 MiB line, depth 10,000, NUL and invalid UTF-8, 300,000 tiny lines, a source that fakes headings
  and tool results, a developer message with a backtick fence, a codex file with only developer
  records, a mixed claude/codex file, a legitimate line with 70 `{` inside a string, 200,000 lines each with a distinct `type`; each trips the limit it should and names it; cancellation
  stops a parse within one check interval; no secret appears in the report. *Measurements, recorded in
  the ADR's addendum:* peak RSS and wall time at the body cap for claude and codex, which fix the
  constants and settle decision 5.3.
- **P3 — upload and job runner.** The Agent upload route behind `RequireUpload`, the runner
  (`queued`→`parsing`→`review`), admission limits, preview, cancel, dismiss; the CP route table,
  `multipartUploadRoutes`, audit, allow-list. *Tests:* an unmarked request writes nothing; a closed
  connection leaves no staging; two uploads parse one at a time; two simultaneous uploads cannot together exceed the staged-bytes limit; a staged file rewritten after hashing fails at `parsing`; every changing route refuses an unmarked request; the over-limit paths of the table
  answer with the limit's name; the raw body is gone once `candidate.md` is synced and on every failure.
- **P4 — confirm, create, deliver, recover.** Name reservation and its hold against allocation; the
  create with the durable idempotency key and no `initial_prompt`; SHA resolution, worktree from the
  SHA and the `HEAD` check; the handover write; the held-prompt delivery (generalized from
  `moveDelivery`); `unknown` reconcile, retry, deliver; cancel refusals. *Tests:* a crash at each
  point between reserve and delivery recovers per 3.4 and never creates a second session; a meta that
  exists but does not read is not retried; a stale local branch fails `head_mismatch` and sends no
  prompt; an imported target survives trash/restore and an A→B move.
- **P5 — Console and guide.** The drop zone, the dialog (repo, branch, commit, kind, the transcript's
  suggestions, the "already reached the workspace" note), the job row with progress and states, the
  review (report, findings, preview), cancel/retry/deliver, en/ja strings; the member guide page
  describing the flow. *Tests:* dom tests for each state; the dialog never offers a kind outside
  `moveTargetKinds()`.
- **P6 — live run.** Real transcripts from the member's computer in both formats, the limits revised
  from what they show, and the result appended to this record as an addendum. Anything not run is an
  issue (`needs-live-run`), not a sentence here.

## Open questions only the user can answer

Not decided here. Each says what depends on it.

1. **Formats in the first release.** Both claude and codex (the issue), or claude first and codex
   in a following PR? The phases are format-agnostic, so this only orders P2's work and fixtures.
2. **Real sizes.** How large are the transcripts you would drop (the largest, and a typical one), and do
   they carry inline screenshots? 64 MiB and "an over-long line is omitted, not fatal" are guesses; a
   transcript of 200 MB would be refused, and one full of images would lose those records (reported,
   but lost).
3. **Do findings block?** When the scrub finds something, may the member confirm freely (counts are
   shown), or must they tick an acknowledgement first? Recommendation: acknowledge, because the raw
   file has already reached the workspace and the choice is cheap to make now.
4. **Where the session starts.** On a new branch created from the picked commit (what decision 3.7
   assumes), on an existing branch fast-forwarded to it, or detached? This decides the create request
   the worktree is made with, and what a member sees in `git status` afterwards.
5. **Is the review step mandatory?** The design always stops at `review`. Skipping it when nothing
   was refused, dropped or scrubbed would save a click and remove the one place the member sees what
   was done to their file.
6. **Keep the raw body?** It is deleted as soon as the candidate is written (4.4). Keeping it for a
   short while would allow "choose another target and re-render", at the price of the least-scrubbed
   copy living longer. Is re-rendering ever wanted, or is dropping the file again acceptable?
7. **Local paths and names in the transcript.** The content carries the member's home directory,
   user name and file paths from the other computer. Leave as is (the default here, since the target
   needs paths to make sense of the history), or rewrite a recognised home prefix?
8. **Who may import.** Every member of a workspace, or an opt-in in Settings? The design assumes a
   workspace is one member's, as the environment is today (a per-user container).

## Consequences

- A member can continue an outside session in one drop, with the validation report as the evidence
  of what was kept, refused and removed, and with no model call before the target's own first turn.
- Importing costs a bounded amount of memory and disk (decision 5) on a host that is memory
  constrained; the limits can reject real but extreme transcripts, and say so by name.
- The target starts with the same loss 0115 accepts (no thinking, no native tool state, tool output
  only for the last turns), and with less trust: it is told the content is a copy.
- A second durable-job store exists next to `session-move`; the two share the handover file format,
  the delivery mechanism and the trash behaviour, and nothing else.

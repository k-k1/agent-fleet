---
audience: "someone changing the workspace agent, or adding an agent kind"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-09"
---

# 04. The workspace agent

English | [日本語](04-agent.ja.md)

## 4.1 What it is

A resident Go process inside each workspace, running unprivileged (`USER dev`) under an
init that reaps zombies (`--init` on docker, `InitProcessEnabled` on the ECS targets;
`native` has no container and no init). **From the CP's point of view it is the only
actor**: everything that touches the runtime, tmux, the working copies, the filesystem
or a CLI agent goes through it, and the CP only relays ([05](05-api.md)).

- Every endpoint except `GET /healthz` sits behind `httpx.RequireToken`, a bearer check
  against `AGENT_TOKEN` ([07 §7.5](07-security.md)). With `AGENT_TOKEN` unset the check
  is off, which is for local development only.
- Sharing the workspace's network namespace is what lets it reach in-workspace services
  over loopback: the preview relay (`/proxy/{port}/…`, `handlePreview`) and the browser
  manager's navigation (§4.10).

## 4.2 The session model

**A session is one logical slot binding a conversation, a working directory, settings
and execution state.** `kind` is the agent; `driver` is how it is controlled.

- **Only `driver=tui` owns a tmux session**, named `claude_<name>` for every kind
  (`session.TmuxName`).
- A `driver=managed` session has no pane. Its process depends on the kind's managed
  driver (§4.3): a daemon shared by the workspace, a child process per session, or code
  inside the agent.
- The internal id is deterministic, `session.UUID` over the directory and the name.
  **The agent's own conversation id is stored separately.**
- **The name is allocated by the server** (`allocSessionName`); a name in the create
  request is ignored. A caller that must not create twice sends an `idempotency_key`
  and can look it up with `GET /sessions-idempotency/{key}`.

### Metadata and the list

- **Metadata is persisted** in `~/.local/state/agent-fleet/sessions`
  (`session.MetaDir`, overridden by `AF_SESSIONS_DIR`). It is on the home volume and
  hidden from the file browser, so **the list and resumability survive stop and start**.
- **A stopped session is auto-archived — never deleted — once its TTL is up**
  ([decisions/0097](../decisions/0097-session-retention.md)).
  - The TTL is the user's setting (`sessionStoppedArchiveDays`, which can be "never"),
    else `AF_SESSION_STOPPED_TTL`, else 7 days (`session.StoppedTTL`).
  - Locked sessions are exempt.
  - The sweep runs inside the list handler; there is no timer.
- **The list is metadata-driven, merged with per-driver liveness**: the runtime handle
  for managed, tmux for tui (`HandleListSessions`).
  - Orphaned `claude_*` tmux sessions with no metadata are listed too. Their kind is
    sniffed from the pane's command (`tmuxx.PaneKind`, which knows claude, codex and
    opencode and calls anything else `shell`).
  - This deliberately closes the "it is running but not in the list" dead end.
- **The CP mirrors the list into its database** (`sessionsPayload`,
  `store.ReplaceSessions`). While the workspace is stopped, or the agent cannot be
  reached, the CP serves the mirror with `alive:false`
  ([06 §6.3](06-data.md)).

### Ending, deleting and restoring

| Operation | Endpoint | Effect |
|---|---|---|
| halt | `POST /sessions/{name}/halt` | stops the driver, keeps the metadata; the row stays as stopped and can be resumed |
| delete | `DELETE /sessions/{name}[?stop=1]`, or its old name `POST /sessions/{name}/stop` | moves the session **to the trash** (`trashSession`, [decisions/0101](../decisions/0101-session-delete-via-trash.md)); see below |
| archive / restore | `POST /sessions/{name}/archive`, `…/restore` | hides the row and brings it back as stopped |
| recreate | `POST /sessions/{name}/recreate` | archives the old slot and starts a new conversation with the same directory, kind, driver and settings; if the launch fails, the old slot is restored |
| lock | `POST /sessions/{name}/lock` | refuses delete and TTL archiving (403); archive is still allowed |

- **The trash** writes an archive of the metadata and the transcript, then removes the
  metadata.
  - A running session is halted first with `?stop=1` (and by `/stop`); without it the
    delete is refused with `409 session_running`.
  - A locked session is refused (403). One that resumes while it is being moved gets
    `session_resumed`, and nothing is deleted.
  - `POST /cleanup/archives/{id}/restore` brings a trashed session back.
  - **Deleting never touches the working copy**, so a restored session does not come
    back to a missing folder.
- **Every path that folds a session away promotes its carried work first**
  (`PromoteCarriedFor`, before any kill or handle drop). A new path of that kind must do
  the same.

### Resuming

- `POST /sessions/{name}/start` resumes a stopped session without attaching, for both
  drivers.
- A managed session is reopened by its driver's `Resume`, following its process model:
  - codex and opencode resume the native id on the shared daemon;
  - copilot, cursor, kiro and muse start their per-session child and reopen the
    conversation recorded for the slot;
  - lcpp opens the slot's own store inside the agent.

  At agent boot, each kind's `ReconcileManaged` rebuilds the live handles.
- **An agent kind cannot resume if its working directory is gone** — it does not fall
  back to the home directory. `shell` does, and `ssm` always starts in the home.
- ⚠️ **claude is resumable only when its JSONL holds a real user or assistant line**
  (`claude.JSONLResumable`). With remote control on, **a `bridge-session` line is
  written before any conversation happens**, so treating "the file exists" as
  resumable makes `--resume` die immediately. A non-resumable file is dropped and the
  slot starts fresh with `--session-id`.
  - Related: the entrypoint seeds **remote control off**
    (`remoteControlAtStartup: false`) for a new workspace. An existing settings file
    without the key gets it once; **a value the user set explicitly is left alone**.
- ⚠️ **claude sometimes restarts itself, and when it does it drops the session id.**
  Switching to the full-screen TUI, restarting after sign-in, changing model: the
  restart argv is rebuilt **from the configuration flags only**, and structurally
  cannot contain `--session-id` or `--name` (measured on 2.1.239: both were on the
  launch command, neither was on the live process).
  - **A claude that lost its id starts a brand-new conversation under a random one**, so
    the deterministic transcript never appears again.
  - Without a remedy, the mirror sits at "no conversation yet" forever and the status is
    written under a different id. **The session vanishes from the Console entirely**,
    taking usage, abort detection and reporting with it.
  - The fix is the `claude-sid` **ledger** (`internal/agents/claude/sid.go`, an
    `agents.SidStore`), which maps the slot to claude's real id. It is recorded when a
    hook announces itself, keyed on `AF_SESSION_NAME`.
  - **That variable is part of the tmux session environment, so it survives the
    restart**, which is why it was chosen over guesswork like matching the working
    directory. The transcript location and the `--resume` target both go through
    `LiveSID()`.

### Conversation ids: captured and imposed

There are two ways to hold a conversation id, and they fail differently.

- **Captured**: the CLI mints the id and we re-record it on **every event**, so if the
  CLI moves to another session we follow on the next event.
  - codex (a hook), opencode (a plugin), agy and kiro (a disk scan).
  - Measured: zero drift (codex 58/58, opencode 16/16).
- **Imposed**: we mint the id and pass it in, and everything downstream assumes it is
  still in use — **which breaks silently the moment the CLI stops using it** (the
  claude case above).
  - claude and copilot (`--session-id`), cursor (`--resume`).
- muse is imposed at start: AF mints a UUIDv7 and MSP's `session/start` takes it
  verbatim. What AF records, though, is the id and path that reply returns
  (`museSession` in `internal/agents/muse`), so the stored id is the host's answer
  rather than an assumption. The store holding them is keyed on the slot.
- lcpp is neither: its store is keyed on the slot itself.

**When you add a kind that imposes an id, you must ship the recovery path with it.**

- claude has the hook-fed ledger above. copilot and cursor, which have no status hook,
  re-find the id on disk (`ResolveImposedSID`, `internal/agents/imposedsid.go`).
- Recovery runs only when the imposed id exists nowhere on the CLI's side.
- It adopts a candidate only when **exactly one** matches the directory, was created
  after the slot, and is not already claimed. **When it is ambiguous, do nothing**:
  showing somebody else's conversation is worse than staying stuck.
- The evidence: copilot's `session-state/<id>/workspace.yaml` (`cwd`, `created_at`);
  cursor's `projects/<cwd-slug>/agent-transcripts/<chatId>/`, whose directory mtime is
  the creation time (appends do not move it — measured).

### Other session operations

- ⚠️ **tmux target matching is a prefix match.** `claude_foo` matches `claude_foo-sh`,
  which can misidentify, and mis-kill, a session. **Every target reference in this
  repository uses the exact form** (`session.ExactTarget`, `=name`). Pane-level
  commands such as `capture-pane` and `send-keys` resolve the pane id first
  (`tmuxx.SessionPaneID`), because `=name` does not address a pane.
- **Fork** (`POST /sessions/{name}/fork`) branches a new slot carrying the
  conversation, with the same kind and driver.
  - An optional body `{"at": <anchorId>, "include": bool}` branches **from a past
    message** instead. The anchor is `transcript.Turn.AnchorID`, a kind-specific opaque
    id, and each kind's `ForkAtResolver` answers whether it can
    (`agents.ErrForkAtRoute` → `fork_at_unsupported`).
  - The mechanism differs by kind:
    - codex and opencode use the runtime's own parameter, so they need managed;
    - muse uses its runtime's cut point;
    - claude and copilot truncate a copy of the transcript, which works in a TUI;
    - lcpp truncates its own store.
  - Which kinds fork at all is [ref/agents](../../guide/ref/agents.md).
- **Switching driver** (`POST /sessions/{name}/driver`) stops and resumes the same
  conversation under the other driver. It applies to kinds that have both, it is
  refused mid-turn (`409 busy_switch`), and it keeps the kind, directory and native
  id.
  - codex goes from managed to Terminal only from a stopped session
    (`409 codex_stop_first`). A direct codex TUI cannot open a thread the shared app-server
    has loaded, and the server unloads one about 70 s after its last subscriber leaves.
    So `DropHandle` has the read-only observer let go of the thread as well as the writer,
    and a Terminal launch that would resume a thread still loaded is refused with
    `409 codex_releasing` (`codex/release.go`: one `thread/loaded/list`).
- **Model resolution at creation** (`resolveLiveModel`, for codex, copilot, opencode
  and lcpp) checks a requested model against the live catalogue and expands a picker
  label or a unique abbreviation into the full identifier.
  - **An ambiguous or unavailable model is refused with `400 bad_model` before the
    clone or worktree happens**. That closes the "it starts and then dies on an invalid
    model" trap. lcpp also refuses an empty model.
  - A model the user hid is refused for every kind (`model_hidden`).
  - If the catalogue cannot be read (offline, CLI not installed), the value is kept and
    the start proceeds.
  - ⚠️ copilot on the free plan has **no catalogue and only auto**, and **auto does not
    accept `--effort`**. So the launch code passes the flag **only for a concrete
    model**. Passing it with auto fails to start, which a free-plan user would hit every
    time. The Console's `useEffortOptions` offers only the default in that case.
- **Titles and branches**: `POST /sessions/{name}/title/{suggest,accept,dismiss,set}`
  proposes and sets the display name from the conversation (`suggest` is also what
  regenerates one). `suggest-branch` proposes a branch name from the conversation, and
  `rename-branch` applies one.

## 4.3 The pattern for integrating a kind

The kinds are the `Kind*` constants in `internal/session/session.go`: claude, codex,
cursor, opencode, agy, copilot, kiro, lcpp, muse, plus shell and ssm. Which of them
support Managed, Terminal (CLI) or both is [ref/agents](../../guide/ref/agents.md).
**Adding one is [20 Adding an agent kind](20-add-an-agent.md)**; this section is the
shape.

### Managed drivers and where the default comes from

The managed drivers are registered in `managedDrivers` (`internal/sessionx/session_turn.go`),
and each declares its process model in `Capabilities.ProcessModel`
(`internal/agents/<kind>/driver.go`):

| `ProcessModel` | Kinds | What runs |
|---|---|---|
| `shared-daemon` | codex, opencode | one daemon per workspace (codex's app server, `opencode serve`); a session is a thread on it |
| `per-session-child` | copilot, cursor, kiro, muse | one child process per session: ACP for copilot, cursor and kiro (`acp.go`), MSP for muse (`internal/msp`) |
| `in-process` | lcpp | code inside the agent; no child process |

- **The agent itself defaults an unspecified driver to `tui`**, except for a kind whose
  `Caps().ManagedOnly` is set (lcpp, muse), which defaults to `managed`
  (`HandleCreateSession`).
- **The "Managed by default" users see comes from the callers.** The Console's launch
  UI starts a kind whose registry entry has `managedDriver: true`
  (`console/src/agents/registry.ts`) as managed. The in-container MCP `create_session`
  sends `managed` for codex, opencode, copilot, cursor and kiro (`mcpStdioCall`), and
  so do the CP's own callers: its MCP `create_session` (`control-plane/internal/mcpsrv/mcp.go`)
  and the scheduler's `injectDriver` (`control-plane/scheduler_wake.go`). A bare
  `POST /sessions` with no driver gets `tui`.
- A new kind with both drivers must be added to all four callers.

### The surfaces a new kind fills

**A kind fills the surfaces of the drivers it supports**, and they are the same every
time. Each one is a contract in the code; claude, codex and opencode are the worked
examples below. Which kind supports which driver, and
how a user signs in to each, are [ref/agents](../../guide/ref/agents.md) (sign-in is
its [How to sign in](../../guide/ref/agents.md#how-to-sign-in) section); the sign-in
flows are [08](08-integrations.md).

- **Launching in tmux, and holding the id** (kinds with a Terminal route): the kind's
  `BuildLaunch` returns an `agents.LaunchPlan`. Decide whether the id is captured or
  imposed first (§4.2). A managed-only kind (`Caps().ManagedOnly`: lcpp, muse) has no
  such surface; its `BuildLaunch` always refuses with `ErrNoTerminalRoute`.
  - claude imposes it: `--session-id` for a new slot and `--resume` through `LiveSID()`
    for an existing one, plus `--name`, `--model` and `--fork-session`.
  - codex captures it: `codex resume <id>` or `codex fork <id>`, launched directly and
    never through the shared app server; a hook re-records the id.
  - opencode captures it: `opencode --session <id>`; its plugin re-records the id.
- **Launching under managed**: a `Driver` registered in `managedDrivers`. What it drives
  follows its `ProcessModel` (the table above).
  - codex: the shared app server's `thread/start` / `thread/resume`.
  - opencode: the shared server's v1 session API and its event stream.
  - lcpp calls no external API: `Resume` builds the handle and opens its own store
    inside the agent.
- **The conversation's truth, and where the transcript is read from**: each kind
  decides both and gives the second a transcript reader of its own (§4.7).
  - claude, codex and opencode: the CLI's native store is both, for both drivers:
    claude's JSONL, codex's rollout JSONL, opencode's SQLite (`message` / `part`).
  - lcpp: its own store is both; there is no other copy of the conversation.
  - muse: **the muse host owns the conversation**; the transcript is read from a
    **mirror** in our store, written from the live item stream (`muse/transcript.go`).
    The mirror exists because the transcript must be a local disk read, never a spawned
    host, and the host's on-disk log is an internal runtime format with no stability
    promise. A failure to write the mirror is never fatal to the session. A turn that ran
    while the agent was not watching (it died mid-turn) is still on the host but missing
    from the mirror until the next resume, which appends what the mirror lacks from the
    host's folded history — the one `session/resume` carries, or `session/read` when the
    resume served none (`muse/backfill.go`). The backfill is never fatal either.
- **Live state** is normalised into the status store (§4.4) from whatever the kind
  emits.
  - claude: hooks plus a tmux probe.
  - codex: runtime events under managed. Under TUI, hooks for working / idle, and the
    rollout for a missed turn end and for a pending question.
  - opencode: server events under managed, the plugin under TUI.
- **Where credentials land** must be in the filesystem denylist (`fsDeny`, §4.6), along
  with anything else the kind writes state into.
  - claude: `CLAUDE_CONFIG_DIR`, moved out of the browsable home.
  - codex: `~/.codex`.
  - opencode: provider keys are kept in the encrypted store, and its own OAuth sign-in
    under `~/.local/share/opencode`. The keys reach a TUI session through
    `LaunchPlan.Env`. The shared `opencode serve` gets them as its process environment
    (`cmd.Env`) and reads them once, at start. So a key change waits for a restart of
    the daemon, which the Console offers (Settings → Agents,
    `POST /connections/opencode/serve/restart`); it drains running turns and cuts off
    any still running at the timeout, so it is never done on the agent's own
    initiative.

- ⚠️ **Environment reaches the process through `tmux new-session -e`**
  (`agents.LaunchPlan.Env`, applied by `startSessionTmux`). **Never prefix secrets onto
  the command**: a prefix lands in `/proc/*/cmdline` and in tmux's
  `pane_start_command`, readable by anything in the workspace. Only the non-secret
  toolchain exports (`toolchainShellPrefix`: `JAVA_HOME`, node, `TZ`) are prefixed.
- ⚠️ **Always reap child processes.** The agent is not PID 1, so nothing collects for
  you, and an un-waited child leaks a PID as `<defunct>` forever.
  - `Run`, `Output` and `CombinedOutput` wait internally and are safe.
  - **When you start a process yourself (`cmd.Start`, `pty.Start`), every path,
    including the failures, must reach a wait.**
  - The easy leak is "kill it on a start timeout and return", which really happened
    for the codex and opencode daemons.
  - The shared login-flow helper, `agents.Flow.Close()`, kills *and* waits
    (`internal/agents/flow_test.go`).
- ⚠️ **codex's hooks use the same nested schema as claude's**
  (`hooks.<Event>=[{hooks=[{type,command}]}]`). Written flat, they **parse but
  silently never fire**, and resume quietly starts a new conversation instead.
- **rtk, the token-saving proxy, is wired differently per kind** (`agent_rtk.go`),
  and only when rtk is in the image:
  - claude: a `PreToolUse`/`Bash` hook;
  - opencode: a plugin that rewrites commands;
  - copilot: a `preToolUse` hook;
  - codex and agy: **an instruction block in `AGENTS.md`, which is best-effort only**.

  On/off is the presence of that artefact. For codex, opencode, agy and copilot the
  persisted preference (`rtk.json`, `GET/PUT /agents/rtk`) is the truth and is
  re-applied at start. For claude it is the hook in its settings.

### The managed boundary

- The boundary is the `Driver`, `ThreadHandle` and `Capabilities` types in
  `internal/agents/driver.go`.
- `POST /sessions/{name}/turn` is driver-independent: managed dispatches to the
  structured API, TUI to the key-input path.
- `/respond` and `/settings` exist only for managed. A TUI session gets
  `501 respond_unsupported` / `settings_unsupported`.
- **Where the CLI keeps a readable native store, the conversation body is never copied
  into a store of our own.** The native store stays the read truth, and the transcript
  is normalised on the way out
  ([decisions/0015](../decisions/0015-agent-managed-driver.md)). lcpp (whose store is the
  conversation) and muse (whose store is a display mirror of the host's) are the
  exceptions described above
  ([decisions/0093](../decisions/0093-lcpp-agent-kind.md),
  [0095](../decisions/0095-muse-agent-kind.md)).

## 4.4 State badges

- The stored state (`internal/status`) is **working / idle / question**. Two refinements
  of question also exist: `plan` (a plan awaiting approval) and `permission` (a tool
  awaiting approval).
- A turn's end also carries a transition label that feeds notifications but is not a
  stored state: `failed`, `aborted`, `blocked`, `auth`, `limited`, `spend_limit`
  (`internal/agents/notify.go`).
- The store is one file per session under `~/.local/state/agent-fleet/session-status/`.

**Hooks fire the `session-status` subcommand** (`RunSessionStatusHook`):

- claude passes the state and reads its id from the hook's stdin;
- opencode's plugin passes the state and the id;
- codex passes the state, the id and `codex`.

**claude's hooks**:

- `UserPromptSubmit` → working;
- `Stop` → idle;
- `PreToolUse` with matcher `AskUserQuestion` → question, and `ExitPlanMode` → plan;
- the `permission_prompt` notification → permission;
- `MessageDisplay` → `message`, which leaves the state alone. It records the reply as it streams, line by
  line: the prose a pending question card shows above the question, and the reply still being written
  that `/messages?live=1` returns while a turn runs (`status/livetext.go`, #1250).

**The hooks merge additively** at start and before each claude launch
(`EnsureStatusHooks`). **`PreToolUse` is registered per matcher**, so the rtk hook
(`Bash`) and the state hooks coexist, and **toggling one does not break the other**.

Every managed driver writes to the same store from its runtime events. The Console polls
every 4 seconds, draws the badge, and raises a browser notification on the transitions
that need a human (working → idle, and into question).

### Terminal notifications (OSC 9 / 99 / 777)

A program can ask its terminal for a desktop notification with OSC 9 (iTerm2),
OSC 99 (kitty) or OSC 777 `notify` (rxvt / Ghostty). **The only place that sees those
bytes is the `pipe-pane` recorder** (`record-terminal`): the attach WebSocket carries
tmux's redraw, and tmux swallows the sequences. The recorder scans its stream
(`internal/oscnotify`) and puts a `terminal-notification` event in the outbox
(`sessionx.TerminalNotifier`). Measured on tmux 3.5a: plain OSC and the tmux
passthrough wrapper (`ESC P tmux; … ESC \`) both reach `pipe-pane` byte for byte.

- **Not a notification:** `OSC 9;<digits>…` is ConEmu's control family — `9;4` is a
  progress bar that agy and opencode emit every turn, `9;9` a shell's cwd — and kitty's
  `p=?` is a capability query.
- **Dropped for hook-driven kinds** (claude, codex, opencode; `terminalNotifyHasHooks`):
  their hooks already put answer-ready / question / permission in the outbox, so an OSC
  notification would report the same moment twice. cmux applies the same rule and sets
  claude's `preferredNotifChannel` to `notifications_disabled`.
- **Throttled per session**: the same text again within 30 s is dropped, and at most
  5 per minute are kept.
- Only 7-bit introducers and terminators are parsed; 0x9c/0x9d are UTF-8 continuation
  bytes of Japanese text, not C1 controls.

What each kind can emit, read from its binary (2026-09-27; no kind was captured live):

| Kind (version) | Emits | Notes |
|---|---|---|
| claude 2.1.283 | nothing by default | `preferredNotifChannel=auto` picks a method by terminal and finds none under tmux; `iterm2` / `kitty` / `ghostty` select OSC 9 / 99 / 777, wrapped in tmux passthrough when `$TMUX` is set. Not changed by us: hooks are authoritative. |
| codex 0.157.1 | nothing by default | `tui.notifications` with `notification_method = osc9 \| bel`. Not enabled: hooks are authoritative. |
| opencode | OSC 9;4 progress; OSC 99 code behind a `p=?` capability query | tmux does not answer the query. |
| agy | OSC 9;4 progress only | No notification while a prompt is pending (`agents/agy/pending.go`). |
| copilot, cursor, kiro | none found | — |
| shell | whatever the user runs | The main beneficiary. |

**claude's `PushNotification` tool comes through a hook instead.** The tool (2.1.286, behind
the `tengu_kairos_push_notifications` flag, off by default) raises its local notification only
through `preferredNotifChannel` — an OSC sequence, dropped above — and claude's Notification hook
does not fire for it. `EnsureStatusHooks` therefore adds a `PostToolUse` entry on matcher
`PushNotification` running `session-status push`, which puts `tool_input.message` in the outbox
as a `terminal-notification` with `proto: "claude-push"` (`sessionx.recordPushNotification`).
The same approach as cmux.

- **Skipped** when `tool_response.disabledReason` is `user_present` or `config_off`: claude
  returns before notifying anything. `no_transport` means no *mobile* push only — the local
  notification went out — so it is forwarded.
- **Delivered once.** The OSC route drops claude, so the two routes never both deliver; a hook
  that fires twice for one call is absorbed by `notice.PutOnce` keyed on `tool_use_id`.
- **Inert on an unexpected payload**: no message, no event. The payload shape (`tool_input`
  `{message, status}`, `tool_response` `{message, pushSent, localSent, disabledReason, sentAt}`)
  was read from the binary, not captured from a live call.
- It never changes the session status; the catch-all `PostToolUse` heartbeat fires for the same
  tool as for any other.

## 4.5 Chat and assistants (a headless CLI)

- **Chat is not a tmux session.** It is a parallel subsystem driving a CLI in headless
  mode against its own conversation store (`~/.config/agent-fleet/chats/<id>.json`),
  streamed over SSE (`POST /chat/conversations/{id}/stream`; [05](05-api.md)). The
  backends are `chatx.ChatProviders`: claude, codex, opencode, agy, cursor, lcpp and
  muse.
- **claude's credentials are the same single file the interactive sessions use**
  (`CLAUDE_CONFIG_DIR`).
  - An older scheme of a symlink plus copy-back was removed. A refresh writes through a
    temporary file and a rename, **which turned the link into a real file**, and two
    processes could then hold different refresh tokens.
  - User and project settings are excluded with `--setting-sources ""`, and other MCP
    entries with `--strict-mcp-config` whenever a server is attached.
  - Transcripts from the old dedicated config directory are migrated once, create-only
    (`migrateLegacyChatClaudeProjects`).
- **The fallback is visible.** The conversation's `agent` is the one requested; each
  message's `agent` and the conversation's `active_agent` record **which backend
  actually ran**. The stream opens with an `agent` frame, so the UI follows a switch
  immediately rather than lying about it.
- **An in-container stdio MCP server** (`workspace-agent mcp-stdio`) is attached to
  chat. It needs no token and no egress; its identity is the workspace itself.
  - claude takes it through `--mcp-config`, codex through `-c mcp_servers.af.*`,
    opencode through `OPENCODE_CONFIG`, and agy through its server list. cursor, lcpp
    and muse chats get none.
  - **Read-only by default** (`mcpStdioTools`). `--write` adds the writing tools
    (`mcpStdioWriteTools`): starting, steering, stopping and deleting sessions, memos,
    schedules, browser control, and cleanup.
  - **The gate is the visible tool set, not a permission prompt.** This is deliberately
    a separate implementation and scope from the CP's MCP endpoint.
- **A second, narrower server is materialised into the interactive CLIs** as the
  `mcpreg` builtin `af`, which runs `mcp-stdio --self-report --chromium-attach`.
  - It always advertises the self-report tools: `af_report`, `af_stop_after_turn` and
    `propose_session_handoff`.
  - It also always advertises a small observation set: session status and usage, and
    the memo tools. The seven Chromium attach tools come with `--chromium-attach`.
  - The user's preferences add `--peer-messaging`, `--image-gen` and `--fleet-spawn`
    (`builtinRunArgsFor`).
  - Anything not advertised is refused on call too (`mcpAdvertised`).
- **Unattended approval for codex**: a headless chat has no approval UI. Besides
  `-a never`, the attached MCP servers are set to
  `default_tools_approval_mode="approve"`; without it every call is cancelled.
  **The read-only sandbox (`-s read-only`) is kept**, so MCP works but shell and file
  changes do not.
- **Assistants** (`/assistants*`) are templates of persona, model, knowledge and tool
  scope (`af_read`, `af_write` or `none`). Asking one (`ask_assistant`) runs a single
  turn **with tools forced off**: one hop and no side effects, guaranteed structurally
  rather than by instruction.
- **Model resolution** (`ResolveChatModel`) snapshots a model into the conversation at
  creation and never rewrites it. That protects reproducibility from a provider changing
  its default.
  - The order is: an explicit model; the user's per-agent row (ui-prefs
    `assistantModels`); then a recommended model read from the live catalogue
    (`recommendedAssistantModel`).
  - But **the conversation holds one model, chosen for the agent it was created for**.
    When another backend actually runs (a fallback, or a mid-conversation switch),
    `chatModelFor` re-resolves the model from *that* CLI's setting. **Passing the
    stored value straight through would feed one vendor's model id to another.**
- **Switching agent mid-conversation** is `PATCH /chat/conversations/{id}` with
  `agent` (it also takes `title`). It is refused mid-turn (409) and for a kind without
  headless chat (400).
  - It changes the pin and the model and adds one notice. **The per-backend resume
    handles and message cursors are preserved**, so switching back continues the native
    session.
  - History the other backend has not seen is replayed on the next send
    (`syncProviderPrompt`), the same path as a fallback.

### Which language a prompt is written in

The reader decides ([decisions/0033](../decisions/0033-stored-text-locale.md)): a prompt
branches on the display locale when **a person reads what it produces**.

| The prompt | Treatment |
|---|---|
| The user reads its output — an answer, a summary, a title, reply suggestions, a report | branch on the locale and write each language natively (the persona and instruction functions take `lang`) |
| Only the model reads it — a tool description, an internal judgement | leave it as it is. The model understands either language, and keeping a second copy in step costs more than it buys |
| It is display and instruction at once — the body of a report card | separate the two first. Translated whole, the instruction to the operator changes with it |

- **The language of the output is a separate axis.** Branching changes the instruction,
  not what the output is written in. Reply suggestions follow the conversation's
  language — they are sent back into that session as they are, so switching them would
  flip that session's language too. Summaries and plans keep the conversation's main
  language, and a chat bridge follows its connection's notification language rather
  than the Console's. Tipping all of these to the display locale recreates the original
  bug in the other direction.
- **The operator persona is never machine-translated.** It carries the prompt-injection
  guard, so a mistranslation is a hole in the defence. Both versions keep the same
  paragraphs in the same order, and `TestOperatorPersonaInjectionGuardParity` pins every
  guard clause as a Japanese and English pair.
- **Reuse the Console's words.** The plan's English headings are the chat input's
  placeholder (`chat.plan.placeholder`); if the two differ, every plan update swaps one
  for the other.
- **A newly branched prompt owes a row in `workspace/agent/prompt_lang_test.go`**, which
  fails when the English side holds a single hiragana, full-width katakana, CJK unified
  ideograph, CJK punctuation mark or full-width ASCII character — `・` and full-width
  brackets included, which slip in by habit. Without the row, the Japanese survives on
  the English Console alone.

## 4.6 Git and the filesystem

- **Repositories**: clone with `GIT_TERMINAL_PROMPT=0` so it fails fast, with the name
  checked against a pattern. Also status (`git status --porcelain=v2`), branches,
  checkout, fetch, fast-forward and delete.
  - Submodules are best-effort after the clone, with SSH URLs rewritten to HTTPS.
  - **The parent clone deliberately does not recurse**, because an SSH-registered
    submodule would fail the whole clone.
- **Submodule sync** (`internal/gitx/git_submodule.go`) fetches into the worktree's own
  git directory (`.git/worktrees/<wt>/modules/…`), a clone separate from the parent's.
  - **Measured (git 2.39): killing an in-progress submodule update wedges it
    permanently.** The git directory is left without a HEAD and the working tree is
    empty. Every later update fails with "Unable to find current revision", **and
    nothing reports it**: status is clean and the submodule listing looks healthy.
  - So the rules are:
    1. Past the start budget (`submoduleWaitTimeout`, 10 s), the agent **stops waiting
       but does not kill**. The update continues in the background, and its outcome is
       logged and notified. Only the 60-minute `submoduleHardTimeout` kills git; that
       leaves a wedge the next launch repairs.
    2. Starting without the submodule is logged **and notified** (`submodule-sync`).
    3. An already-wedged submodule is repaired by the one recipe that was measured to
       work: `fetch` to complete the transfer, then `checkout --detach --force` of the
       recorded revision. **Only empty working trees are touched, so local changes are
       never destroyed.** A reused worktree is re-synced the same way.
- **Seeding from the parent** (`git_submodule_seed.go`) is what makes that separate
  clone cost almost nothing.
  - **Measured (git 2.47): make a submodule's remote unreachable and a fresh worktree's
    update fails**, even though the parent holds every object on the same disk; nothing
    links the two stores.
  - So before the update runs, each submodule is cloned locally from the parent's
    `.git/modules/<name>`. **Measured: 0.23 s for a 41 MB submodule with the remote
    offline.** The objects are hardlinked to the parent's, so the worktree's store cost
    168 KB rather than 41 MB, and N worktrees no longer cost N times the submodule's
    size.
  - `protocol.file.allow=always` is set **for that one invocation only**, and only for a
    path the agent computed itself. It is never set for a URL out of `.gitmodules`,
    which is where CVE-2022-39253 lives.
  - The URL override is passed with `-c`, not written to config. **The config file is
    shared with the parent and every sibling worktree**, so writing the local path
    there would redirect their fetches too.
  - Afterwards the submodule's origin is put back to the real remote, taken from config.
    `git submodule sync` would re-read `.gitmodules` and undo the SSH→HTTPS rewrite.
  - Anything the parent does not have is left to the normal update that follows.
  - **Nested submodules are seeded too**, by descending (at most 8 levels): a nested
    one's objects sit under its parent submodule's own store. It cannot be done in one
    pass, because a nested submodule is declared only inside its parent submodule, which
    does not exist until that one is cloned.
  - Submodules are fetched with `--jobs 4`; git's own default is one, strictly
    sequential.
- **The recursive update needs `--init` to reach a nested submodule at all.**
  - Measured (git 2.47): without it, `update --recursive` clones the top level and
    descends into it. There it finds the nested entry uninitialized and **skips it,
    with exit 0 and no output**. That leaves an empty directory which only
    `submodule status --recursive` reports as missing.
  - The initial `submodule init` cannot cover this. It expands the top-level
    `.gitmodules`, and a nested one is unreadable until its parent submodule is checked
    out.
  - This is also what makes the nested SSH→HTTPS rewrite rules
    (`submoduleInsteadOfArgs`) reachable.
- **SCM read and write**: changes, diff, log, graph, show, stage, unstage, discard,
  commit. Revisions are checked as hex, and responses are size-capped.
- **The filesystem API** (tree, file, upload, rename and the rest, rooted at the home)
  defends against traversal, re-checks a path after resolving symlinks, caps sizes and
  detects binaries.
- **The denylist** (`fsDeny` in `fs.go`) hides from listings, and refuses direct
  access to (400), everything that holds credentials or agent state:
  - the agents' own directories (claude, codex, opencode, agy, copilot, cursor, kiro,
    muse);
  - this product's configuration and state (`.config/agent-fleet`,
    `.local/state/agent-fleet`, `.local/share/agent-fleet`);
  - `.ssh`, `.git-credentials`, and `.aws` (the SSO token cache and generated config).

  One read-only exception serves codex's generated images.
- **LFS** is installed system-wide in the image, so clone and checkout smudge normally.
  A pointer left unsmudged is detected by the file API and badged in the viewer.
- Git authentication is one credential helper (`workspace-agent cred`) that decrypts on
  demand ([07 §7.6](07-security.md)).

## 4.7 Transcripts and usage

- Transcripts are returned as **a window at the tail with backward paging**
  (`GET /sessions/{name}/messages`, [decisions/0009](../decisions/0009-transcript-paging.md)).
- Each kind has a reader for its own storage format, and they all normalise to a common
  turn shape. **The parsers are deliberately not merged.**
- Usage per kind has its own source:
  - `GET /claude/usage` and `/codex/usage`: the CLIs' local records, plus claude's
    captured status line;
  - `/copilot/usage`: GitHub's API;
  - `/muse/usage`: what the runtime last reported;
  - `/connections/agy/usage`.

  The cross-session ledger is `/sessions/usage` and `/usage/series`.

## 4.8 Secrets — the agent's responsibility

The agent owns the encrypted store, `secrets.enc` (AES-256-GCM, 0600, written through a
temporary file and a rename under a lock).

- **It supplies credentials through subcommands that never create a plaintext file**:
  `workspace-agent cred`, the git credential helper. `bitbucket-cred` is an alias of it.
- The key, `AF_SECRET_KEY`, is injected by the CP at start. The agent is indifferent to
  how it was provisioned ([07 §7.6](07-security.md)).
- Without a key (a CP with no master key injects none), the same code path writes a
  plaintext `secrets.json`.
- Old plaintext credentials are folded into the store at start and deleted
  (`migrateLegacySecrets`).

**One thing deliberately does not live here: a git provider's OAuth client secret**
([decisions/0052](../decisions/0052-tenant-git-oauth.md), decision 7).

- It is the *tenant's* credential. Keeping it in the CP avoids copying it into
  **every member's** store.
- The agent asks the CP to perform the refresh (`POST /internal/git-oauth/bitbucket/refresh`,
  and the same for Jira). **The user's own refresh token stays here.**
- The bridge's coordinates (`AF_CP_BASE_URL`, `AF_GIT_OAUTH_TOKEN`) are copied into the
  encrypted store at start (`seedGitOAuthBridge`), rather than read from the
  environment. **The credential helper is a separate process started by git, and its
  environment cannot be guaranteed.** The internal git provider's token is handled the
  same way (`seedInternalGit`).

## 4.9 The workspace image and its entrypoint

`workspace/Dockerfile` is multi-stage: a `golang:*-trixie` builder, then
`node:22-trixie-slim` (Debian 13, [decisions/0068](../decisions/0068-debian-13-base.md)).
**The image and the agent are common to every deployment target** — that is the point of
the split ([09](09-deploy.md)).

- **The agent CLIs arrive by one of two routes**, and **the distribution default is
  lean** (`BAKE_AGENT_CLIS=0`; [decisions/0037](../decisions/0037-registry-policy.md)).
  - **Lean**: claude, opencode, codex, copilot, cursor, agy and rtk are **not baked
    in**. That is the safe default, because it does not redistribute proprietary
    software.
    - The entrypoint **boot-installs the pinned versions** from the official sources
      into `~/.local` on first start.
    - The home persists, so later starts skip silently.
    - No network is a warning, not a failure, and the install retries on the next
      start.
    - When the user has not opted into self-update, a CLI that updated itself is put
      back to the pin.
    - kiro (about 855 MB unpacked) and muse are excluded even from that and installed
      on demand: kiro by its launch guard (`install-kiro --if-needed`), muse from its
      connection card (`install-muse`).
  - **Baked** (`BAKE_AGENT_CLIS=1`): an explicit knob for a deployment that wants a fast
    first start.
- **The version pins are the same build arguments on both routes**
  (`CLAUDE_CODE_VERSION`, `CODEX_VERSION`, … — the bump runbook is
  [10 §10.2.1](10-development.md)).
  - **Every pin is written to `/usr/local/share/agent-fleet/versions.json`**, whatever
    the knobs say. That covers the agent CLIs, the toolchains and the database servers.
  - The manifest is read by `GET /env/tool-versions` (Settings → Toolchains, "Tool
    versions"), the smoke test and the boot-install.
  - Two of the operations-tooling MCP servers **cannot be asked their version by
    running them**: `--version` starts one of them, and the other has no version flag.
    So their version is read from the installed package metadata instead
    (`toolSpec.PyDist`, `uvToolVersion`). **A new server of that kind must be handled
    the same way.**
- **Common tools** (`BAKE_OPTIONAL_TOOLS=1`, the default):
  - the Go toolchain (`GO_VERSION`, kept in step with `go.mod`);
  - build-essential and python3 (pip is allowed to install for the user);
  - git-lfs, tzdata and the usual command-line tools;
  - a pinned Debian Chromium with Japanese fonts.

  `BAKE_OPTIONAL_TOOLS=0` is the lean root filesystem for `native`: those tools are
  installed on demand instead (`install-chromium` and the like).
- **Chromium keeps its sandbox.**
  - The setuid sandbox helper is verified at build time. Every other setuid or setgid
    bit is stripped from the image.
  - Chromium runs with `--disable-dev-shm-usage`.
  - The docker runtime adds `SYS_ADMIN` to the bounding set so the helper can create
    namespaces; `dev` gets no effective capability. The ECS runtimes add none.
- **JDKs are outside the image**, in two roots:
  - a shared directory of Temurin JDKs, mounted read-only at `/usr/lib/jvm` on docker
    (⚠️ the JDKs' `cacerts` symlinks must be materialised when that directory is
    built, or the trust store is empty);
  - `~/.local/share/agent-fleet/jvm`, which `workspace-agent install-jdk` fills. On ECS
    nothing is mounted, so it is the only root there.

  `JAVA_HOME` follows the Settings → Toolchains choice, and the lookup prefers the
  workspace's own architecture (`jvmSearchDirs`). Node is installed per version by
  `workspace-agent install-node` into nvm's layout.
- **Layer order**: the heavy, rarely-changing layers come first. The frequently-changing
  copies (the agent binary, the entrypoint, the plugin, the notes) come last, so a
  small fix does not bust the cache. `CMD` is the absolute
  `/usr/local/bin/workspace-agent`, so a copy in `~/.local/bin` cannot shadow it.
- **The entrypoint seeds only what is absent**:
  - claude's `settings.json` (the skip-permissions prompt, remote control, notifications,
    the rtk hook);
  - `~/.gradle/gradle.properties`, with conservative values for a memory-constrained
    host.

  After that the settings UI is the truth; forcing values on every start would fight
  it.
- **The entrypoint re-applies on every start**:
  - the opencode plugin;
  - opencode's `permission=allow`;
  - cursor's update channel;
  - kiro's auto-update switch.
- **The workspace guide is distributed by the agent, not the entrypoint**
  ([decisions/0042](../decisions/0042-user-instructions.md)).
  - claude reads the image's managed policy, `/etc/claude-code/CLAUDE.md`.
  - `reconcileAgentInstructions()` merges the guide **between markers** into the
    `AGENTS.md` of codex, opencode, agy and muse, and leaves everything outside the
    markers alone. A plain copy used to overwrite the whole file on every start and
    wipe what the user had added.
  - copilot and kiro get a dedicated file (`agent-fleet-guide.*`).
  - **cursor has no local user scope and cannot receive it.**
  - The topic files (`workspace/notes/`) ship under `/usr/local/share/agent-fleet/notes/`
    and are registered as skills for claude, codex, opencode and muse
    (`fleetskills.Apply`).
  - ⚠️ `workspace/.dockerignore` excludes `**/*.md`. The guide, the topic files and the
    assistants' knowledge (the one `//go:embed` input) each need a `!` exception.
- **Timezone**: the toolchains setting `timezone` (default `Asia/Tokyo`) is exported as
  `TZ` by the entrypoint; an unknown zone warns and falls back to UTC. It takes effect
  on the next stop and start.
- **The user guide is mounted, or fetched**
  ([decisions/0064](../decisions/0064-docs-three-audiences.md)).
  - Every member gets the same tree: the `guide/` shelves and the root READMEs
    (`guideRoots` in `control-plane/workspace_docs.go`). The developer documentation
    never ships.
  - On docker and native the CP stages that tree (`stageWorkspaceDocs`) and mounts it
    read-only at `/usr/local/share/agent-fleet/docs`.
  - The ECS tasks have no host path the CP can write, so the agent fetches the same
    tree at start instead. It calls `GET /internal/docs` (`control-plane/docs_bridge.go`)
    with a per-membership `AF_DOCS_TOKEN` (`docs_sync.go`).
  - **A mount always wins**: a non-empty docs directory is not fetched over.
  - The archive is not trusted:
    - regular files only;
    - no absolute or `..` paths;
    - caps on the file count and the total size;
    - it is extracted to a staging directory and renamed into place only when the gzip
      stream was read to the end, so a cut download never appears as half the docs.
  - The mount point is owned by `dev` in the image for that reason.
- claude updates itself only under `~/.local`; a baked copy stays fixed. A launcher left
  dangling by an old home path is repaired by the entrypoint.
- **Applying a change**: touching the image or the entrypoint means rebuilding the image
  and a stop and start of the workspace ([10](10-development.md)).

## 4.10 The browser manager

`BrowserManager` (`internal/browserx`) runs one Chromium process per workspace, started
lazily and driven over a CDP pipe. It owns an independent browser context and page per
browser id.

- **The surface**: `POST /browser/pages`, `GET` and `DELETE /browser/pages/{id}`, and
  the WebSocket `GET /ws/browser?id=`.
- **What it refuses**: the agent's own port, and any top-level navigation off loopback
  (`allowedTopLevelBrowserURL`). Subresources may go to ordinary external hosts under
  the workspace's egress policy, but never to the management endpoints: the container
  host aliases, the cloud metadata hosts, the CP, link-local addresses
  (`forbiddenBrowserResource`).
- **The wire protocol** (version 1) sends a `ready` frame first. After that come state,
  navigation, console and error messages as text, and **raw JPEG as binary**.
- **From the Console it accepts only** viewport (with pinch zoom), pointer, wheel, key
  and text, navigation, visibility and copy. **Raw debugging protocol is never
  exposed.**
- The user-visible ceilings are in [ref/limits.md](../../guide/ref/limits.md). The
  defaults can be tuned per workspace (`AF_BROWSER_MAX_FPS`, `AF_BROWSER_PAGE_LIMIT`,
  `AF_BROWSER_DETACHED_GRACE_SEC`, `AF_BROWSER_JPEG_QUALITY`). An idle Chromium is
  stopped after `AF_BROWSER_IDLE_SEC`.
- **The frame rate is enforced at capture, not merely by throttling the send.** The
  acknowledgement is delayed by a one-frame worker, so Chromium is limited at capture
  and encode rather than producing frames that are thrown away.
- The pipe has fixed message and queue limits (8 MiB per message; 256 events or
  32 MiB queued). **When a required event saturates them, the browser is terminated
  and the page moves to `crashed`** rather than growing the queue.

The details are [decisions/0018](../decisions/0018-container-browser-pane.md).

**Attaching to a Chromium someone else started** is a separate manager
([decisions/0038](../decisions/0038-chromium-attach-view.md)).

- The surface is `/browser/attach-targets`, `/browser/attachments*` and
  `GET /ws/browser-attachments`, behind the `attach_chromium` family of MCP tools.
- Control modes are `view-only`, `user-control` and `locked`. The MCP tool starts
  view-only, where the user's clicks do nothing; the HTTP handoff starts
  `user-control`.
- A target is identified by the GUID on the second line of Chromium's
  `DevToolsActivePort`, not by the port, so a reused port cannot silently attach to
  another session's browser.
- Detaching never closes the target.

## 4.11 tmux server scope, and isolating a second instance

> **Why this section exists.** During an integration test
> ([decisions/0008](../decisions/0008-antigravity-cli-agent-kind.md)), a second agent
> started on a different port **ran `kill-server` against the shared default socket**
> on shutdown. It **destroyed every unrelated running session, four times**, including
> the developer's own.

**The permanent fixes:**

- In production there is one agent per workspace, and it is the sole creator of the
  default socket's tmux server. The old shutdown relied on that. **The assumption breaks
  the instant a second instance exists** in the same environment.
- **`kill-server` is banned outright in agent product code.**
  - Shutting down kills **only the sessions this instance owns** — its own metadata
    intersected with what is live (`ownedLiveSessions`) — by exact target.
  - **A live session with no metadata of ours is not touched.** It is impossible to
    tell "another instance's work" from "an orphan that lost its metadata".
  - In production, killing the owned sessions leaves the server to exit on its own,
    which reaches the same end state as before.
- **All tmux execution funnels through `tmuxx.Cmd`.** Setting `AF_TMUX_SOCKET=<name>`
  sends every call to `tmux -L <name>`, a dedicated server isolated from the default
  socket **and from an inherited `$TMUX`**.
- Both rules are held by tripwire tests in `workspace/agent/tmux_guard_test.go`, which
  detect the banned call and any bypass of the funnel.

**How to start a second instance safely** (in-container tests, local debugging).
**Separate the socket, the metadata directory and the port, or it collides with the
real one**. Give it its own `HOME`, keep it off the shared CLI daemons, and start it from
an empty environment:

```sh
d=$(mktemp -d) && mkdir "$d/home"
env -i PATH="$PATH" TERM="${TERM:-xterm-256color}" LANG=C.UTF-8 \
  HOME="$d/home" \
  AF_TMUX_SOCKET=af-e2e-$$ \
  AF_SESSIONS_DIR="$d/sessions" \
  AGENT_ADDR=:7710 AGENT_TOKEN=test-token \
  AF_CODEX_APP_SERVER_DISABLE=1 AF_OPENCODE_SERVE_DISABLE=1 \
  ./workspace-agent
```

- **The socket**: ⚠️ without `AF_TMUX_SOCKET`, starting from inside a tmux pane (your
  usual development session) **inherits `$TMUX` and reliably targets the shared
  server**. That was the direct cause of the incident.
- **The metadata directory**: sharing it makes the second instance believe the real
  sessions are its own and **stop them on shutdown** (ownership is read from the
  metadata).
- **The port**: the agent binds `AGENT_ADDR` before any boot work and exits if it is
  taken, so a clash fails fast rather than half-starting.
- **The home**: the boot work rewrites files across the home. It migrates state,
  reconciles every CLI's instruction file, re-registers the status hooks, and renames
  the `af` MCP server in every CLI's configuration. A second instance on the real home
  rewrites the real sessions' setup.
- **The shared daemons**: at boot the Agent adopts a codex app-server already listening
  on its default address (`ws://127.0.0.1:7798`). It opens a writer connection and
  attaches its observer to every thread that daemon has loaded, including the real
  instance's sessions (measured: a second instance started without the flag observed five
  of them). The opencode serve daemon (`http://127.0.0.1:7799`) is reachable the same way
  once a Managed opencode session exists. `AF_CODEX_APP_SERVER_DISABLE=1` and
  `AF_OPENCODE_SERVE_DISABLE=1` keep the second instance off both. Terminal (CLI) codex
  and opencode sessions still run without them.
- **The environment**: `env -i` passes only the variables listed. Started from a
  session's shell, the second instance would otherwise inherit that shell's Control Plane
  URL and tokens (`AF_CP_BASE_URL`, `AF_MCP_TOKEN`, …) and CLI state directories such as
  `CLAUDE_CONFIG_DIR` and `CODEX_HOME`, which point back at the real instance's state
  whatever `HOME` says.
- Clean up with `tmux -L af-e2e-$$ kill-server`, **against your own socket only**.
  Typing `kill-server` against the shared one is forbidden.
- Tests isolate the same way. A test that runs tmux directly uses its own `-L` socket;
  a test that goes through product code sets `AF_TMUX_SOCKET` with `t.Setenv`.

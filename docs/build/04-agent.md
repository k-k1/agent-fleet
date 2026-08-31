# 04. The workspace agent

English | [日本語](04-agent.ja.md)

Audience: someone changing the workspace agent, or adding an agent kind
Source of truth: the code (this is a map and a statement of intent)
Updated: 2026-07

## 4.1 What it is

A resident Go process inside the per-user container, running unprivileged under an init
that reaps zombies. **From the CP's point of view it is the only actor**: everything
that touches the runtime, tmux, git, the filesystem or a CLI agent goes through it, and
the CP only relays ([05](05-api.md)). Every endpoint requires the bearer token
([07 §7.5](07-security.md)). Sharing the container's network namespace is what lets it
reach in-container services over loopback.

## 4.2 The session model

**A session is one logical slot binding a conversation, a working directory, settings
and execution state.** `kind` is the agent; `driver` is how it is controlled. **Only
`driver=tui` owns a tmux session**; a managed session is a thread on a runtime shared
per workspace, with **no pane and no process of its own**. The deterministic internal
id is derived from the directory and name; **the agent's own conversation id is stored
separately.**

- **Metadata is persisted** outside the browsable area, on the home volume, so **the
  list and resumability survive stop and start**. Stopped sessions are pruned after a
  TTL.
- **The list is metadata-driven, merged with per-driver liveness.** Orphaned tmux
  sessions with no metadata are listed too, sniffing the kind from the pane's command —
  this deliberately closes the "it is running but not in the list" dead end.
- **Four ways to end one**: `halt` stops the driver and keeps the metadata (resumable);
  `stop` stops it and drops the metadata (the CLI's native history survives);
  `archive` / `restore` hide and unhide; `recreate` archives the old and starts a fresh
  conversation in the same place.
- **Resuming**: a managed session resumes its native conversation id on the shared
  runtime, and a restart of the agent reconstructs the live handles by reconciliation.
  **The agent kinds cannot resume if the working directory is gone** — they do not fall
  back to the home directory; a plain shell does.
  - ⚠️ Deciding resumability from "a transcript file exists" is wrong for claude: with
    remote control on, **a line is written before any conversation happens**, so
    `--resume` dies immediately. The check must be for a real conversation turn. Related:
    the seeded default is **remote control off** for a new workspace, and an existing one
    is nudged to off **once** — but **a value the user explicitly set is respected**.

- ⚠️ **claude sometimes restarts itself, and when it does it drops the session id.**
  Switching to the full-screen TUI, restarting after sign-in, changing model — the
  restart argv is rebuilt **from the configuration flags only**, and structurally cannot
  contain the id or the display name (measured: both were on the launch command, and
  neither was on the live process). **A claude that lost its id starts a brand-new
  conversation under a random one**, so the deterministic transcript never appears
  again. Without a remedy the mirror sits at "no conversation yet" forever, the status
  is written under a different id, and **the session vanishes from the Console
  entirely** — taking usage, abort detection and reporting with it.
  The fix is a **ledger** mapping the slot to the agent's real id, recorded when a hook
  announces itself, keyed on the session-name environment variable. **That variable is
  part of the tmux session environment, so it survives the restart** — which is exactly
  why it was chosen over guesswork like matching the working directory.

- **There are two ways to hold a conversation id, and they fail differently.**
  - **Captured**: the CLI mints the id and we re-record it on **every event**, so if the
    CLI moves to another session we follow on the next event (measured: zero drift).
  - **Imposed**: we mint the id and pass it in, and everything downstream assumes it is
    still in use — **which breaks silently the moment the CLI stops using it** (the
    claude case above).
  **When you add a kind that imposes an id, you must ship the recovery path with it.**
  Recovery only runs when the imposed id exists nowhere on the CLI's side, and only
  adopts a candidate when **exactly one** matches the directory, was created after the
  slot, and is not already claimed. **When it is ambiguous, do nothing** — showing
  somebody else's conversation is worse than staying stuck.

- ⚠️ **tmux target matching is a prefix match.** `claude_foo` matches `claude_foo-sh`,
  which can misidentify — and mis-kill. **Every target reference in this repository uses
  the exact form.**
- **The CP mirrors the list into its database**, so the list is visible even while the
  workspace is stopped ([06 §6.3](06-data.md)).
- **Fork** branches a new slot carrying the conversation. An optional body branches
  **from a past message** instead; the anchor is a kind-specific opaque id, and each
  kind answers whether it can. The mechanism differs: some use the runtime's own
  parameter (which requires managed), others truncate the transcript (which works in a
  TUI).
- **Switching driver** stops and resumes the same conversation, and refuses mid-turn.
- **Model resolution at creation** checks a requested model against the live catalogue
  and expands an abbreviation into the full identifier. **An ambiguous or unavailable
  model is refused before the clone or worktree happens** — closing the "it starts and
  then dies on an invalid model" trap. If the catalogue cannot be read, the value is
  kept and the start proceeds.
  - ⚠️ copilot on the free plan has **no catalogue and only auto**, and **auto does not
    accept a reasoning-effort flag**. So the launch code passes that flag **only for a
    concrete model** — passing it with auto fails to start, which a free-plan user would
    hit every time.

## 4.3 The pattern for integrating a kind

The kinds are claude, codex, cursor, opencode, agy, copilot, kiro, plus shell and ssm.
Codex and opencode default to managed; claude, shell and ssm are TUI only.

**The surfaces a new kind must fill are the same every time** — the template was
established when opencode was added and reused for codex. **Adding one is
[20 Adding an agent kind](20-add-an-agent.md)**; this section is the shape.

| Surface | claude | codex | opencode |
|---|---|---|---|
| Default driver | tui | managed | managed |
| TUI launch | an imposed id plus resume | resume, with the id captured by a hook | a session flag, with the id captured by a plugin |
| Managed launch | — | the shared app server's thread API | the shared server's session API |
| Conversation truth | its own JSONL | its own JSONL, for both drivers | its own database, for both drivers |
| Live state | hooks plus a tmux probe | runtime events when managed; hooks and a probe when TUI | server events when managed; the plugin when TUI |
| Sign-in | its own OAuth ([08 §8.5](08-integrations.md)) | its own login (key or device flow) | an environment key, **prefixed onto the command** |
| Credential location | its own config directory, moved out of the home | its own directory | the encrypted store |
| Filesystem denylist | its config paths | its directory | its data directory |

- ⚠️ **`tmux new-session -e` does not reach the process** — it only sets the session
  environment. **Prefixing the command is this repository's convention** for injecting
  environment. Note that claude no longer needs it at all, which also removed a secret
  from the command line.
- ⚠️ **Always reap child processes.** The agent is not PID 1, so nothing collects for
  you and an un-waited child leaks a PID forever. The convenience helpers wait
  internally and are safe; **when you start a process yourself, every path — including
  the failures — must reach a wait.** The easy leak is "kill it on a start timeout and
  return", which really happened for two runtimes. The shared login-flow helper does
  kill *and* wait, and there is a regression test.
- ⚠️ **codex's hooks use the same nested schema as claude's.** Writing them flat
  **parses but silently never fires** — a known trap where resume quietly starts a new
  conversation instead.
- The token-saving wrapper works differently per agent: a hook, a plugin that rewrites
  commands, or **an instruction block, which is best-effort only**. On/off is expressed
  by the presence of the artefact, with a persisted preference as the truth.

The managed boundary is a small set of interfaces. The turn, respond and settings
endpoints are **driver-independent semantics**; managed dispatches to the structured
API and TUI to the key-input path. **The conversation body is never copied into a store
of our own** — the native store stays the read truth, and the transcript is normalised
on the way out ([decisions/0015](../decisions/0015-agent-managed-driver.md)).

## 4.4 State badges

The outward vocabulary is normalised to **working / idle / question**. Hooks fire a
subcommand that records the state to a file per session. Hooks merge additively per
session start, and **the pre-tool hook is registered per matcher**, so the token-saving
wrapper and the state hook coexist and **toggling one does not break the other**.
Managed drivers write to the same store from runtime events. The Console polls, draws
the badge, and raises a browser notification on the transitions that need a human.

## 4.5 Chat and assistants (a headless CLI)

- **Chat is not a tmux session.** It is a parallel subsystem driving the CLI in headless
  mode against its own conversation store, streamed over SSE.
- **The credentials are the same single file the interactive sessions use.** An older
  scheme of a symlink plus copy-back was removed: a refresh writes through a temporary
  file and a rename, **which turned the link into a real file**, and two processes could
  then hold different refresh tokens. User and project settings, and MCP entries, are
  deliberately excluded from chat.
- **The fallback is visible.** The conversation records the requested agent, and each
  message plus the conversation record **which backend actually ran**, so the UI follows
  a switch immediately rather than lying about it.
- **An in-container stdio MCP server** is attached to chat — no token, no egress, and
  its identity is the container itself. **Read-only by default**; a flag advertises the
  writing tools. **The gate is the visible tool set, not a permission prompt.** This is
  deliberately a separate implementation and scope from the CP's endpoint.
- **A second, narrower stdio server is materialised into the interactive CLIs.** It
  advertises **only the report tool and the browser-attach tools** and refuses the fleet
  tools both in advertisement and on call.
- **Unattended approval for codex**: a headless chat has no approval UI, so the granted
  MCP server is set to auto-approve — without it every call is cancelled. **The
  read-only sandbox is kept**, so MCP works but shell and file changes do not.
- **Assistants** are templates of persona, model, knowledge and tool scope. Asking one
  runs a single turn **with tools forced off** — one hop and no side effects, guaranteed
  structurally rather than by instruction.
- **Model resolution** snapshots the model into the conversation at creation and never
  rewrites it, protecting reproducibility from a provider changing its default. But
  **the conversation holds one model, chosen for the agent it was created for**, so when
  another backend actually runs — a fallback, or a mid-conversation switch — the model
  is re-resolved from *that* CLI's setting. **Passing the stored value straight through
  would feed one vendor's model id to another.**
- **Switching agent mid-conversation** is a dedicated endpoint (refused mid-turn). It
  changes the pin and the model and adds one notice — **the per-backend resume handles
  and message cursors are preserved**, so switching back continues the native session.

## 4.6 Git and the filesystem

- **Repositories**: clone with terminal prompting disabled so it fails fast; status,
  branches, checkout, fetch, fast-forward, delete. Submodules are best-effort after the
  clone; **the parent clone deliberately does not recurse**, because an SSH-registered
  submodule would fail the whole clone.
- **Submodule sync** fetches into the worktree's own git directory — a full re-clone
  separate from the parent. **Measured: killing an in-progress submodule update wedges
  it permanently.** The git directory is left without a HEAD, the working tree is empty,
  every later update fails, **and nothing reports it** — status is clean and the
  submodule listing looks healthy. A large submodule hits this on every start. So:
  1. past the start budget the agent **stops waiting but does not kill** — it continues
     in the background and reports the outcome;
  2. starting without the submodule is logged **and notified**;
  3. an already-wedged submodule is repaired by the one recipe that was measured to
     work — complete the transfer, then force-checkout the recorded revision. **Only
     empty working trees are touched, so local changes are never destroyed.**
- **SCM read and write**: changes, diff, log, graph, show, stage, unstage, discard,
  commit. Revisions are validated and responses are size-capped.
- **The filesystem API** defends against traversal, caps sizes and detects binaries.
  **The denylist** hides — and refuses direct access to — the agent configuration
  directories, this product's own configuration, SSH keys, git credentials and the cloud
  credential cache.
- **LFS** is installed system-wide in the image, so clone and checkout smudge normally.
- Git authentication is the unified credential helper, decrypting on demand
  ([07 §7.6](07-security.md)).

## 4.7 Transcripts and usage

- Transcripts are returned as **a window at the tail with backward paging**
  ([decisions/0009](../decisions/0009-transcript-paging.md)).
- Each kind has a reader for its own storage format, and they all normalise to a common
  turn shape. **The parsers are deliberately not merged.**
- Usage is aggregated from each CLI's own local records.

## 4.8 Secrets — the agent's responsibility

It owns the encrypted store and supplies credentials through subcommands that **never
create a plaintext file**. The key is injected by the CP at start; the agent is
indifferent to how it was provisioned ([07 §7.6](07-security.md)).

★ **One thing deliberately does not live here: a git provider's OAuth client secret**
([decisions/0052](../decisions/0052-tenant-git-oauth.md)). It is the *tenant's*
credential, so copying it into **every member's** store is avoided by keeping it in the
CP; the agent asks the CP to perform the refresh, and **the user's own refresh token
stays here.**
★ The bridge's coordinates are copied into the encrypted store at start rather than read
from the environment — **the credential helper is a separate process started by git, and
its environment cannot be guaranteed.**

## 4.9 The workspace image and its entrypoint

**The image and the agent are common to every deployment target** — that is the point of
the split ([09](09-deploy.md)).

- **The agent CLIs arrive by one of two routes**, and **the distribution default is
  lean**:
  - **Lean**: the CLIs are **not baked in** — a safe default that does not redistribute
    proprietary software. The entrypoint **boot-installs the pinned versions** from the
    official sources into the home on first start (**persistent, so later starts skip
    silently**; no network is a warning, not a failure, and it retries next time). One
    large CLI is excluded even from that and installed on demand.
  - **Baked**: an explicit knob for a deployment that wants a fast first start.
- **The version pins are the same build arguments on both routes**, and **every pin is
  written into a manifest inside the image**, which the version report, the smoke test
  and the boot-install all read. The manifest also covers the operations-tooling
  servers — two of which **cannot be asked their version by running them** (one starts a
  server, the other has no version flag), so their version is read from the installed
  package metadata instead. **A new server of that kind must be handled the same way.**

## 4.10 The browser manager

One Chromium process per workspace, started lazily and driven over a pipe, owning an
independent context and page per id. **It refuses the agent's own port, external
top-level navigation and the management endpoints**, so a page's traffic stays inside
the container's loopback.

The wire protocol sends a ready frame, then state, navigation, console and error
messages as text and **raw JPEG as binary**. From the Console it accepts only viewport,
pointer, wheel, key and text, navigation and visibility — **raw debugging protocol is
never exposed**.

The ceilings are in [ref/limits.md](../ref/limits.md). The frame rate is enforced **not
merely by throttling the send** but by delaying the acknowledgement to a one-frame
worker, **so Chromium is limited at capture and encode** rather than producing frames
that are thrown away. The pipe has fixed message and queue limits, and **when a required
event saturates them the browser is terminated and the page moves to crashed** rather
than growing the queue.

## 4.11 tmux server scope, and isolating a second instance

> **Why this section exists.** During an integration test, a second agent started on a
> different port **ran `kill-server` against the shared default socket** on shutdown,
> and **destroyed every unrelated running session — four times**, including the
> developer's own.

**The permanent fixes:**

- In production there is one agent per container, and it is the sole creator of the
  default socket's tmux server. The old shutdown relied on that. **The assumption breaks
  the instant a second instance exists** in the same environment.
- **`kill-server` is banned outright in agent product code.** Shutting down kills
  **only the sessions this instance owns** — its own metadata intersected with what is
  live — by exact target. **A live session with no metadata of ours is not touched**: it
  is impossible to tell "another instance's work" from "an orphan that lost its
  metadata". In production, killing the owned sessions leaves the server to exit on its
  own, which reaches the same end state as before.
- **All tmux execution funnels through one helper.** Setting the socket variable sends
  every call to a dedicated server, isolated from the default socket **and from an
  inherited session variable**.
- Both rules are held by tripwire tests that detect the banned call and any bypass of
  the funnel.

**How to start a second instance safely** (in-container tests, local debugging) — **all
three of these, or it collides with the real one**:

```sh
AF_TMUX_SOCKET=af-e2e-$$ \
AF_SESSIONS_DIR="$HOME/tmp/e2e-$$/sessions" \
AGENT_ADDR=:7710 AGENT_TOKEN=test-token \
./workspace-agent
```

- **The socket** — ⚠️ without it, starting from inside a tmux pane (that is, your usual
  development session) **inherits the session variable and reliably targets the shared
  server**. That was the direct cause of the incident.
- **The metadata directory** — sharing it makes the second instance believe the real
  sessions are its own and **stop them on shutdown**.
- **The port** — to avoid colliding with the real agent.
- Clean up with `kill-server` **against your own socket only**. Typing it against the
  shared one is forbidden.
- Tests isolate the same way: either a dedicated socket, or the environment variable
  when the test goes through product code.

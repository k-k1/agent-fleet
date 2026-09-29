---
audience: "anyone driving a CLI's interactive screen from code"
source_of_truth: "the code; this is the method for verifying it against a real TUI"
updated: "2026-09"
---

# 92. Driving a CLI's modal TUI — the verification playbook

English | [日本語](92-driving-a-tui.ja.md)

On a Terminal (CLI) session the Console answers an agent's modal screens — its question
prompts, plan approval, permission prompts — **by sending keys to the session's tmux
pane on the user's behalf**. That coupling **breaks silently when the CLI changes its
UI**: not as an error, but as *the wrong option being answered*. So it must be
re-verified against a real TUI whenever the behaviour looks wrong, the driving code
changes, or the CLI is updated.

None of this applies to a managed session, which has no pane to key: the Console answers
its pending interactions through the Agent's structured routes (a question through
`POST /sessions/{name}/respond`, for example).
Which kinds have a Terminal route at all is in
[the agent capability table](../../guide/ref/agents.md) — lcpp and muse, for example,
have none (their `BuildLaunch` returns `ErrNoTerminalRoute`). Which modals a kind puts up
on that route is best read from its key-sequence builder and the code that detects its
live state (see [92.4](#924-where-the-driving-code-lives)).

**The dated incident and measurement records that produced this playbook are in the
frozen archive** — they are pinned to specific CLI versions and do not belong on a
living shelf. What is here is the method, which does not expire.

## 92.1 The playbook

Stand up a throwaway session, reproduce **exactly the keys and timing the Agent
sends**, and observe the pane. The example drives claude's question modal
(`AskUserQuestion`); the method carries over to the other kinds, but their key
sequences do not (see [92.4](#924-where-the-driving-code-lives)).

> Do not touch the fleet's live sessions. **Each question costs a real turn.** Every
> command below has a reason — [92.1.1](#9211-isolating-the-probe--three-traps) says
> what goes wrong without it.

```bash
# 0) a scratch directory, a tmux socket of your own and a session id. $AF_WORK_DIR is
#    unset in a Managed session; the fallback is the documented one. If your shell does
#    not persist between commands, note the three values and reuse them.
w="${AF_WORK_DIR:-$HOME/.af-work/$(basename "$PWD")}/probe" && mkdir -p "$w"
sock="probe-${AF_SESSION_NAME:-$$}"
sid=$(cat /proc/sys/kernel/random/uuid)

# 1) start a throwaway session in the scratch directory, without the calling session's
#    variables. If claude has not trusted that directory yet, the first run stops at
#    its folder-trust prompt: read the pane before pressing Enter.
env -u AF_SESSION_NAME -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_ENTRYPOINT \
    -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_EXECPATH -u CLAUDE_PID -u AI_AGENT \
  tmux -L "$sock" new-session -d -s auqtest -x 140 -y 50 -c "$w" \
  "claude --session-id $sid --model sonnet --dangerously-skip-permissions '<a prompt that asks one question>'"

# 2) wait for the modal and look
tmux -L "$sock" capture-pane -p -t auqtest | tail -30

# 3) reproduce the Agent's input: here, the second option of a single-select, as the
#    Console's {keys} sends it. The Agent leaves 90 ms between the steps of one {keys}
#    or {seq} request, the Enter included.
tmux -L "$sock" send-keys -t auqtest Down
sleep 0.09
tmux -L "$sock" send-keys -t auqtest Enter
#    Free text instead: Down once per option to reach the type-in row, then the text
#    as a literal ({seq} text step, send-keys -l), then Enter:
#    tmux -L "$sock" send-keys -t auqtest -l 'some text'

# 4) read back what was actually answered (claude prints a "User answered …" line;
#    -J joins it if the pane wrapped it)
tmux -L "$sock" capture-pane -p -J -t auqtest -S -60 | grep -A3 "answered"

# 5) clean up — your own socket only
tmux -L "$sock" kill-server
```

A typed prompt (`{prompt}`) is paced differently from key steps: the Agent types it,
waits `inputSubmitDelay` — 20 ms for claude, 200 ms for the other kinds, overridable
with `AGENT_INPUT_SUBMIT_DELAY_MS` — and then sends Enter. codex, opencode, copilot,
cursor and kiro receive the prompt text as a bracketed paste (`load-buffer` +
`paste-buffer -p`) rather than as `send-keys -l` (`typePromptText`).

The Agent's own screen readers (`tmuxx.CapturePane`, `ReadPane` and the state probes in
`internal/tmuxx/tmuxx.go`) capture **without** `-J`, so a line longer than the pane is
split exactly as they see it. Read the pane the same way when you are checking what
state detection sees, and add `-J` only when you need one wrapped line whole.

### 92.1.1 Isolating the probe — three traps

Run from inside a session, a probe without these precautions interferes with the fleet.

1. **A tmux socket of your own** (`tmux -L "$sock"`). Without `-L`, a probe started from
   inside a pane lands on the tmux server the Agent owns, the one every running Terminal
   (CLI) session in the workspace lives on: a `kill-server` there takes them all down
   (managed sessions have no pane and are not on it). Why the Agent's
   server is shared and how its code keeps off other servers is
   [04 §4.11](04-agent.md#411-tmux-server-scope-and-isolating-a-second-instance). Name the
   socket per session, not a fixed `probe`: two sessions running this recipe would
   otherwise share one server and collide on the session name.
2. **Drop the session-name environment variable**, and the CLI's own session
   variables, before starting the probe. Every pane the Agent starts carries
   `AF_SESSION_NAME`, and claude's status hooks are installed in its user
   `settings.json`, so a probe started from inside a session fires them too — and
   `NormalizeHookSID` (`internal/agents/claude/sid.go`) **re-attributes them to the
   calling session**. Measured symptoms: the probe's question state was written **onto
   the measurer's own session** — a question card appeared in the Console for a question
   that did not exist, and the composer was blocked as if it were pending — and the
   session-id ledger pointed at the probe's conversation, so **a later resume could have
   restored the wrong conversation**. It self-healed here only because the host session
   kept firing its own hooks; measuring from an idle session would have left it.
   Inheriting the CLI's own variables is worse still (measured): the probe is treated as
   a child session and **the hooks never fire at all**. With no `AF_SESSION_NAME` the
   hooks key their state by the session id claude reports, so passing an explicit one
   fixes where the status and pending files land: you can watch them directly and clean
   up exactly that id afterwards.
3. **Start it in the scratch directory** (`-c`). Without it the probe inherits your
   working directory — a repository the Agent may already have marked trusted
   (`ensureFolderTrusted`) — so the run no longer matches a fresh one, and whatever the
   probe writes lands in your checkout. Keep throwaway files out of `/tmp` too: it is
   shared by every session.

### 92.1.2 Prompts that produce each case

Ask for one question, then follow up in the same session to cover several shapes in one
start: a single-select, a multi-select, one call carrying two questions, and a
single-select whose options carry a preview.

**Always include a label that mixes non-ASCII text and digits** (the recorded probes used
Japanese). It catches two risks at once — that typed text is ignored, and that a digit
key confirms immediately.

### 92.1.3 What to check

- Typing a label's full text leaves the modal **unresponsive**. If it reacts, filtering
  has come back and the behaviour has changed.
- `Down × i, Enter` answers **the row you intended**.
- In a multi-select, Enter **toggles rather than submits**.
- Text entered on the type-in row registers. In a multi-select, typing checks the row
  and an Enter would uncheck it again.
- When any option of a question carries a preview, that question has **no type-in row**;
  free text goes into the highlighted option's notes (`n`, the text, Enter). The layout
  is decided per question, not per form. A sequence that assumes the type-in row lands on
  "Chat about this" instead, the typed text is dropped, and claude reads the Enter as
  declining the question.

## 92.2 The regression checklist, on every CLI update

Run at least these five: ① single-select by keys, ② typing a full label and confirming
**nothing happens**, ③ multi-select toggle then submit, ④ free text on the type-in row,
⑤ free text on a previewed single-select (notes, no type-in row).

**If any of the five has changed, the sequence builder and this chapter must be updated
in the same change.**

cursor's two Terminal menus — the command approval ("Run this command?") and a plan
launch's build approval ("Ready to build?") — are read off the pane, so re-capture them
too and compare with `internal/agents/cursor/testdata`: a reworded menu reads as no menu,
and free text typed into it approves the command or builds the plan.

## 92.3 The invariants this produced

These are the shape of the fix, and worth preserving through any rewrite:

- **Answering a choice is always a key sequence, never sending the label as text.**
  Typing a label was what silently answered the wrong option.
- **While an interaction is pending, plain text input is refused**, whatever the
  source — the Console composer, a scheduled run, an external client driving the
  session. The code names the state: `question_pending`, `plan_pending`,
  `permission_pending`, `auth_expired` (claude's login has expired), and
  `interaction_pending` for any other. The gate (`promptBlocker`) is a whitelist:
  everything that is not idle or working is blocked, because Enter confirming a
  highlighted row silently is the same accident in the plan and permission modals too.
  It only sees what it reads for each kind: the status store for claude, whose hooks
  write its modals there, and for every other kind with a Terminal route the source the
  kind's own live state is read from (`kindModalProbes`). A kind with a Terminal route
  and neither fails a test, because it would fall through to a status store nothing
  fills — as codex, opencode and cursor did, where a pasted line answered the first
  option, ran the command or built the plan (measured, #1227).
- **A modal is pending only while a screen can still show it.** A pending state that
  outlives its modal refuses every send with nothing left to answer. A question asked
  before the pane's current CLI process started is not pending, nor is one whose turn
  has ended: a SIGKILLed codex or opencode leaves its question open in the rollout or the
  store for good, and neither a resume nor a later turn closes it.
- **A reject is not a key walk to a "no" row.** The plan-approval menu's length depends
  on the claude version, and a fixed `Down × 3` wrapped round onto a "Yes" row and
  approved the plan it meant to reject. Approve is Enter on the default row; reject is an
  interrupt (Escape) (`planDecision.ts`).
- **A click selects; a separate button submits.** Click-to-submit could not be taken
  back, and it destroyed the preview of the option you were comparing against.
- **Verification is not finished at the key sequence — it has to go through the delivery
  layer.** A sequence that is correct in a probe can still fail to reach the agent. For
  example, the Agent checks every `{k}` step against a whitelist of named keys
  (`allowedKey`) and refuses the whole request on one it does not know, so a notes key
  sent as `{k: "n"}` never reached the pane; it is sent as a text step instead. An
  answer that fails must say so on screen: silence looks like success.

## 92.4 Where the driving code lives

What to re-read when this playbook finds a change:

- **The key sequences** — `console/src/features/mirror/questionKeys.ts`:
  `buildClaudeSeq` / `buildClaudeSubmit` for claude's tabbed modal, `buildMenuSeq` for
  the one-page-per-question menus (codex, opencode, agy; agy's write-in row is entered
  with Enter before it takes text), `buildRespondAnswers` for managed sessions. Plan and
  permission buttons are wired in `MirrorView.tsx`.
- **What counts as pending** — `DriveState` (`internal/sessionx/agent.go`) derives a
  session's live state: the status store (`internal/status`), which claude's hooks fill
  with its question, plan and permission modals, and, for some kinds without such hooks,
  per-kind readers of a transcript, event log or pane (`opencode.LiveState` or
  `kiro.LiveState`, for example). The gate, `promptBlocker`, reads the same per-kind
  sources through `kindModalProbes` (`session_io.go`); for codex, opencode and cursor
  that is each kind's `TerminalModal` (see 92.3). `PendingModal` in
  `internal/agents/modal.go` is a different seam:
  it preserves a modal left pending when a session stops (asked before a deliberate halt,
  or when the session list first notices a pane that has gone) — do not read the live
  modal shapes from it.
- **The delivery** — `POST /sessions/{name}/input` in
  `workspace/agent/internal/sessionx/session_io.go`: `{keys}` (`sendNamedKeys`), `{seq}`
  (key and text steps), `{prompt}` (`submitPromptTUI` → `typeLineAndSubmit`), the
  `allowedKey` whitelist and the `promptBlocker` gate.
- **What pins it** — `questionKeys.test.ts` pins each builder's output and reads
  `allowedKey` from the Go source so that every `{k}` a builder emits is one the Agent
  accepts. These are unit tests: they never meet a real CLI. The manually dispatched
  `claude-tui-contract.yml` runs claude live for state detection
  (`TestClaudeTUIContractLive`, the live counterpart of the unit tests over the capture
  corpus in `internal/tmuxx/testdata/footers`) and the plan-approval default row
  (`TestClaudePlanApprovalContractLive`). No automated test answers a real question
  modal with the Console's key sequences — that is what this playbook is for.

# 92. Driving a CLI's modal TUI — the verification playbook

English | [日本語](92-driving-a-tui.ja.md)

Audience: anyone driving a CLI's interactive screen from code
Source of truth: the code; this is the method for verifying it against a real TUI
Updated: 2026-08

The Console answers an agent's modal screens — its question prompts, plan approval,
permission prompts — **by sending keys to tmux on the user's behalf**. That coupling
**breaks silently when the CLI changes its UI**: not as an error, but as *the wrong
option being answered*. So it must be re-verified against a real TUI whenever the
behaviour looks wrong, the driving code changes, or the CLI is updated.

**The dated incident and measurement records that produced this playbook are in the
frozen archive** — they are pinned to specific CLI versions and do not belong on a
living shelf. What is here is the method, which does not expire.

## 92.1 The playbook

Stand up a throwaway session, reproduce **exactly the keys and timing the agent
sends**, and observe the pane.

> Do not touch the fleet's live sessions. Use a scratch directory. **Each question
> costs a real turn.**

```bash
# 1) start a throwaway session (the first run needs Enter for the trust prompt)
tmux new-session -d -s auqtest -x 140 -y 50 "claude '<a prompt that asks one question>'"

# 2) wait for the modal and look
tmux capture-pane -p -t auqtest | tail -30

# 3) reproduce the agent's input (-l is a literal keystroke; the agent uses ~90 ms
#    between keys, and a shorter delay before the Enter that submits)
tmux send-keys -t auqtest -l 'some text'
tmux send-keys -t auqtest Down
sleep 0.09

# 4) read back what was actually answered
tmux capture-pane -p -t auqtest -S -60 | grep -A3 "answered"

# 5) clean up
tmux kill-session -t auqtest
```

### 92.1.1 Isolating the probe — two traps that were actually hit

The naive form above is **dangerous when run from inside a session**. Fix two things.

1. **Use a dedicated tmux socket** (`tmux -L probe …`). The default socket is **the
   server the agent itself owns**: killing it takes **the whole fleet** down, and your
   probe appears to the agent as an orphan with no metadata. A separate socket is a
   separate server.
2. **Drop the session-name environment variable**, and the CLI's own session
   variables, before starting the probe. Inheriting them makes the probe's status hook
   **re-attribute itself to the calling session**. Measured symptoms: the probe's
   question state was written **onto the measurer's own session** — a question card
   appeared in the Console for a question that did not exist, and the composer was
   blocked as if it were pending — and the session-id ledger pointed at the probe's
   conversation, so **a later resume could have restored the wrong conversation**. It
   self-healed here only because the host session kept firing its own hooks; measuring
   from an idle session would have left it. Inheriting the CLI's own variables is worse
   still: the probe is treated as a child session and **the hooks never fire at all**.

```bash
env -u AF_SESSION_NAME -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_ENTRYPOINT \
    -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_EXECPATH -u CLAUDE_PID -u AI_AGENT \
  tmux -L probe new-session -d -s p1 -x 200 -y 50 -c /tmp/probe \
  "claude --session-id $(uuidgen) --model sonnet --dangerously-skip-permissions"
```

Passing an explicit session id fixes where the status and pending files land, so you
can watch them directly and clean up exactly that id afterwards.

### 92.1.2 Prompts that produce each case

Ask for one question, then follow up in the same session to cover several shapes in one
start: a single-select, a multi-select, and one call carrying two questions.

**Always include a label that mixes text and digits.** It catches two risks at once —
that typed text is ignored, and that a digit key confirms immediately.

### 92.1.3 What to check

- Typing a label's full text leaves the modal **unresponsive**. If it reacts, filtering
  has come back and the behaviour has changed.
- `Down × i, Enter` answers **the row you intended**.
- In a multi-select, Enter **toggles rather than submits**.
- Text entered on the type-in row registers.

## 92.2 The regression checklist, on every CLI update

Run at least these four: ① single-select by keys, ② typing a full label and confirming
**nothing happens**, ③ multi-select toggle then submit, ④ free text on the type-in row.

**If any of the four has changed, the sequence builder and this chapter must be updated
in the same change.**

## 92.3 The invariants this produced

These are the shape of the fix, and worth preserving through any rewrite:

- **Answering a choice is always a key sequence, never sending the label as text.**
  Typing a label was what silently answered the wrong option.
- **While an interaction is pending, plain text input is refused** with a specific error
  code, whatever the source — including an external client driving the session. The gate
  is a whitelist: everything that is not idle or working is blocked, because Enter
  confirming a highlighted row silently is the same accident in the plan and permission
  modals too.
- **A click selects; a separate button submits.** Click-to-submit could not be taken
  back, and it destroyed the preview of the option you were comparing against.
- **Verification is not finished at the key sequence — it has to go through the delivery
  layer.** A sequence that is correct in a probe can still fail to reach the agent.

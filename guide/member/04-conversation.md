---
audience: "anyone talking to a running agent"
updated: "2026-10"
---

# 04. Talking to a session — replying, plans, highlights, suggestions, translation

English | [日本語](04-conversation.ja.md)

This chapter is about the conversation view of a session: reading and replying to the agent, queueing follow-ups, plans, reply suggestions, what to do when a turn is interrupted, and saving or translating a conversation. To start a session, see [03 Starting sessions](03-starting-sessions.md); to stop or tidy one up, see [05 Stopping and tidying up](05-stopping-and-tidying.md).

## Reading and replying to a running agent's conversation

claude, codex, cursor, copilot, kiro, agy, opencode, lcpp and muse sessions can be driven from the **Chat** view. With Terminal (CLI)
execution, switch with **Chat ⇄ Terminal** at the top of the pane; with Managed execution
you use only the chat view from the start. In the chat view the
exchange reads as **per-turn Markdown**, and you can reply right there. It suits
following progress on your phone and firing back short replies.

When the agent is waiting on your judgment, a corresponding card appears. Which cards and
choices are available depends on the agent and the state.

- **Question**: pick a choice (or type a free-form answer), then press **"Submit answer"**. Clicking a choice only selects it, so you can change your mind (and compare the previews some choices come with) before anything is sent. What you have picked and typed is kept while you go and look something up: switching to another tab, or to the terminal, brings the card back as you left it. It is cleared once the answer is sent, or when you cancel the question.
- **Plan awaiting approval**: either "Approve and run" the proposed plan or "Reject (keep going)".
- **Awaiting permission**: "Allow" / "Deny" the edit or command run (you can also choose to auto-allow for the rest of this session).

Sending a prompt from the input field below works just like typing into the terminal
(image paste included). The **mode toggle** lets you pick Plan ⇄ Build, and while it's
running you can halt it with **"Stop"** (Esc). Right after sending, the message shows as
sent, and it's reflected in the conversation once the agent starts processing.

**Work-in-progress** and **thinking** blocks fold and unfold from their heading. When a long one is
open, a **"Close"** also appears at the bottom of the body, so you can fold it from where you
finished reading instead of scrolling back to the heading.

A stopped session is read-only history; to reply, restart it with **"Resume and continue"**.

### Questions and plans left by a stopped session

When a session stops while a question, a plan or a permission request is still waiting for you
(whether you stopped it, it was folded away after waiting, or the workspace stopped), the card
stays in its chat, headed **"Question left unanswered at stop"**, **"Plan awaiting approval at
stop"** or **"Permission requested at stop"**, and the notification center tells you. The dialog itself did not survive the stop, so whatever you
choose reaches the agent as an ordinary message, and the session is resumed to receive it.

- **Question**: pick and type as on a live card, then **"Answer and resume"**.
- **Plan**: an optional "Notes or changes" box, then **"Approve and run"** (it asks first: the
  session resumes and runs the plan straight away, and that cannot be undone) or **"Do not
  approve"** (the session resumes and is asked to revise the plan, or gets your notes).
  **"Review in another session"** is offered too; see
  [Having another session review the plan](#having-another-session-review-the-plan).
- **Permission**: the tool call it asked about is gone, so the only choice is **"Resume and
  continue"**; the agent is told what it had asked and tries again if it still needs to.
- **Discard** removes the card without resuming the session.

A card that is not answered is dropped after 14 days.

### Stopping a turn when something is queued

What **"Stop"** does with messages that are waiting in the queue depends on the execution method.

**Managed** sessions stop in two stages:

- **The first stop ends the running turn only.** What is queued (your own follow-ups and other
  sessions' messages alike) then runs, one message per turn, in order. This is what you want when
  you queued a correction ("no, do X instead") and stop the turn so the correction takes effect
  now. On **codex** and **muse** a message you send while a turn runs is not queued: the agent takes
  it into the running turn at once, so there is usually no need to stop. If you stop anyway, it
  ends with that turn; the next turn still sees it in the conversation.
- **A second stop ends the rest.** Any stop until the queue has run out (and the last turn it
  started has settled) stops what is running and discards everything still queued, messages from
  other sessions included. It needs no timing: you can press it a minute later. Sending a new
  message of your own starts over, so the stop after that is a first stop again. A double click on
  Stop counts as two stops.
- **"Stop and discard the queue"** in the menu next to Stop does both at once, whenever you press
  it. The menu is there while the session runs or has anything queued, also while a question or
  approval card is shown, and also when the chat has not shown you the queue yet. It is
  highlighted, with a count, while the chat shows something queued.
- **Discarded input comes back.** A notice says how many queued messages were discarded.
  **"Put back"** returns your own discarded messages to the input box one at a time, without
  sending them; messages from other sessions, the operator or a schedule are only listed. A
  message you sent a moment before pressing Stop, which had not reached the agent yet, comes back
  the same way. The last five discards per session are kept in the Agent's memory; they go, like
  the queue itself, when the session is stopped, archived or switched to another execution
  method, and when the Agent restarts.
- **A queued message can be taken out without stopping.** A queued bubble offers **"Back to
  input"** and **"Remove"** until the message is being handed to the agent; after that it shows
  without actions.

What a stop can never take back is input the agent already holds. On codex and muse a stop that
arrives while a message is being handed over still stops it the moment the agent starts it (lcpp
runs inside the Agent and stops it the same way); on copilot, cursor, kiro and OpenCode that stop
is best effort, and the message can take a first step before it stops.

**Terminal (CLI)** sessions keep the CLI's own behaviour: Stop sends the key that CLI stops a turn with (Esc, two Esc for OpenCode, Ctrl+C for cursor and copilot) and the CLI decides. cursor and copilot get Ctrl+C only while the turn is running; otherwise Stop answers that nothing is running. A second Stop click while one is being sent is ignored.
The discard action and the notice are not offered there, because the queue lives inside the CLI.
This is what each CLI did with a prompt typed while its turn was running, measured once per CLI.
They are observations, not promises, and a CLI update can change them:

| CLI | Stop while a prompt is queued | The pane is closed while a prompt is queued |
|---|---|---|
| claude | The turn stops and the queued prompt runs next | The queued prompt is lost |
| codex | The turn stops and the queued prompt runs next | The queued prompt still runs: codex's background process outlives the pane |
| opencode | Stop sends two Esc about half a second apart (one Esc only arms the interrupt). The turn stops and the queued prompt runs next | The queued prompt still runs: opencode's background server outlives the pane |
| cursor | Stop sends Ctrl+C: the turn stops and the prompt is left unsent in the CLI's panel (a plain Esc would cancel the queued prompt and let the turn run on) | Not restored or run when the CLI resumes |
| kiro | The turn stops and the queued prompt runs next | Not restored or run when the CLI resumes |
| copilot | Stop sends Ctrl+C: the turn stops and the queued prompt is dropped (a plain Esc does not interrupt) | Not restored or run when the CLI resumes |
| agy | Not measured (Stop still sends one Esc; the reply says the key is unmeasured) | Not measured |

Messages from other sessions to a Terminal session follow the same rules, with these reports to
the sender:

- A message to a claude Terminal session in the middle of a turn is reported as queued, not
  delivered: claude keeps it in memory only, so ending the pane before the turn ends (stopping,
  archiving or recreating the session) loses it.
- A message to a cursor Terminal session whose CLI refuses it at submit (for example a model the
  plan cannot use) is reported the same way, with the CLI's error text: the message stays in the
  input box and nothing runs it, so the sender is told not to resend.
- A message to a cursor Terminal session whose input box still holds unsent text (a prompt the CLI
  refused earlier, or a draft) is refused, because cursor would run the two as one prompt. The
  sender is told to have that text sent or cleared in the pane first.

**Rejecting a plan** and **having another session review the plan** (including **"Review again
in …"**) send the same stop, and follow the same rules. **Cancelling a question** follows them on codex only, where cancelling stops the
turn; the other agents answer the question as declined and leave the queue alone.

### Holding follow-ups before sending (send queue)

While a turn runs, a plain send goes to the agent at once. To write several follow-ups and
send them later, use the **▾ on the right edge of the Send button** (it appears only while a turn runs) and choose **"Add to waiting list"**, or press **Alt+Enter**, instead: the message is
held in a **Waiting to send** list above the input box and nothing reaches the agent or the
conversation yet.

- **Edit, reorder, delete**: each row has up / down arrows, **Edit** (Enter saves, Shift+Enter
  adds a line, Esc cancels; Enter while an IME candidate is open does not save) and **Delete**.
  A message held with attachments keeps them (shown as "+N").
- **Order of sending**: when the turn ends, the first row is sent, then the next one once the agent
  has visibly started that turn and it ends, and so on. Items never go out two at a time. While you
  are editing a row, nothing is sent; it continues when you save or cancel.
- **Send now** sends one row immediately, whatever the session is doing. On **codex** and **muse**
  (Managed) the agent takes it into the running turn; on every other agent it is accepted and
  handled after the running turn, exactly like a plain send during a turn.
- **Stopping pauses it.** Pressing **Stop**, rejecting a plan, sending a plan to
  **"Review in another session"** or **"Review again in …"**, and cancelling a question with Esc on
  a Terminal session all hold the rest of the list, so the stop is not followed by your queued
  messages; the header then says **"Auto-send is paused because you stopped the turn."** and
  **Resume** lets it go on. A send the agent refuses also pauses the list, and the message stays at
  its place. While the session is stopped nothing is sent; the rows stay in the list.
- **It is per session and temporary.** The list stays when you switch panes or sessions in the
  same page, and is cleared when you reload the page; it is never stored on the server. It sends
  only while that session's chat is open. It is not the **Memo queue** ([10 Chat and memos](10-chat-memo.md#memo-queue); saved, shared across
  devices) and not the messages other sessions send to this one.

### Reusing something you sent earlier

With the field empty, **↑↓** walk back through the prompts you sent in this conversation (on a
phone, the **∧∨** buttons beside the field).

To find one fifty messages back, press **Ctrl+R**. The same **incremental history search** bash
has opens above the field and offers your past prompts containing what you type, newest first.
The match is shown in the input itself, so even a long prompt can be read in full before you
commit to it.

- **Ctrl+R** goes further back, **Ctrl+S** comes forward (**↑↓** do the same). The "3/12" on the
  right is where you are in the matches.
- **Enter** (or **Tab**) **only puts it in the input; nothing is sent**, so you can edit it first.
- **Esc** (or **Ctrl+G**) cancels and gives you back whatever you were typing before the search.
- When nothing matches any more it says "No match" and the last match stays in the input.
- Phones have no Ctrl key: open it from the **magnifier button** to the left of the input.
- What is searchable is exactly what **↑↓** recall: the prompts you sent in this conversation,
  `/commands` included. Other sessions' inputs are not in it.

### Writing comments on a plan

When you want to say "change just this part" about a long plan, you don't have to quote it and
retype. **"Open in pane"** on the plan card opens the body in a pane marked for **review**, and
**selecting a passage lets you attach a comment to it**.

The plan card then sends them all at once with **"Send comments (N)"**. For a plan awaiting
approval it reads **"Send feedback and keep planning (N)"**: the plan is not approved, because
delivering the body requires closing the approval dialog, so the two go together. The agent comes back with a revised plan that takes
your comments into account.

### Having another session review the plan

Instead of reading it yourself, you can have **another agent read it**. **"Review in another
session"** on a plan awaiting approval opens the usual launch dialog; the only difference is
that the prompt asking for a review is already filled in. You pick the agent and the model
there, so you can show the plan to a different model than the one that wrote it.

- **The plan is rejected at that point** (the button says so). The findings come back as a
  session-to-session message, and a session frozen on an approval dialog cannot receive text,
  so it has to have its hands free first. The planning session stays in plan mode, takes the
  findings, and proposes a revised plan.
- The reviewer starts in the **same working copy** by default, so it sees your uncommitted work
  too. You can pick a new worktree in the launch dialog instead.
- The reviewer folds itself away when it is done. The session stays in the list, so you can open
  it later to see **why** a given point was raised.
- **A plan left by a stopped session** can be reviewed the same way, from the
  [card it left behind](#questions-and-plans-left-by-a-stopped-session). Nothing is rejected
  there and the session is not resumed; once the reviewer session has been created the card is
  removed (cancelling the launch dialog keeps it), and the reviewer's findings resume the session.
- **"Review again in ‹reviewer›"** appears on the card of a revised claude plan when a session
  launched from that plan's card is still there. It sends the revised plan back to that reviewer
  as a message, so the second round sees its earlier findings, instead of starting a new reviewer.
  A stopped reviewer is resumed first; one in the middle of a turn gets the message when the turn
  ends. On a live card the plan is rejected as with the first review, and if the send fails after
  that, the error toast offers **"Send again"**. On the card of a stopped session nothing is
  rejected, and the card is removed only once the plan has been sent. The link between plan and
  reviewer is kept in this browser only, so another browser or device shows only "Review in
  another session".

### Calling a skill or a command

The button beside the input field (it shows the trigger character, **`/`** or **`$`**, or **✦** for an
agent that has no trigger) opens **the skills and commands this session can actually call**.
Typing the trigger at the head of an empty input opens the same list (`/` for claude, cursor and
opencode, `$` for codex; the full-width `／` and `＄` a Japanese IME produces are accepted too).

- Each entry carries its description, an argument hint, and where it came from: **user**, **CLI**
  or **shared**.
- Picking one **only inserts it into the input; nothing is sent.** Add the arguments, then send.
- From the keyboard: **↑↓** to move, **Enter** (or **Tab**) to insert, **Esc** to close.
- **Skills written for another agent** and left in the repository are listed too. Picking one
  inserts "read that file and follow its instructions", so the content is usable even by an agent
  that has no skill mechanism of its own.
- See [agents.md](../ref/agents.md) for which agents offer the picker.

### Reply suggestions

Above the input field, chips offer short replies you use often (OK, go ahead, commit …) plus
suggestions that follow the latest reply. Clicking inserts one into the field; **hold Ctrl, ⌘ or
Alt while clicking to send it immediately**. The suggestions learn from the short messages you
send, and right-click (long-press on touch) lets you **pin** or remove one.

It works from the keyboard alone: with the field empty, **Tab** moves to the chips, **←→** moves
between them, **Enter** inserts, **Ctrl (⌘) +Enter** sends, and **Esc** returns to the field (if
you set Enter to send, Enter and Ctrl+Enter swap roles).

The **✨ button** hands the recent conversation to an AI and adds suggestions that fit the
context (it only spends tokens when you press it). Show or hide all of this in ⚙ Settings →
Keys.

### Branch from a past message

Hover (or focus) one of **your own** past messages and **"Branch here"** appears under that
bubble. Pressing it creates a **new session** carrying the conversation up to that point.
**The current session is left exactly as it is.**

Use it for "I want to try a different approach from that instruction" or "I took a wrong
turn back there and want to redo it", without copying and pasting the conversation. There
are two ways to branch:

- **Redo this message** (default): the message itself is left out. The branch opens in the
  state you were in just before sending it, with your original wording waiting in the
  composer as a **draft**. Editing and resending, or just resending, is one action either way.
- **Continue after it**: the message and the reply it got are both carried over. Use this
  when you want to head in a different direction from a point that went well.

The confirmation dialog shows the branch point, how many exchanges are carried over, and
that the original stays.

**Don't confuse it with handoff.** A handoff *summarises the conversation and passes it to a
different agent*; a branch *copies the conversation as-is within the same agent*. Branch when
the exact wording and the fine details matter; hand off when you want another agent to take over.

If the button isn't there, one of these applies: you can't branch from the agent's messages,
nor from one that's still being sent. Only **claude, codex, opencode, copilot, lcpp and muse**
sessions support it, and codex and opencode only in **managed** execution (the CLI launch command
has no way to pass a branch point; lcpp and muse are always managed). One exception: an
**opencode 2** Terminal (CLI) session attached to the shared server can branch too, because that
server takes the branch point. A Terminal session on a private server (or on opencode 1.x) cannot.

## Highlighting a line in a conversation

Select text in a conversation and a **Highlight** pill appears. Pick a colour (yellow,
green, blue or pink) and the passage stays marked.

- The marks are part of the conversation, not of your browser. Anyone the session is
  shared with sees the same passage marked, in the same place, and the **Highlights**
  strip under the conversation header lists them with the number of people who added
  them.
- **Remove highlight** takes yours away. Removing one does not touch anybody else's.
- Colour carries no meaning the product enforces; it means whatever your team decides.
- On a phone or tablet the colours appear in a bar at the **bottom of the screen** instead of
  beside the selection: your browser's own Copy / Share menu already covers the space just above
  whatever you selected.

Use it when a conversation is long and the interesting part is three screens up: a
highlight survives, where "look at the bit about the retry" does not.

## Seeing what a session changed

The **Changed files** strip under the conversation header lists the files that session
actually touched, and takes you straight to one. Click its heading to fold it open or
closed; the choice is remembered per session. **Ctrl+P** (**⌘P** on macOS), then **Tab**
to "Changed files", reaches the same list.

Each row carries a badge: **Untracked**, **Unstaged**, **Staged**, **Committed** or **No diff**
for a file in the working copy, **Session scratch** for a file in `$AF_WORK_DIR` (deleted with
the session), **Outside working copy** for anything else. A file in an agent's own memory folder
is badged **Claude memory**, **Codex memory** or, for another agent, **Agent memory**, and opens
read-only. That badge means the agent kind's own memory files, not Agent Fleet's
[agent memory](18-agent-memory.md).

It answers the question you have at review time ("what did this thing do to my
working copy?") without reading the whole conversation back. It is derived from the
conversation and the repository together, so it reflects what happened, not what the
agent said it would do.

## Reply suggestions

Above the message box, **Suggest replies with AI (from the recent conversation)** offers
a few things you might plausibly want to send next. They are suggestions, not actions:
nothing is sent until you choose one.

- **Always show (pin)** keeps a suggestion around.
- **Stop suggesting this** removes one, and sending the same text yourself brings it back.
- If the conversation is still short, or no AI is available for it, you are told so
  rather than shown an empty box.

## When a turn is interrupted

An agent's turn can be cut off by something outside the conversation, such as a provider
error, a dropped connection or a usage limit. When that happens the conversation is not
silently left half-finished:

- You are notified, including when a run resumes after a usage limit resets.
- **Auto-resume interrupted turns** (in the assistant's settings) lets the assistant
  restart the turns that a retry would fix, rather than leaving them for you to notice.

Only interruptions that a resend actually repairs are resumed. A turn that failed
because of what it was asked to do is left alone, because retrying it would just fail
again.

---

Related: [03 Starting sessions](03-starting-sessions.md) for launching and resuming ·
[ref/features.md](../ref/features.md) for the catalogue ·
[ref/agents.md](../ref/agents.md) for which agents support what

## Saving a conversation as Markdown

The **⋯** menu of any session that has a conversation offers **"Export as Markdown"** with three
choices: **"Conversation only (no tools)"** keeps what you and the agent said, asked and planned
and leaves tool calls out entirely, **"Tool names only"** also reduces each tool call to one line,
and **"Including tool output"** adds command output and edit diffs. The
file covers the whole conversation, not just the part the chat view has loaded. Thinking and a
subagent's own exchanges are left out. Strings that look like tokens or passwords are masked as
`[secret redacted]`; the masking is a best-effort pattern match, so read the file before you
share it. Times are shown in your browser's time zone. The workspace has to be running; the
session itself may be stopped.

The file is named after the session (`<session>.md`, or `session.md` when the name itself looks
like a secret). It starts with the session, the agent, the working folder, the branch and the
export time; these labels and the file's headings follow the Console's display language, not the
browser's. A question the agent is still waiting on is included at the end. When the export
fails, the message says why (the error, or the HTTP status).

## Reading an answer in your own language (translation)

When an agent answers in a language you don't read, a **"Translate"** button appears under that
answer in the conversation view (the mirror). Pressing it replaces just that answer with your
display language (or the answer language, if you fixed one in settings); **"Original"** brings the
agent's own text back.

- **No session turn is used.** The translation is one model call made outside the session: the
  conversation does not move and the agent's context does not grow. That is what makes it different
  from sending "please answer in Japanese" again.
- **Tokens are spent only when you press it.** It shows up in usage as "Answer translation (mirror)".
- **A translation is kept until that session is deleted.** The same text is free from the second
  press on, and the translation is still there after you re-open the pane, or open the session on
  your phone.
- **Only prose is translated.** Code blocks, commands, paths and tool output stay exactly as they
  were. Marks (highlights) stay on the original, so they are hidden while the translation is shown.
- The button appears only on an answer **written in a language other than your own**. An answer that
  genuinely mixes both does not get one (re-saying what you can already read buys nothing), though a
  few words in the other language (a Console label quoted as-is, a product name) do not withhold
  it. Someone else's shared session does not get one either: a translation runs in the owner's
  Workspace.
- Turn it off under Settings > AI assistance, **Answer translation (mirror)**.

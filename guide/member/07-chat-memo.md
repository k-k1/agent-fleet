---
audience: "anyone using the assistant chat or the memo queue"
updated: "2026-08"
---

# 07. Chat and memos — quick questions, translation, and memo capture

English | [日本語](07-chat-memo.ja.md)

## What the assistant chat is

The **chat** you start from **Assistants** in the left pane is a different thing from a work
session. The name resembles the "chat view" used with a session's Managed execution, but
this one is a **repository-free conversation** with no working directory or persistent
execution state. It's a good fit for translating or summarizing Markdown documents and
answering small questions.

Here's how to think about **choosing between it and a session**.

- **Chat** — short-to-medium questions, translations, summaries. Things like "translate this text into Japanese" or "what does this function do?", where an answer on the spot is all you need.
- **Session** — work that actually reads and writes files, code changes, bulk processing of large files. If file output is involved, go to a session (you can hand over files with "Send" in [04 Files](04-files.md)).

Start a conversation with **+ (New chat)** in the **Assistants** section. Send with Ctrl+Enter
(you can switch to Enter-to-send in settings). The other side's messages are labeled
"Assistant", yours "You", and "Thinking…" appears while it's working. You can also
**paste an image** into the input field and send it along (for example, showing a
screenshot and asking about it).

### Purpose-built assistants and translation

Assistants with specific roles are provided, such as guidance on using Agent Fleet or SRE
consultation. Type ordinary questions straight into a new chat; for translating or
summarizing files, use a file's right-click menu.

You can also set up your own purpose-built assistant with **"Create assistant"**. You
configure a name, a description (the greeting when a conversation starts), a persona
(instructions on role and tone), the agent / model to use, and **tool permissions**.

If you leave the model blank, new conversations use the default model that fits the chosen
agent. Currently the defaults are **Luna for codex** and **Nemotron for opencode**. If you specify
a model here, that choice wins, and the model of a conversation you've already started
doesn't change afterwards.

Each reply carries the model that produced it next to the agent name (e.g. `sonnet 5`), the
same way a session's mirror labels its turns. It is recorded per reply, so earlier answers
keep the model they were actually written with even if the conversation later switches model
or falls back to another agent. When a reply shows no model, the CLI ran on its own default
and does not tell us which model that was (cursor's Auto, for instance) — we leave it blank
rather than print a guess.

- **None** — answers within the chat alone, with no external tools. This is plenty for translation and summarization.
- **AF read** — can read your workspace's session list, statuses, and output, plus agent usage / limits (claude / codex usage rates and reset times) and each session's context size and cumulative token spend (no writing).
- **AF write** — in addition to reading, can send prompts to sessions (doing work on your behalf). Grant this only for trusted uses.

The built-in **Fleet Operator** is the flagship example of "AF write": from chat it can
direct everything from launching sessions to giving instructions and receiving completion
reports. See [08 Fleet Operator](08-organising.md) for details.

For example, for a translation assistant you might put "You are a technical-document
translator. Return only the translated text." in the persona, and "Send me text and I'll
translate between Japanese and English." in the greeting.

### Switching the agent mid-conversation

The **agent priority** list on ⚙Settings → the "Assistant" tab applies to conversations you
start from now on (and to short one-off calls such as title suggestions). To move a
conversation that is **already under way** to a different CLI, click the agent chip in the
chat header (e.g. `Claude ▾`) and pick another one.

- Nothing is lost. The history so far is handed to the new agent with your **next message**,
  so that one turn costs extra tokens.
- The model is re-resolved from the **row of the CLI you switched to** on ⚙Settings → the
  "Assistant" tab (a model you had pinned for the previous agent is not carried over).
- CLIs you have not connected can't be selected (sign in under Settings → Connections and
  they appear). You also can't switch while a reply is being generated — stop it first.
- You can switch back. That agent resumes from where it last answered and only receives the
  part of the conversation it missed.

The chat's **reply language** is set with "Reply language" on ⚙Settings → the
"Assistant" tab. "Match input" replies in the language of the text you hand over, while
choosing 日本語 / English answers in that language even for text in other languages
(the file right-click "Translate with assistant" is exempt — it always translates according to the source text).

### When a conversation gets long (a context rule of thumb)

The more exchanges you pile up, the more context keeps flowing to the model. Once the
first response comes back, a **context bar** (usage / limit · %) — the same one as a
session's mirror — appears under the chat header. When usage passes **80%** the bar
turns a warning color, and a one-time notice arrives in the conversation (it also shows
up in the notification center).

There are two ways to deal with it.

- **Compact (keep going as is)** — press **"Compact"** at the right end of the context
  bar to summarize the conversation so far and **hand only the summary to a fresh
  session**, continuing in the same chat. The on-screen history stays intact; only the
  summary carries over (producing the summary costs one turn's worth of tokens). Good
  when you want to keep a long conversation going as is.
- **Open a new chat** — if you're at a natural break, opening a new chat and writing out
  just the key points yourself is the surest way.

Either way, the longer you push on with a bloated conversation, the more you invite
degraded response quality, failed turns, and ballooning token spend.

There's also a safety net if you leave it alone. If usage is still above **90%** when
you start the next exchange, it auto-compacts first (summary handoff) before responding
(can be turned OFF with "Auto-compact chat context" on ⚙Settings → the "Assistant" tab).
Furthermore, even if the context blows past the limit before compacting, the system
automatically attempts a summary handoff and then retries the reply. If it was exceeded
too far to recover, a notice to that effect appears in the conversation (and the
notification center), so deal with it via "Compact" or a new chat.

### Put what must not be forgotten in the work plan

There is one slot that is **carried forward verbatim, never summarised** — through compaction
and into every new session: the **work plan**, next to the context bar. Write down the
constraints (assumptions that hold from here on), the givens (facts the next move depends on)
and what comes next (order, dependencies), and a long conversation stops drifting off its base.

- **"Edit"** writes it directly. **"Refresh"** re-derives it from the recent conversation — press
  it right after a discussion changes the direction.
- On compaction it is updated automatically to match the recent conversation.
- **"Clear"** empties it (the conversation history and the handoff summary are untouched).
- It is not a list of finished work. It works when it holds only what you would get wrong
  without.

## Reading and replying to a running agent's conversation

claude, codex, cursor, copilot, kiro, agy, and opencode sessions can be driven from the **Chat** view. With Terminal (CLI)
execution, switch with **Chat ⇄ Terminal** at the top of the pane; with Managed execution
you use only the chat view from the start. In the chat view the
exchange reads as **per-turn Markdown**, and you can reply right there. It suits
following progress on your phone and firing back short replies.

When the agent is waiting on your judgment, a corresponding card appears. Which cards and
choices are available depends on the agent and the state.

- **Question** — pick a choice (or type a free-form answer), then press **"Submit answer"**. Clicking a choice only selects it, so you can change your mind — and compare the previews some choices come with — before anything is sent.
- **Plan awaiting approval** — either "Approve and run" the proposed plan or "Reject (keep going)".
- **Awaiting permission** — "Allow" / "Deny" the edit or command run (you can also choose to auto-allow for the rest of this session).

Sending a prompt from the input field below works just like typing into the terminal
(image paste included). The **mode toggle** lets you pick Plan ⇄ Build, and while it's
running you can halt it with **"Stop"** (Esc). Right after sending, the message shows as
sent, and it's reflected in the conversation once the agent starts processing.

**Work-in-progress** and **thinking** blocks fold and unfold from their heading. When a long one is
open, a **"Close"** also appears at the bottom of the body, so you can fold it from where you
finished reading instead of scrolling back to the heading.

A stopped session is read-only history; to reply, restart it with **"Resume and continue"**.

### Writing comments on a plan

When you want to say "change just this part" about a long plan, you don't have to quote it and
retype. **"Open in pane"** on the plan card opens the body in a pane marked for **review**, and
**selecting a passage lets you attach a comment to it**.

The plan card then sends them all at once with **"Send comments (N)"**. For a plan awaiting
approval it reads **"Send comments and reject (N)"** — delivering the body requires closing the
approval dialog, so the two go together. The agent comes back with a revised plan that takes
your comments into account.

### Calling a skill or a command

The button beside the input field — it shows the trigger character, **`/`** or **`$`** (**✦** for an
agent that has no trigger) — opens **the skills and commands this session can actually call**.
Typing the trigger at the head of an empty input opens the same list (`/` for claude, cursor and
opencode, `$` for codex; the full-width `／` and `＄` a Japanese IME produces are accepted too).

- Each entry carries its description, an argument hint, and where it came from — **user**, **CLI**
  or **shared**.
- Picking one **only inserts it into the input; nothing is sent.** Add the arguments, then send.
- From the keyboard: **↑↓** to move, **Enter** (or **Tab**) to insert, **Esc** to close.
- **Skills written for another agent** and left in the repository are listed too. Picking one
  inserts "read that file and follow its instructions", so the content is usable even by an agent
  that has no skill mechanism of its own.
- Which agents offer the picker is in [agents.md](../ref/agents.md).

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
turn back there and want to redo it" — without copying and pasting the conversation. There
are two ways to branch:

- **Redo this message** (default) — the message itself is left out. The branch opens in the
  state you were in just before sending it, with your original wording waiting in the
  composer as a **draft**. Editing and resending, or just resending, is one action either way.
- **Continue after it** — the message and the reply it got are both carried over. Use this
  when you want to head in a different direction from a point that went well.

The confirmation dialog shows the branch point, how many exchanges are carried over, and
that the original stays.

**Don't confuse it with handoff.** A handoff *summarises the conversation and passes it to a
different agent*; a branch *copies the conversation as-is within the same agent*. Branch when
the exact wording and the fine details matter; hand off when you want another agent to take over.

If the button isn't there, one of these applies: you can't branch from the agent's messages,
nor from one that's still being sent. Only **claude, codex, opencode and copilot** sessions
support it, and codex and opencode only in **managed** execution (the CLI launch command has
no way to pass a branch point).

## Memo queue

The memo queue is for **capturing instructions on the spot as they occur to you, then
flushing them to a session together later** (the **Memo queue** in the left pane). You can,
say, jot down "want to fix that" items on your phone during the commute, then hand them
over in one go at your PC.

- **Capture** — write into "Add a quick memo… (send them together later)" and press "Add". You can also open a file and capture from "Send" in [05](04-files.md). Memos are grouped by repository and category.
- **Tidy up with AI** — select memos and hit **"Organize the selected memos with an assistant"**: it turns scribbles into clear instructions and suggests categories. The result is **always previewed** and nothing is applied until you approve with "Apply N item(s)" (nothing gets rewritten behind your back).
- **Send** — pick a running destination session and send in bulk with **"Send selection"**. You can also send a whole category at once ("Send this category together"). If there's no running session to send to, start one first.

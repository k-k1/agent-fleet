---
audience: "anyone using the assistant chat or the memo queue"
---

# 10. Chat and memos — quick questions, translation, and memo capture

English | [日本語](10-chat-memo.ja.md)

## What the assistant chat is

The **chat** you start from **Assistants** in the left pane is a different thing from a work
session. The name resembles the "chat view" used with a session's Managed execution, but
this one is a **repository-free conversation** with no working directory or persistent
execution state. It's a good fit for translating or summarizing Markdown documents and
answering small questions.

Here's how to think about **choosing between it and a session**.

- **Chat**: short-to-medium questions, translations, summaries. Things like "translate this text into Japanese" or "what does this function do?", where an answer on the spot is all you need.
- **Session**: work that actually reads and writes files, code changes, bulk processing of large files. If file output is involved, go to a session (you can hand over files with "Send" in [07 Files](07-files.md)).

Start a conversation with **+ (New chat)** in the **Assistants** section. Send with Ctrl+Enter
(you can switch to Enter-to-send in settings). The other side's messages are labeled
"Assistant", yours "You", and "Thinking…" appears while it's working. You can also
**paste an image** into the input field and send it along (for example, showing a
screenshot and asking about it). Its thumbnail, both before you send and in the sent message,
opens enlarged in the same lightbox the gallery uses.

If the chat runs on claude and the workspace's Claude login has expired, the failed reply turns
into a short notice with **Re-authenticate** and **Retry**; see
[21 Troubleshooting](21-troubleshooting.md#an-agent-stopped-with-your-login-has-expired).

### Purpose-built assistants and translation

Assistants with specific roles are provided, such as guidance on using Agent Fleet or SRE
consultation. Type ordinary questions straight into a new chat; for translating or
summarizing files, use a file's right-click menu.

You can also set up your own purpose-built assistant with **"Create assistant"**. You
configure a name, a description (the greeting when a conversation starts), a persona
(instructions on role and tone), the agent / model to use, and **tool permissions**.

If you leave the model blank, new conversations use the model set for that agent under ⚙ Settings →
Assistant → **Assistant models** ([20](20-settings.md)); when that is "Recommended", each agent's own
default rule picks a fast, low-cost model, usually the newest Luna for codex, and for opencode GLM
when your account lists it, otherwise Nemotron. Models you excluded are skipped, and when nothing
qualifies the CLI's own default is used (details in [09](09-agents.md)). If you specify
a model here, that choice wins, and the model of a conversation you've already started
doesn't change afterwards.

Each reply carries the model that produced it next to the agent name (e.g. `sonnet 5`), the
same way a session's mirror labels its turns. It is recorded per reply, so earlier answers
keep the model they were actually written with even if the conversation later switches model
or falls back to another agent. When a reply shows no model, the CLI ran on its own default
and does not tell us which model that was (cursor's Auto, for instance); we leave it blank
rather than print a guess.

- **None**: answers within the chat alone, with no external tools. This is plenty for translation and summarization.
- **AF read**: can read your workspace's session list, statuses, and output, plus agent usage / limits (claude / codex / muse usage rates and reset times, agy's quota) and each session's context size and cumulative token spend (no writing).
- **AF write**: in addition to reading, can send prompts to sessions (doing work on your behalf). Grant this only for trusted uses.

The built-in **Fleet Operator** is the main example of "AF write": from chat it can
direct everything from launching sessions to giving instructions and receiving completion
reports. See [11 Running many sessions](11-many-sessions.md) for details.

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
  they appear). You also can't switch while a reply is being generated; stop it first.
- You can switch back. That agent resumes from where it last answered and only receives the
  part of the conversation it missed.
- When a turn fails on a usage or rate limit before anything was shown, it is re-run once on the
  next usable CLI in the priority list and the chat shows a notice; the conversation's agent does
  not change, and it goes back once the limit lifts. A limit about to lift does not switch (the turn
  ends with its error), a turn that had already started showing its answer is not re-run, and a
  compaction that hits a limit is not retried elsewhere; one started while the conversation's CLI is
  known to be limited runs on the next CLI (see chapter 20).

The chat's **reply language** is set with "Reply language" on ⚙Settings → the
"Assistant" tab. "Match input" replies in the language of the text you hand over, while
choosing 日本語 / English answers in that language even for text in other languages
(the file right-click "Translate with assistant" is exempt: it always translates according to the source text).

### When a conversation gets long (a context rule of thumb)

The more exchanges you pile up, the more context keeps flowing to the model. Once the
first response comes back, a **context bar** (usage / limit · %), the same one as a
session's mirror, appears under the chat header. When usage passes **80%** the bar
turns a warning color, and a one-time notice arrives in the conversation (it also shows
up in the notification center).

There are two ways to deal with it.

- **Compact (keep going as is)**: press **"Compact"** at the right end of the context
  bar to summarize the conversation so far and **hand only the summary to a fresh
  session**, continuing in the same chat. The on-screen history stays intact; only the
  summary carries over (producing the summary costs one turn's worth of tokens).
- **Open a new chat**: if you're at a natural break, opening a new chat and writing out
  just the key points yourself is the surest way.

Either way, the longer you push on with a bloated conversation, the more you invite
degraded response quality, failed turns, and ballooning token spend.

There's also a safety net if you leave it alone. If usage is still above **90%** when
you start the next exchange, it auto-compacts first (summary handoff) before responding
(can be turned OFF with "Auto-compact chat context" on ⚙Settings → the "Assistant" tab).
Even if the context blows past the limit before compacting, the system
automatically attempts a summary handoff and then retries the reply. If it was exceeded
too far to recover, a notice to that effect appears in the conversation (and the
notification center), so deal with it via "Compact" or a new chat.

### Put what must not be forgotten in the work plan

There is one slot that is **carried forward verbatim, never summarised**, through compaction
and into every new session: the **work plan**, next to the context bar. Write down the
constraints (assumptions that hold from here on), the givens (facts the next move depends on)
and what comes next (order, dependencies), and a long conversation stops drifting off its base.

- **"Edit"** writes it directly. **"Refresh"** re-derives it from the recent conversation: press
  it right after a discussion changes the direction.
- On compaction it is updated automatically to match the recent conversation.
- **"Clear"** empties it (the conversation history and the handoff summary are untouched).
- It is not a list of finished work. It works when it holds only what you would get wrong
  without.

## Memo queue

The memo queue is for **capturing instructions on the spot as they occur to you, then
flushing them to a session together later** (the **Memo queue** in the left pane). You can,
say, jot down "want to fix that" items on your phone during the commute, then hand them
over in one go at your PC.

- **Capture**: write into "Add a quick memo… (send them together later)" and press "Add". You can also open a file and capture from "Send" in [07](07-files.md). Memos are grouped by repository and category.
- **Capture straight into a category**: the **+** at the right of a category heading opens the input under that heading, and what you write lands in that category (and repository). No need to type the category name again.
- **Read a long memo, then close it again**: a memo body is clamped to two lines; click it to open the full text (a long one stops at half the pane and scrolls inside the memo from there). **Collapse**, under the body or in the row's **⋯** menu, puts it back to two lines. Clicking the body again while it is open starts editing. The same **⋯** menu also has **Copy**, which puts the memo's text on the clipboard as written.
- **Tidy up with AI**: select memos and hit **"Organize the selected memos with an assistant"**; it turns scribbles into clear instructions and suggests categories. The result is **always previewed** and nothing is applied until you approve with "Apply N item(s)".
- **Send**: pick a running destination session and send in bulk with **"Send selection"**. You can also send a whole category at once ("Send this category together"). If there's no running session to send to, start one first. Sending several memos together adds the category headings (`## …`) and the `1.` numbering; **a single memo goes out as its bare text**. Either way you can edit the message before it leaves.
- **Memos taken in from the inbox** carry an **"Inbox: …"** chip and are always sent on their own,
  one message each, with where they came from; a running session has to take them
  ([12](12-collaboration.md#asking-a-teammate-the-inbox)).

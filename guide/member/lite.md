---
audience: "someone using Agent Fleet without touching the terminal"
---

# Simple guide (for people who don't use the terminal)

English | [日本語](lite.ja.md)

The entry point of this volume (what to read first, and the one rule to keep) has moved to
[Start here if you don't use the terminal](../user/start/without-terminal.md). What you can do is in
[What you can do with Agent Fleet](../user/start/overview.md), the screen in [Find your way around the screen](../user/start/console-tour.md),
and the first-time setup in [Start your first session](../user/start/quickstart.md). The chapters below have not moved yet.

---

## 4. Using chat (questions, translation, text cleanup)

From "**Assistants**" on the left, choose "**+ New chat**" and a conversation with the AI begins.
Just write in the input field at the bottom and send. It's good at requests like "translate
this", "summarize this", "make this text easier to read".

- By default you send with **Ctrl+Enter (⌘+Enter on a Mac)**; Enter inserts a newline. If you
  would rather send with Enter alone, switch the **send key** in ⚙ Settings → Keys. You can also
  paste in an image and ask "what is this?".
- While the conversation is still empty, it says "**Send a message to start the conversation.**"
  The very first time you open it, it says "**No chats yet. Start one from +.**"
- Even with no configuration at all, several **assistants (AI personalities/roles)** come ready
  out of the box. For example, the **Agent Fleet Assistant** (guidance on using the product) and
  the **SRE Assistant** (a sounding board for monitoring and incident response). Pick the one
  that fits when you press "+ New chat". If all you need is translation or summarization, the
  easiest way is to right-click a file in the file list on the left and choose
  **"Translate with assistant" or "Summarize with assistant"**
  (a chat opens with that file already handed over).

### How is chat different from a session?

A common question. Roughly speaking:

- **Chat (assistants)** = for repository-free Q&A and translation. Your help desk: ask casually,
  as often as you like. It only answers within the conversation; it doesn't create files or take on big jobs.
- **Session** = where developers have the AI do serious work (writing code, fixing lots of files).

So it's enough to remember: "short questions, translation, text cleanup" go to chat, and "big
work that rewrites files" goes to a session. **Big jobs that produce file output**, like
translating a long document and saving it as a separate file, are better handed to a session
(i.e. the dev team) rather than chat. Chat runs out of steam when it takes on too much work at
once, which is why the roles are kept separate.

---

## 5. Reading documents and slides

Documents written by the dev team open from **Files (the repository tree)** on the left. Just
walk down the folders and click a file name.

- **Markdown (.md) documents** are shown as a nicely formatted **preview**. The
  "**Preview / Source**" toggle at the top also lets you see the original formatted text.
  Diagrams (Mermaid) are rendered as pictures too.
- **Marp slide decks** can be read as slides, flipping through one page at a time. A
  "**Slides / Preview / Source**" toggle appears at the top, with the slide view shown first.
  Flip pages with "◀ ▶" or the arrow keys, and use the "**Fullscreen**" button to show them
  large (works for presentations too).

You're only reading, so there's no worry about accidentally breaking the contents. External
links open in a new tab.

The screen showing an open file has a "**Send**" button. From there you can **hand the document
to a chat assistant** and ask "translate this" or "summarize this", send it to a development
session, or add it to the memo queue covered in the next chapter. When you're thinking "I'd
like this English document translated", the easy route is: open it, press "Send", and pick
**Assistants (open in chat)** as the destination.

---

## 6. The memo queue (collect ideas and hand them over)

Even when you think "I'd like this fixed" or "I want a feature like this", interrupting the team
every single time feels awkward. The **memo queue** is the place to **pile up those quick notes
and hand them to development later, all at once**.

- Write whatever comes to mind in the input field of the "**Memo queue**" on the left
  ("**Add a quick memo… (send them together later)**") and press "**Add**". If you like, group
  memos with "**Category (optional)**" (e.g. frontend, api). When it's empty it says
  "**No memos yet.**"
- The "**Send**" button on an open file can also "**Add to queue**" a request about that
  document (see the previous chapter).
- Piled-up memos can stay rough; that's fine. Use "**Organize the selected memos with an
  assistant**" at the top and the AI will polish the wording and even suggest a categorization.
  You review the result in a preview (the "**Tidy with assistant**" screen), and nothing is
  rewritten until you press "**Apply**".
- To hand them over, tick the checkboxes and press "**Send selection**", or press "**Send**" on
  a category header (= send this category together), then choose the destination session. The
  selected memos arrive **combined into a single message**.

### "Who receives my memos?"

Memos don't automatically fly off into someone's email. When you perform the send operation,
they go to **the running session you chose** (that development AI's work). In other words, you
choose the destination. If there is no session you can send to, it says "**No running session
to send to.**" In that case, ask the developer in charge to get a session running, then send.
Sent memos aren't deleted; they remain marked "**Sent**" for a while, so you can look back at
them later. You can also just keep collecting and send them another day.

---

## 7. Watching development progress

The **Sessions** list on the left shows what state each piece of development AI work is in. How to read the state marks
and follow a conversation is in [Read a session's state](../user/sessions/status.md).

### Reading work that was shared with you

When a developer **shares** something with you ("keep an eye on how this goes"), it appears under
**Shared sessions** on the left. Click it and you can read that conversation **read-only**. It
opens independently of your own workspace, so you don't need to have any repository.

- Only with the **may-propose** permission can you **propose** something to send from the field
  below. A proposal reaches the AI **only after the owner (the developer who shared it) approves
  it**. Nothing starts moving on its own.
- **While the owner's workspace is stopped**, the history can't be read. It says so; ask the
  developer to start it.
- If the list looks out of date, press **"Refresh"**.

### An important ground rule

The rule not to answer questions meant for someone else has moved to [Start here if you don't use the terminal](../user/start/without-terminal.md#dont-answer-questions-meant-for-someone-else).

---

## 8. When you're stuck + FAQ

### First moves when something goes wrong

1. **If items disappeared from the left**, check that the button at the very top of the left pane
   still says **"All"**. When it shows another name (a working set), only that group is listed;
   nothing was deleted.
2. Try **reloading** the page (browser refresh).
3. If you haven't pressed "Start workspace", most features won't work. Check that it's started.
4. If it still doesn't work, don't push through; **ask your administrator**. Login, connections,
   and permissions are the administrator's territory.

### FAQ

**Q. Can I use it without touching the black screen (terminal)?**
Yes. Everything in this volume (chat, memo queue, file browsing, progress watching) is done
entirely with buttons and input fields. You never type a command or learn git.

**Q. How is chat different from a session?**
Chat is a repository-free help desk for questions and translation; a session is where developers
have the AI do serious work. Remember it as: short questions → chat; big work that rewrites
files → session (i.e. the dev team). See [Chapter 4](#4-using-chat-questions-translation-text-cleanup) for details.

**Q. Who receives my memos?**
There's no automatic delivery. When you perform the send operation, they go to the running
session you chose. You get to pick the destination. See [Chapter 6](#6-the-memo-queue-collect-ideas-and-hand-them-over).

**Q. If I press the wrong button, will something break?**
Browsing and chatting can't damage anything. Memo organizing doesn't rewrite anything until
you press "Apply". If in doubt, close with "Later" or ✕ and you're back where you started.

**Q. Does it work on a smartphone?**
For viewing and light operations, yes. Open the left-side list from the three-line menu at the
top. For focused work, a computer is more comfortable.

**Q. Do I need "connect git" or "create a session"?**
Not if you only chat and browse. Setup requires just two things: "Start workspace" and
"Connect an agent". See [Start your first session](../user/start/quickstart.md).

**Q. What if I want to do more?**
When you feel like editing files yourself or having the AI write code, the developer-facing
[member/README.md](README.md) is your next step. If you want to understand the machinery
itself, head to the developer documentation.

# Easy Guide (for people who don't use the terminal)

English | [日本語](lite.ja.md)

Audience: someone using Agent Fleet without touching the terminal
Source of truth: the Console itself — if a screen disagrees with this page, the screen is right
Updated: 2026-08

> Who this guide is for: people who don't do development themselves, but want to ask the AI
> questions or for translations, peek at how development is going, read the documents and slides
> inside repositories, or pass ideas and requests along to the development team. Product managers,
> planners, documentation owners, and so on.

**First, relax: with this way of working, you will never touch the black screen (the terminal).**

Agent Fleet has a text-only black screen called the "terminal". Developers type commands there
to make the AI write code. But that is not where you'll be working. All you touch are the
**buttons, checkboxes, and input fields** in your browser. You don't need to memorize commands,
and you don't need to know what the word "git" means. This volume explains only the ways of
using Agent Fleet that never require opening that black screen.

This guide describes the "how" of the operations. It doesn't get into the "why it works that way"
mechanics (if you're curious, that is in the developer documentation).
Guides for other roles and the glossary are in the index [README.md](README.md). If you ever need
to go a step further (write code yourself, edit files, and so on), move on to the developer-facing
[member/README.md](README.md).

---

## 1. What you can do with Agent Fleet

Agent Fleet is a service that lets everyone in your organization use AI (such as Claude) from
the browser. It has plenty of advanced features, but **these four are all you need to learn**.

| What you want to do | Where to do it | In a nutshell |
|--------------|----------|----------------|
| Ask the AI questions, get translations, polish text | **Chat (assistants)** | A help desk that needs no repository. Ask casually, as often as you like |
| Collect ideas and requests, then hand them to the dev team | **Memo queue** | Pile up quick notes, then send them together |
| Read documents and slides | **File list** | Browse repository contents in your browser |
| See how far development has progressed | **Session list** | Glance at the status badges on the AI's work |

To repeat: **the terminal is not on this list**. All four work with nothing but buttons and
mouse clicks. If you get stuck, you can always close things with the "Later" button or the ✕,
so don't be afraid to click around.

---

## 2. Logging in and reading the screen

Open the URL your company gave you and a login screen appears. Sign in with the account you
normally use (your company's login method). IT set this up, so if you're lost, just ask your
administrator for the URL and login method and you'll be fine.

After logging in you'll see a screen (the Console) divided into several areas. You don't need
to understand all of it. **All you need to remember is the following list on the left side.**

- **Assistants** — the list of AI chats and "+ New chat". This is your entrance for questions and translations.
- **Memo queue** — the list of request memos you've collected. It shows a count badge.
- **Files (the repository tree)** — where the dev team's documents and slides live.
- **Sessions** — the list of development AI work. Status badges (colored marks) show progress at a glance.
- **Shared sessions** — work a developer has shown you. It doesn't appear when nothing is shared (chapter 7).

It also works on a narrow smartphone screen. In that case the left-side list starts out
collapsed; press the three-line (menu) button near the top to open it. It closes automatically
when you pick an item, so you can operate it with one finger.

---

## 3. One-time setup

The first time you open it, a guidance card appears saying "**Welcome to Agent Fleet /
Two steps first, then just pick your goal**". It's a to-do list where items get checked off in
order. There are only two things to do.

1. **Start workspace** — brings up your own private work area. Just press the "Start" button
   and wait for the "Starting…" indicator to settle. This one is a must.
2. **Connect an agent** — sign in to the AI (such as Claude). Chat needs this, so press
   "Connect" and follow the on-screen instructions to sign in.

Next, two cards appear asking "**Where do you want to start?**". You should press
**"Start chatting" under "Ask AI a question or for a translation"**. That alone opens a chat
and your setup is done. The other card, "Develop in a repository", is for developers, so you
can leave it alone (the git connection and session creation steps are shown only to people
who choose it).

You can close the card with "Later" at the bottom right, and completed items get checked off
automatically. If you want to see it again after closing it, you can always reopen it from
"**Getting-started guide**" in the menu under your name at the top right. You can stop the
workspace when you're done with it; press "Start" again next time and it comes back.

**When to ask your administrator** — don't struggle on your own in these cases: you don't know
the login method or the URL / signing in at "Connect an agent" isn't working / you're not sure
what your account can use in the first place. These are configuration and permission matters,
so asking your administrator is the fastest and surest route.

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

- **Chat (assistants)** = for repository-free Q&A and translation. Your help desk — ask casually,
  as often as you like. It only answers within the conversation; it doesn't create files or take on big jobs.
- **Session** = where developers have the AI do serious work (writing code, fixing lots of files).

So it's enough to remember: "short questions, translation, text cleanup" go to chat, and "big
work that rewrites files" goes to a session. **Big jobs that produce file output** — like
translating a long document and saving it as a separate file — are better handed to a session
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
- Piled-up memos can stay rough — that's fine. Use "**Organize the selected memos with an
  assistant**" at the top and the AI will polish the wording and even suggest a categorization.
  You review the result in a preview (the "**Tidy with assistant**" screen), and nothing is
  rewritten until you press "**Apply**" (it never overwrites on its own).
- To hand them over, tick the checkboxes and press "**Send selection**", or press "**Send**" on
  a category header (= send this category together), then choose the destination session. The
  selected memos arrive **combined into a single message**.

### "Who receives my memos?"

Memos don't automatically fly off into someone's email. When you perform the send operation,
they go to **the running session you chose — that development AI's work**. In other words, you
choose the destination. If there is no session you can send to, it says "**No running session
to send to.**" — in that case, ask the developer in charge to get a session running, then send.
Sent memos aren't deleted; they remain marked "**Sent**" for a while, so you can look back at
them later. You can also just keep collecting and send them another day.

---

## 7. Watching development progress

The **Sessions** list on the left shows all the development AI work in a row. The colored mark
on each row (the status badge) tells you at a glance what state it's in. Read them like this:

| What you see | Meaning | How to take it |
|------------|------|--------------------|
| **Working…** (a spinning mark) | The AI is working right this moment | It's moving. Just wait |
| **Ready** (a check mark) | Reached a stopping point, awaiting the next instruction | Its hands are free (awaiting input) |
| **Question / Awaiting permission / Plan ready** | The AI is waiting for a human reply | The developer in charge needs to respond (see the ground rule below) |
| **Stopped** | Not running (can be resumed later) | Just paused for now |
| **Running** | A shell or the like is up | Active |

Open a row and a "**Chat / Terminal**" toggle appears at the top. **Choose the "Chat" side**
to follow that work's **conversation** in a readable form (formatted turn by turn). "Terminal"
is the black screen for developers, so looking at "Chat" alone is enough for you. While you're
watching, it shows "**Viewing history (resume to type)**" — you're essentially in read-only mode.

### Reading work that was shared with you

When a developer **shares** something with you ("keep an eye on how this goes"), it appears under
**Shared sessions** on the left. Click it and you can read that conversation **read-only**. It
opens independently of your own workspace, so you don't need to have any repository.

- Only with the **may-propose** permission can you **propose** something to send from the field
  below. A proposal reaches the AI **only after the owner (the developer who shared it) approves
  it** — nothing starts moving on its own.
- **While the owner's workspace is stopped**, the history can't be read. It says so; ask the
  developer to start it.
- If the list looks out of date, press **"Refresh"**.

### An important ground rule

When the AI is **waiting for someone's reply** — as in "Question" or "Awaiting permission" —
please don't answer in their place just because you happen to be watching. That question is
addressed to the developer in charge. Stick to **watching** the progress, and if you notice
something, speak to the person in charge or write it into the memo queue. Replying on your own
can send the work off in a direction the developer never intended.

---

## 8. When you're stuck + FAQ

### First moves when something goes wrong

1. **If items disappeared from the left**, check that the button at the very top of the left pane
   still says **"All"**. When it shows another name (a working set), only that group is listed —
   nothing was deleted.
2. Try **reloading** the page (browser refresh).
3. If you haven't pressed "Start workspace", most features won't work. Check that it's started.
4. If it still doesn't work, don't push through — **ask your administrator**. Login, connections,
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
"Connect an agent". See [Chapter 3](#3-one-time-setup).

**Q. What if I want to do more?**
When you feel like editing files yourself or having the AI write code, the developer-facing
[member/README.md](README.md) is your next step. If you want to understand the machinery
itself, head to the developer documentation.

---
audience: "someone using Agent Fleet without touching the terminal"
---

# Simple guide (for people who don't use the terminal)

English | [日本語](lite.ja.md)

**First, relax: with this way of working, you will never touch the black screen (the terminal).**

Agent Fleet has a text-only black screen called the "terminal". Developers type commands there
to make the AI write code. But that is not where you'll be working. All you touch are the
**buttons, checkboxes, and input fields** in your browser. You don't need to memorize commands,
and you don't need to know what the word "git" means. This volume explains only the ways of
using Agent Fleet that never require opening that black screen.

This guide describes the "how" of the operations. It doesn't get into the "why it works that way"
mechanics. See the [guide's index](../README.md) for guides for other roles, and the
[glossary](../ref/glossary.md). If you ever need to go a step further (write code yourself, edit
files, and so on), move on to the full [user guide](README.md).

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

- **Assistants**: the list of AI chats and "+ New chat". This is your entrance for questions and translations.
- **Memo queue**: the list of request memos you've collected. It shows a count badge.
- **Files (the repository tree)**: where the dev team's documents and slides live.
- **Sessions**: the list of development AI work. Status badges (colored marks) show progress at a glance.
- **Shared sessions**: work a developer has shown you. It doesn't appear when nothing is shared (chapter 7).

It also works on a narrow smartphone screen. In that case the left-side list starts out
collapsed; press the three-line (menu) button near the top to open it. It closes automatically
when you pick an item, so you can operate it with one finger.

---

## 3. One-time setup

After you sign in, the main area shows **Home**. The first time, a guidance card appears at
the top of it saying "**Welcome to Agent Fleet / Two steps first, then just pick your goal**". It's a to-do list where items get checked off in
order. There are only two things to do.

1. **Start workspace**: brings up your own private work area. Just press the "Start" button
   and wait for the "Starting…" indicator to settle. This one is a must.
2. **Connect an agent**: sign in to the AI (such as Claude). Chat needs this, so press
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

**When to ask your administrator.** Don't struggle on your own in these cases: you don't know
the login method or the URL / signing in at "Connect an agent" isn't working / you're not sure
what your account can use in the first place. These are configuration and permission matters,
so asking your administrator is the fastest and surest route.

---

## 4. Using chat (questions, translation, text cleanup)

From **Assistant** on the left you can ask the AI questions and have it translate or summarize. How to start and when to use a session instead is in [Ask an assistant](../user/chat/assistant.md); translating files and purpose-built assistants are in [Use translation and purpose-built assistants](../user/chat/purpose-assistants.md).

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

Collect requests as they come in the **Memo queue** on the left, and hand them to a session later. How it works, and who receives your memos, is in [Collect memos and hand them over](../user/chat/memos.md).

---

## 7. Watching development progress

The **Sessions** list on the left shows what state each piece of development AI work is in. How to read the state marks
and follow a conversation is in [Read a session's state](../user/sessions/status.md).

A conversation a developer shared with you is under **Shared sessions** on the left, read-only. How to read it is in [Share a conversation](../user/team/share.md#read-a-conversation-shared-with-you).

### An important ground rule

When the AI is **waiting for someone's reply** (as in "Question" or "Awaiting permission"),
please don't answer in their place just because you happen to be watching. That question is
addressed to the developer in charge. Stick to **watching** the progress, and if you notice
something, speak to the person in charge or write it into the memo queue. Replying on your own
can send the work off in a direction the developer never intended.

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
"Connect an agent". See [Chapter 3](#3-one-time-setup).

**Q. What if I want to do more?**
When you feel like editing files yourself or having the AI write code, the developer-facing
[member/README.md](README.md) is your next step. If you want to understand the machinery
itself, head to the developer documentation.

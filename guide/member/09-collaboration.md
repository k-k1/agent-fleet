---
audience: "anyone working with other members on the same conversation, or tidying up after themselves"
updated: "2026-08"
---

# 09. Working with others, and keeping things tidy

English | [日本語](09-collaboration.ja.md)

This chapter covers the parts of the Console that are about **more than one person**, or
about **what happens to work after it is finished**: highlighting a line so somebody
else sees it, seeing what a session actually changed, and clearing away what has piled
up without losing anything you still want.

## Highlighting a line in a conversation

Select text in a conversation and a **Highlight** pill appears. Pick a colour — yellow,
green, blue or pink — and the passage stays marked.

- The marks are part of the conversation, not of your browser. Anyone the session is
  shared with sees the same passage marked, in the same place, and the **Highlights**
  strip under the conversation header lists them with the number of people who added
  them.
- **Remove highlight** takes yours away. Removing one does not touch anybody else's.
- Colour carries no meaning the product enforces — it means whatever your team decides.

Use it when a conversation is long and the interesting part is three screens up: a
highlight survives, where "look at the bit about the retry" does not.

## Seeing what a session changed

The **Changed files** strip under the conversation header lists the files that session
actually touched, and takes you straight to one. Click its heading to fold it open or
closed; the choice is remembered per session. **Ctrl+P** (**⌘P** on macOS), then **Tab**
to "Changed files", reaches the same list.

It answers the question you have at review time — "what did this thing do to my
working copy?" — without reading the whole conversation back. It is derived from the
conversation and the repository together, so it reflects what happened, not what the
agent said it would do.

## Reply suggestions

Under the message box, **Suggest replies with AI (from the recent conversation)** offers
a few things you might plausibly want to send next. They are suggestions, not actions —
nothing is sent until you choose one.

- **Always show (pin)** keeps a suggestion around.
- **Stop suggesting this** removes one; sending the same text yourself brings it back.
- If the conversation is still short, or no AI is available for it, you are told so
  rather than shown an empty box.

## Sharing and handing over

Both live in the session's ⋯ menu, and they are different things:

| | What it does | The other person |
|---|---|---|
| **Share…** | shows your conversation to another member of the same tenant, read-only | reads it; may be allowed to propose |
| **Hand off to another agent…** | starts a new session carrying the context | continues the work themselves |

A shared session appears in the other person's **Shared sessions** section and carries a
**Shared** badge on yours, so you can always see what is visible to someone else.
Sharing does not give access to your workspace, only to the conversation.

## Keeping a session awake

Idle workspaces stop on their own. If you have something long running that must not be
interrupted, **Keep awake** in the ⋯ menu exempts the session from idle-stop for a set
number of hours, and the row shows how much of that is left.

It is deliberately time-boxed. An exemption you set once and forget is how a workspace
ends up running for a month.

## Locking something against deletion

**Lock against deletion** in the ⋯ menu marks a session as one that must not go away.
A locked session carries a badge saying so, and is **skipped by cleanup and by
automatic tidying** — both the manual sweep below and anything that would have removed
it on its own.

Use it for the conversation you will need at the end of the quarter, and for the
worktree you have not finished with. Cleanup refuses to remove a worktree that still
holds a locked session, and tells you to unlock it first rather than quietly working
around you.

## Cleaning up

**Clean up** surveys the stopped sessions, unneeded worktrees and merged branches that
have accumulated, and lets you clear them in one pass.

Every candidate is graded, and the grade is the point:

| Grade | Means |
|---|---|
| **Safe** | nothing is lost by removing it |
| **Review** | probably fine, but look first |
| **Keep** | it is running, or it has uncommitted or unpushed work. Stop it or push it first |

**Select all safe** does the boring majority in one click. What you remove is not
destroyed: sessions and branches go to **Trash (restore)**, the second tab of the same
dialog, and can be brought back.

Archived sessions are a separate thing from the trash and are not part of cleanup —
they have their own browser.

## When a turn is interrupted

An agent's turn can be cut off by something outside the conversation — a provider
error, a dropped connection, a usage limit. When that happens the conversation is not
silently left half-finished:

- You are notified, including when a run resumes after a usage limit resets.
- **Auto-resume interrupted turns** (in the assistant's settings) lets the assistant
  restart the turns that a retry would fix, rather than leaving them for you to notice.

Only interruptions that a resend actually repairs are resumed. A turn that failed
because of what it was asked to do is left alone, because retrying it would just fail
again.

---

Related: [02 Sessions](02-sessions.md) for launching and resuming ·
[ref/features.md](../ref/features.md) for the catalogue ·
[ref/agents.md](../ref/agents.md) for which agents support what

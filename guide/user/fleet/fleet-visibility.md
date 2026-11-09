---
audience: "anyone who wants a session to act with the other sessions in mind"
source_of_truth: "The af tools every session gets (session state, context usage, the memo queue)"
---

# Let a session see the fleet

English | [日本語](fleet-visibility.ja.md)

Every session can see the workspace it is part of: whether other sessions are running, and how full its own context is, without asking you.
It can also leave you a note about something it noticed.

## How it works

| What you do | What the agent does |
|---|---|
| (nothing to set) | Checks other sessions' state and its own context usage |
| Read the notes that pile up, and decide when and where to send them | Leaves a note about something it noticed outside what it was asked to do |
| Answer a session stopped on a question or an approval | Tells you when it finds a session stopped like that |

[Let sessions message each other](messages.md) is for telling; this is for seeing.

## What a session can see

Every session has this from the start; there is no setting to turn on.

| What it sees | What it is for |
|---|---|
| Other sessions' state | Checking whether the session it handed work to is working, idle or stopped |
| Its own context usage | Deciding to hand the rest over to a new session before the context runs out ([Hand off to another session or agent](../sessions/handoff.md#hand-off)) |
| The memo queue | Seeing what is waiting there |

## Have it leave notes

A session can add one memo to the memo queue: "there is another bug next to the function I fixed", "this runbook is out of date". It **leaves the report without stopping its work**.

- When it finds a memo it left was wrong or incomplete, it corrects that memo rather than adding a second one
- It cannot delete memos
- When and to which session the memos go is still your decision ([10 Chat and memos](../../member/10-chat-memo.md))

## What it cannot see or do

- **It sees that another session is stopped on a question or a plan approval, but not the text.** Reading it would give a session material to answer, on your behalf, a question meant for you. A session that finds one stopped tells you, and goes no further
- **It cannot drive other sessions.** Sending instructions, answering questions, approving plans, starting, stopping or deleting sessions and sending memos to sessions belong to the [Fleet Operator](operator.md) and the Console
- The exceptions are what you open in Settings
  - "Messages between sessions": send short messages and read other sessions' output ([Let sessions message each other](messages.md))
  - "Starting sessions from sessions": start children and look after them ([Start child sessions from a session](child-sessions.md))

## Related

- [Let sessions message each other](messages.md)
- [Hand work to the Fleet Operator](operator.md)
- [10 Chat and memos](../../member/10-chat-memo.md)

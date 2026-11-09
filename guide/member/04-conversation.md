---
audience: "anyone talking to a running agent"
---

# 04. Talking to a session

English | [日本語](04-conversation.ja.md)

What this chapter held has moved to one page per task.

| To do this | Page |
|---|---|
| Read the conversation, answer cards, reuse what you sent, reply suggestions, highlights | [Reply to a session](../user/sessions/reply.md) |
| Hold follow-ups during a turn, stop a turn when something is queued | [Hold what you send, stop a turn](../user/sessions/queue.md) |
| Comment on a plan, have another session review it | [Comment on a plan, have it reviewed](../user/sessions/plans.md) |
| Call a skill or a command | [Call a skill or a command](../user/sessions/skills.md) |
| Branch from a past message | [Branch from a past message](../user/sessions/branch-message.md) |
| See the files a session touched | [See what a session changed](../user/sessions/changes.md) |
| Save a conversation as Markdown | [Save a conversation as Markdown](../user/sessions/export.md) |
| Read an answer in your own language | [Read an answer in your own language](../user/sessions/translate.md) |

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

Related: [03 Starting sessions](03-starting-sessions.md) for launching ·
[05 Stopping and tidying up](05-stopping-and-tidying.md) for resuming ·
[ref/agents.md](../ref/agents.md) for which agents support what

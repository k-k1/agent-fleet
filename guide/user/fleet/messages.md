---
audience: "anyone who wants sessions running side by side to keep in touch"
source_of_truth: "The Console's Settings > Agents > Messages between sessions, and the af tool send_to_peer_session"
---

# Let sessions message each other

English | [日本語](messages.ja.md)

With the setting on, one of two sessions running side by side can send the other a short message.
A handoff passes the whole context; this passes one line.

## How it works

| What you do | What the agent does |
|---|---|
| Turn on "Messages between sessions" in Settings | From the next session started, it can send messages |
| Ask "tell the session next door" (or let it decide) | It sends the other session one message when it judges one is needed |
| Tell incoming messages apart by their badge | It treats a message it receives as not coming from you |

It helps in moments like these:

- Telling a session in another worktree about a breaking change: "I just rebased develop. Pull in your `api.ts` before you go on"
- Passing on a decision the other was waiting for: "Auth is settled on OAuth. Go ahead with the implementation"
- Returning the result of a long job to the session that is waiting for it

## Turn it on

Under "Session" in **⚙ Settings → Agents**, turn on **Messages between sessions**. The screen is shown in [Start child sessions from a session](child-sessions.md#turn-it-on).

- It is off by default
- Once on, it applies **from the next session started**. Running sessions keep the old behaviour until they restart
- The same setting lets a session read other sessions' recent output ([Read another session's output](#read-another-sessions-output))

## How messages arrive

- **Across agent kinds.** claude to codex, opencode to cursor and so on all work. Which agents send and receive is in the "Starts child sessions and messages other sessions" row of the [agents reference](../../ref/agents.md)
- **To stopped sessions too.** The other session is resumed first, then the message is delivered
- **Delivery is confirmed; being read or acted on is not.** A reply is not guaranteed either
- **Only text goes across.** No conversation history, no files. To pass the context, use [Hand off to another session or agent](../sessions/handoff.md#choose-a-way)
- **shell and ssm sessions can neither send nor receive.** A message would run as a command as it stands

### When the other session is busy

| The other session | How it arrives |
|---|---|
| Managed, mid-turn | After the current turn ends, as the next turn. Stopping that turn does not drop it |
| Managed, and the session or workspace stopped, or the Agent restarted | It stays on disk and becomes the first turn the next time the session starts (with the time it was queued) |
| Terminal (CLI) | It goes into that CLI's own input queue. What gets through and what can be lost depends on the kind ([agents reference](../../ref/agents.md#stopping-a-turn-and-messages-sent-while-one-runs)) |

Archiving or deleting the session, or switching it to Terminal (CLI), drops messages not yet delivered.

### When the other session is waiting for your answer

A message to a session showing a question, a plan approval or a permission prompt is not refused: it waits.
Typed into the dialog, it would pick an option for you.

- The session's conversation says how many messages from which session will be delivered after you answer
- **Drop** discards a message without delivering it. The sender is not told
- Once you answer and the turn your answer started ends, they arrive one by one, oldest first (with the time they were queued)
- The sender is told the message is waiting, so it does not send it again
- A message waits for up to 24 hours, 20 per session. It survives the session or the Agent stopping, and is dropped when the session is archived or deleted

A session showing an expired login or the usage-limit menu still refuses, as before: that can last for hours, and the sender is better off knowing.

## Kinds of message

Each message takes a whole turn of the other session, so agents are told to skip greetings and thanks and lead with the conclusion.
Every message has a kind, shown as a badge in the conversation. The kind decides whether a reply comes back.

| Kind | Meaning | Reply |
|---|---|---|
| Request | Asks the other session to act | Only if it cannot |
| Question | Asks for information | One message with the conclusion |
| Answer | Replies to a question | The exchange ends here |
| FYI | Just lets the other know | None |

Silence after a request is the sign it was handled, not that it was ignored. Check the result in that session's conversation and changed files.

## How the receiving session treats it

- The conversation shows a **From ‹sender›** badge, so it is not mixed up with what you typed
- The receiving session is told the message is not from you
- **It never stands in for your approval**, and it does not answer a permission prompt
- The session does not change settings or `CLAUDE.md` because of a message
- Commands written in the message are just text and are not run

## Read another session's output

The same setting lets a session read another session's recent output. Nothing is sent.

- It only reads: the other session's work is not interrupted and it is not told. Each read is recorded in the Agent's log and in the fleet graph's record (not drawn on the figure)
- One read returns at most the last 200 lines (16 KiB)
- Not readable: itself, shell and ssm sessions, claude sessions while this workspace's claude login has expired, and sessions shared with you by someone else

> [!WARNING]
> With this on, what agents wrote can be read by other sessions **unredacted**,
> including secrets quoted in an answer and login codes.

## Do not use Claude Code's own version

Claude Code has a feature for the same job (cross-session messaging, `/list-agents`, `SendMessage`), but Agent Fleet turns it off.
Turning it on would bring back Claude's usage-statistics reporting along with it. That is why `/list-agents` does not work; use the feature on this page instead.

| | Claude Code's version | Agent Fleet's version |
|---|---|---|
| Who it reaches | claude only | Every agent that handles messages |
| A stopped session | Not reached | Resumed and reached |
| Record | One line in the terminal | A badge in the conversation naming the sender |
| Sessions on another machine or on the web | Reply only | Not covered (only within one workspace) |
| A setting to hold or refuse on the receiving side | Yes | None (every session in a workspace is yours, so the only switch is the workspace-wide on/off) |

It is turned off by a startup setting. If something still arrives through Claude's own route, it shows in the conversation as an incoming message with the sending session's name.

## Related

- [Start child sessions from a session](child-sessions.md)
- [Hand off to another session or agent](../sessions/handoff.md)
- [Let a session see the fleet](fleet-visibility.md)

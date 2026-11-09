---
audience: "anyone whose agent login expired, whose model is not offered, or whose assistant stopped"
source_of_truth: "the Console's Settings > Agents and Assistant, and the model choices in the launch dialog"
---

# Fix an agent's login or a missing model

English | [日本語](agents.ja.md)

Find the cause and the fix when an agent's login has expired, the model you want is not offered, or the assistant stops answering.
How to connect each agent starts from [Choose an agent](../agents/choose.md).

## A claude session shows a login screen such as "Select login method"

The connection is usually still fine. This is not a login problem: claude's interactive screen is just redoing its first-run steps.

- **Resuming** the session, or **"Recreate (current conversation goes to the archive)"** from the ⋯ menu, almost always clears it
- Running `/login` in the terminal works too

The connection state is on the Claude card in ⚙ Settings → Agents ([Connect Claude](../agents/claude.md#connect)).

## The agent stopped, saying its login has expired

The error in the conversation offers **"Re-authenticate"**. It opens ⚙ Settings → Agents, scrolled to that agent's card.
Sign in again there, and the session can carry on from where it stopped.

- Once you have signed in again, the same error changes to **"Re-authenticated. …"** and the button goes away
- For Claude with **"Auto-resume a cut-off turn"** on, the session picks up from where it stopped by itself. The continuation carries an **"Auto-resume"** badge
- The error itself stays red: it is the record of a failure that did happen, so it is not erased
- If nothing continues after a while, send "continue" in that conversation and it resumes from the same point

## "Login expired — sign in again", and sending starts nothing

The workspace's Claude login has expired. The credentials are still on disk, so the terminal looks like an ordinary prompt, but nothing you send starts a turn.
That is why the Console shows **"Login expired — sign in again"** instead of ready, and refuses the send.

1. Press **"Re-authenticate"** on the Claude card in ⚙ Settings → Agents
2. Once you are signed in again, the session carries on without being stopped

How to sign in is in [Connect Claude](../agents/claude.md#connect), and for Codex in [Connect Codex](../agents/codex.md#connect).

- Codex, too, refuses sends after a turn died on its login, until you sign in again. The refusal toast and its **"Re-authenticate"** open that agent's card
- The launch dialog warns about the expired login as well, but still lets you launch. The first prompt is not delivered, though, and a toast with **"Re-authenticate"** says so once. Sign in again, then send the prompt again

As the expiry gets close, the card shows **"Expires in N day(s)"** (within three days). The CLI's own warning appears only in the last day and only briefly, so this card is where you can notice it in time.

## The assistant chat says "Your Claude login has expired"

The assistant chat shows the same failure as **"Your Claude login has expired. Re-authenticate, then send again."**

- **"Re-authenticate"** opens the Claude card so you can sign in again
- **"Retry"** sends the failed message again

The original error stays under "Details". The assistant chat is described in [Chat and memos](../../member/10-chat-memo.md#what-the-assistant-chat-is).

## Connecting codex goes nowhere after you approve

To connect with a ChatGPT subscription, first turn on "Enable device code authentication for Codex" under **"Settings > Security"** in ChatGPT.
While it is off, approving does not finish the connection ([Connect Codex](../agents/codex.md)).

## The model you want is not offered, or launching with it is refused

It may be in **"Models to exclude"** on that agent's card.
An excluded model disappears from the launch dialog, the default model and the list an assistant picks from, and launching with its name is refused. A schedule's model field and launches through an assistant are refused too.
How to remove it is in [Choose an agent](../agents/choose.md#exclude-models).

To use a specific Claude release (a full id such as `claude-opus-4-8`), register it under **"Additional Claude models"** on the same screen and it joins the choices.

## The launch dialog offers only "Default"

codex, opencode, copilot, cursor, kiro, agy, lcpp and muse fetch their model list when the dialog opens.
**"Loading models…"** for a while is normal. Wait for it to go, then read what appears instead.

| It says | What to do |
|---|---|
| **"Every model is excluded in settings …"** | Review **"Models to exclude"** on that agent's card |
| **"The current billing choice leaves no model to pick …"** | Review **"opencode.ai billing"** on the opencode card ([Connect OpenCode](../agents/opencode.md)) |
| **"Only the default model is available (check this agent's connection and plan)."** | The agent returned an empty list. Check the connection and plan on its card. On a GitHub Copilot Free plan this is normal: that plan offers Auto alone |
| **"Couldn't fetch the model list — the workspace may have only just started …"** | The list could not be fetched. Right after a start the Agent is not listening yet: wait half a minute and open the dialog again |

## An opencode session stays "working" and nothing comes back

Look above the conversation for **"The provider returned an error and the agent is retrying (attempt N): …"**.
opencode is retrying a provider error. The causes and fixes are in [Connect OpenCode](../agents/opencode.md#the-provider-returned-an-error-and-the-agent-is-retrying-appears).

The band appears in Managed sessions. A Terminal (CLI) session has no band: opencode's own retry output shows in the terminal.

## Starting opencode says "This opencode version is not supported"

The opencode the workspace runs is older than 2, usually one you installed or pinned yourself.
Agent Fleet runs only opencode 2 (`@opencode/cli`): remove the old one or pin a 2.x release, then start the session again ([Connect OpenCode](../agents/opencode.md)).

## The assistant stopped carrying on by itself

It reached the automatic reply limit. To stop it running away, it can only take so many turns in a row without a message from you (10 by default, 50 at most).
**Send your next message and it resumes.**

- The limit is **"Unattended auto-reply limit"** in ⚙ Settings → Assistant. It cannot be unlimited
- To keep normal completion reports from triggering automatic replies, turn on **"Quiet completion reports"** on the same screen. Only the report card and the notification arrive

How automatic replies work is in [Running many sessions](../../member/11-many-sessions.md#session-reports-and-auto-reply).

## The assistant chat says the context is over the limit and does not answer

The conversation has grown too long. Carry on in one of these ways:

- **"Compact"** at the right end of the context bar, which carries a summary forward
- Open a new chat and hand over just the essentials

Starting an exchange above 90 % compacts it automatically first. Going over the limit before that point leaves it in this state.
Lowering **"Auto-compact threshold (tokens)"** in ⚙ Settings → Assistant makes it less likely ([Chat and memos](../../member/10-chat-memo.md#when-a-conversation-gets-long-a-context-rule-of-thumb)).

## Related

- [Choose an agent](../agents/choose.md)
- [Connect Claude](../agents/claude.md)
- [Connect Codex](../agents/codex.md)
- [Connect OpenCode](../agents/opencode.md)
- [Chat and memos](../../member/10-chat-memo.md)

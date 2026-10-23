---
audience: "anyone working with other members on the same conversation, or tidying up after themselves"
updated: "2026-10"
---

# 09. Working with others, and keeping things tidy

English | [日本語](09-collaboration.ja.md)

This chapter covers the parts of the Console for **more than one person** and for
**what happens to work after it is finished**: highlighting a line so somebody
else sees it, seeing what a session actually changed, and clearing away what has piled
up without losing anything you still want.

## Highlighting a line in a conversation

Select text in a conversation and a **Highlight** pill appears. Pick a colour (yellow,
green, blue or pink) and the passage stays marked.

- The marks are part of the conversation, not of your browser. Anyone the session is
  shared with sees the same passage marked, in the same place, and the **Highlights**
  strip under the conversation header lists them with the number of people who added
  them.
- **Remove highlight** takes yours away. Removing one does not touch anybody else's.
- Colour carries no meaning the product enforces; it means whatever your team decides.
- On a phone or tablet the colours appear in a bar at the **bottom of the screen** instead of
  beside the selection: your browser's own Copy / Share menu already covers the space just above
  whatever you selected.

Use it when a conversation is long and the interesting part is three screens up: a
highlight survives, where "look at the bit about the retry" does not.

## Seeing what a session changed

The **Changed files** strip under the conversation header lists the files that session
actually touched, and takes you straight to one. Click its heading to fold it open or
closed; the choice is remembered per session. **Ctrl+P** (**⌘P** on macOS), then **Tab**
to "Changed files", reaches the same list.

Each row carries a badge: **Untracked**, **Unstaged**, **Staged**, **Committed** or **No diff**
for a file in the working copy, **Session scratch** for a file in `$AF_WORK_DIR` (deleted with
the session), **Outside working copy** for anything else. A file in an agent's own memory folder
is badged **Claude memory**, **Codex memory** or, for another agent, **Agent memory**, and opens
read-only. That badge means the agent kind's own memory files, not Agent Fleet's
[agent memory](14-agent-memory.md).

It answers the question you have at review time ("what did this thing do to my
working copy?") without reading the whole conversation back. It is derived from the
conversation and the repository together, so it reflects what happened, not what the
agent said it would do.

## Reply suggestions

Under the message box, **Suggest replies with AI (from the recent conversation)** offers
a few things you might plausibly want to send next. They are suggestions, not actions:
nothing is sent until you choose one.

- **Always show (pin)** keeps a suggestion around.
- **Stop suggesting this** removes one, and sending the same text yourself brings it back.
- If the conversation is still short, or no AI is available for it, you are told so
  rather than shown an empty box.

## Sharing and handing over

The first two live in the session's ⋯ menu, and they are different things:

| | What it does | The other person |
|---|---|---|
| **Share…** | shows your conversation to another member of the same tenant, read-only | reads it; may be allowed to propose |
| **Hand off to another agent…** | starts a new session of your own, of any agent kind, carrying the context ([02](02-sessions.md#continuing-elsewhere-handoff-branch-move)) | nobody else is involved |
| **Hand off to a member** | offers that handoff to someone the session is shared with ([below](#handing-a-session-to-another-member)) | starts the new session in their own workspace |

A shared session appears in the other person's **Shared sessions** section and carries a
**Shared** badge on yours, so you can always see what is visible to someone else.
Sharing does not give access to your workspace, only to the conversation.

## Handing a session to another member

**Hand off to a member** is a button on the handoff proposal card in the conversation: the
card that **Hand off to another agent…** produces, or that an agent proposes on its own. It
does not start anything for you. It offers the proposal to one member, who starts the new
session in their own workspace if they accept.

- **Who can receive it**: only members this session is already shared with (read-only is
  enough). If it is shared with nobody, the dialog offers **Share this session** first.
- **What is sent**: a name for the new session, the handoff text, and the repository, branch
  and commit the Control Plane reads from your working copy. Nothing on your disk travels, and
  your workspace has to be running to send. A session has at most one waiting offer, and
  editing the card afterwards does not change what the recipient sees.
- **The push gate**: the recipient cannot see your disk, so unpushed commits or a branch that
  was never pushed stop the send. **Push, then send** pushes exactly the commit shown to the
  branch of the same name on `origin`, without force, tags or submodules, then checks again;
  if git refuses, nothing is sent and git's message is shown. Uncommitted changes are never
  pushed: you send anyway only after ticking that they are not handed over. A detached HEAD
  is refused outright, so check out a branch first.

The recipient sees the offer at the top of the inbox's **Received** list (**Handoffs from
members**), as a notification, and as a banner on the shared conversation with **Review and
take it over**. **Take it over** asks for a working copy and opens the usual launch dialog,
already filled in: the session runs in the recipient's workspace and bills to them. **Decline**
closes the offer.

What can happen to an offer:

- It **expires after 7 days** without an answer, and the sender is notified. The card's status
  line shows waiting, taken over, declined, withdrawn or expired.
- **Withdrawing** has no button of its own: pressing **Start new session** on the card while
  the offer waits asks first, then withdraws it before your launch dialog opens, so the work
  never starts on both sides. To change the text, withdraw this way and send again.
- A notification for an offer that is no longer open says it is gone and opens nothing.
- The offer is reserved when the recipient presses launch, not when the dialog opens. If the
  sender withdrew it, or another window is already launching it, nothing starts and a message
  says why. While a launch holds it, the sender cannot withdraw it and it does not expire. A
  launch that stops before the Control Plane confirms it for sending to the workspace stops
  holding the offer after 10 minutes.
- Once the launch is confirmed, an unanswered result keeps the offer reserved for the
  recipient: **press launch again to check** whether the session started. Ten minutes after the
  confirmation, if there is still no outcome, **Not in my workspace — give it back** appears.
  It returns the offer, but it does not cancel the earlier launch.

## Asking a teammate: the inbox

The **inbox** button in the top bar (beside the bell) is where members of the same tenant
send each other a **message** or a **request**. It has its own badge; inbox items never
appear in the notification centre, and an open request never expires there.

- **Send**: open the inbox → **New**, choose *Request* or *Message*, pick up to 20
  recipients and write the text (16 KiB at most). The item reaches every recipient or
  none of them: if one recipient does not accept items from you, nothing is sent and the
  inbox names who.
- **A request** is taken by one person. Each recipient can **Take it** or **Decline**;
  the first to take it becomes the assignee, and everyone else sees who did. The assignee
  can **Hand back** (it is open again), **Mark done**, start a session from it, and record
  the PR and final branch by hand. The sender can **Withdraw** it while it is open or
  taken; work already started is not stopped. A request everybody declined, or that nobody
  left in the tenant can take, ends as *Declined by everyone*.
- **Starting a session from a request** is for the assignee only and runs in your own
  workspace on your own account. The Control Plane checks that you still hold the request
  before the session is created, and records which session it was.
  If it cannot tell whether a session was started (the connection dropped mid-launch), it
  does not start another: check your sessions, and hand the request back and take it again
  to start over.
- **Receiving** (inbox → **Receiving**): accept from everyone in the tenant or from chosen
  members only, block a sender, or turn on *do not disturb*, which mutes the badge and
  toasts but still delivers.
- **Recipes**: tick *Attach a recipe* in **New** to send a prompt (16 KiB at most) with a
  suggested agent, model and effort, and optionally a repository (`owner/repo` on github.com
  or bitbucket.org; a pasted URL keeps no credentials or query). The recipient sees the
  repository apart from the prompt, and nothing is fetched or cloned on their side.
- **Taking a recipe in**: **Take into my memos** copies it into your memo queue as your own
  memo, marked with where it came from. For a request only the person who took it can do
  this; a message's recipe any recipient can. Pressing it again gives the same memo. That
  memo is always sent to a session on its own, never joined to your other memos, and a
  session started from it, or from a request, shows an inbox mark in the left pane.
- **Attaching a session**: pick up to 5 sessions, each one of yours or one shared with you. Attaching
  your own session can share it read-only with every recipient (the whole conversation,
  including what is added later) — you confirm the session, the recipients and the scope,
  and the share and the message are created together or not at all. A recipient who cannot
  see an attached session gets an empty placeholder, decided again on every read: if the
  share is removed later, the card turns into the placeholder. Withdrawing a request removes
  no share.
- **[Handoffs from members](#handing-a-session-to-another-member)** are listed at the top of
  **Received** while they wait for your answer, and they count on the inbox badge until you
  take them over or decline them; opening the inbox does not clear them. Open one to take it
  over or decline it, exactly
  as from its notification or the shared session. They are not inbox items: the
  **Receiving** settings, blocking and *do not disturb* do not apply to them — a handoff
  can only come from a member who shares that session with you.
- **Agents never see the inbox.** Nothing in it reaches your agents before you take it in,
  and agents cannot send. To have an agent write a request, let it draft a memo and paste
  it into **New**.

Limits: 20 recipients per item, 30 items per hour per sender, 10 pending items from one
sender to one recipient. Messages are kept 90 days from sending, requests 90 days after they
end; a request open for 14 days is marked as long open. Bodies are stored encrypted with the
tenant key when the deployment has one (the operator can still decrypt them), and
**Receiving** says so when it does not. Administrators get an audit record of who sent which
kind of item to whom, of every later step on it (take, decline, hand back, done, withdraw, PR
record, take-in, launch) and of every block or unblock, never the text.

## Showing teammates where you stand on usage limits

**Settings → Limit board** lets you tell the members of this tenant how close you are to your
agents' usage limits, so someone choosing who to ask for help can see who has room. It is
**hidden until you choose otherwise**, and the choice is per tenant: if you belong to several
tenants, each has its own setting, and the tab lists every tenant you currently publish to.

- **Who sees it:** hidden, everyone in the tenant, or members you pick.
- **How much:** *state only* (whether a session has hit a limit, a spend limit, a block or a
  sign-in problem), *band* (each quota as under 50% / under 80% / under 95% / 95% or more), or
  *exact* (the percentage and the reset time).

What you see of a teammate is only what they publish to you. Someone who publishes nothing to
you looks exactly like someone nobody has observed yet: **Unknown**. Your provider e-mail,
plan and error text are never shown, and an administrator gets no wider view.

How to read a value:

- "at least 42% (10 min ago)": what the provider last reported. Use only grows within a window,
  so it is a lower bound, and after 60 minutes it is marked **stale**.
- "reset not yet observed": the window has reset since. The old value no longer means anything,
  and it is **not** shown as 0% or available.
- **Unknown**: nothing was observed, the agent is not covered (the table is in
  [ref/limits.md](../ref/limits.md)), or the member does not publish to you.

Values arrive while your workspace runs, and from an open Console tab. When neither is the case
they go stale. Signing in to a different provider account hides the previous account's values.

The board never pools limits: nothing moves credentials, runs your work on someone else's
account, or ranks members by what they have left. Whether your subscription terms allow sharing
work this way is your organisation's call.

## Keeping a session awake

Idle workspaces stop on their own. If you have something long running that must not be
interrupted, **Keep awake** in the ⋯ menu exempts the session from idle-stop for a set
number of hours, and the row shows how much of that is left.

It is deliberately time-boxed. An exemption you set once and forget is how a workspace
ends up running for a month.

## Having a session stop when it is done

The opposite case: you want the session to **stop once the work is finished**: you left a
long job running overnight and would rather it did not hold the workspace up afterwards.

There are two ways to ask, and both set the same thing:

- **Tell the session**: "stop when this is done". It arms the stop itself.
- **Stop when this turn ends** in the ⋯ menu: set it yourself. Cancelling is in the same place.

An armed row carries a badge. The stop happens **after the running turn ends**: it will not
stop while a question is waiting for you or while work is still going. It is the resumable
stop, so the conversation stays and Start picks it up where it left off, and you get one
notification when it happens.

**Sending a new instruction releases it.** "Stop when you are done" was said about the work
in front of you, not about whatever you ask for next.

This is tidying rather than cost control: a session that has finished is not what keeps the
workspace awake, so stopping it does not change when the workspace stops. What you get is
the memory it was holding, back right away, and a list where "finished" reads as "stopped".
Idle auto-stop does the same thing on a delay, except that it skips a session while you
have it open, so stopping one while you watch it work is something only this can do.

## Locking something against deletion

**Lock against deletion** in the ⋯ menu marks a session as one that must not go away.
A locked session carries a badge saying so, and is **skipped by cleanup and by
automatic tidying**, both the manual sweep below and anything that would have removed
it on its own.

Use it for the conversation you will need at the end of the quarter, and for the
worktree you have not finished with. Cleanup refuses to remove a worktree that still
holds a locked session, and tells you to unlock it first rather than quietly working
around you.

## Cleaning up

**Clean up** surveys the stopped sessions, unneeded worktrees and merged branches that
have accumulated, and lets you clear them in one pass.

Every candidate is graded:

| Grade | Means |
|---|---|
| **Safe** | nothing is lost by removing it |
| **Review** | probably fine, but look first |
| **Keep** | it is running, or it has uncommitted or unpushed work. Stop it or push it first |

**Select all safe** does the boring majority in one click. What you remove is not
destroyed: sessions and branches go to **Trash (restore)**, the second tab of the same
dialog, and can be brought back.

Archived sessions are a separate thing from the trash and are not part of cleanup.
They have their own browser.

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

Related: [02 Sessions](02-sessions.md) for launching and resuming ·
[ref/features.md](../ref/features.md) for the catalogue ·
[ref/agents.md](../ref/agents.md) for which agents support what

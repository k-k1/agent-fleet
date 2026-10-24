---
audience: "anyone working with other members on the same conversation"
updated: "2026-10"
---

# 12. Working with others — sharing, handing over, the inbox, usage limits

English | [日本語](12-collaboration.ja.md)

This chapter covers the parts of the Console for **more than one person**: sharing a conversation, handing a session over to another member, asking a teammate through the inbox, and showing where you stand on usage limits.

## Sharing and handing over

Two different things live in the session's ⋯ menu. **Share…** shows your conversation to another
member of the same tenant, read-only ([Sharing a conversation](#sharing-a-conversation-shared-sessions)).
Handing over starts a new session instead: **Hand off to another agent…** starts one of your own,
with the context carried over ([05](05-stopping-and-tidying.md#continuing-elsewhere-handoff-branch-move)),
and **Hand off to a member** offers that handoff to someone the session is shared with, who starts
it in their own workspace ([below](#handing-a-session-to-another-member)).

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

The tab also shows **Your values (only you see this)**, a **Members** list with what the others
publish to you, and **What is collected**, the agents the board covers. A tenant administrator can
turn the limit board off for the whole tenant; then nobody's values are collected or shown, and the
tab says so.

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

## Sharing a conversation (shared sessions)

You can show a session's conversation to another member of the same tenant, **read-only**:
"keep an eye on where this investigation goes", "while you review it, here is how we got here".

A shared session appears in the other person's **Shared sessions** section and carries a
**Shared** badge on yours, so you can always see what is visible to someone else.
Sharing does not give access to your workspace, only to the conversation.

**Sharing it**

1. Choose **"Share"** from the **⋯** menu of a session row or a repository row (you can also
   create one from ⚙ "Shares" in the left pane's **Shared sessions** section → **"New share…"**).
2. There are three kinds of **target**.
   - **Session**: that one session.
   - **Project**: the base working copy **plus the sessions in the worktrees under it**. If you
     give each session its own worktree, this is usually the one you want.
   - **WT**: a single worktree.
3. Pick the **recipient** by searching for their email address.
4. Pick the **permission**.
   - **View only**: reading, nothing else.
   - **Can propose**: the recipient can **propose** something to send, and **it does not reach
     the agent until you approve it**.
5. **"Unshare"** in the share list ends it.

**Being shared with**

They appear under **Shared sessions** in the left pane, grouped by owner and project. The
conversation opens read-only; with "may propose" there is a composer for sending the owner a
proposal (it says up front that nothing is sent until the owner approves). While the owner's
workspace is stopped, the history cannot be read. The list refreshes on its own, but
**"Refresh"** pulls it right now.

If the session proposed a **handoff to a new session**, that card appears in the shared
conversation too, at the point it was proposed, with the title and the full prompt. Only the
owner can edit it, discard it, or launch from it, so those buttons are not shown to you.

If the agent is **stopped on a question**, **its options appear in the shared conversation too**,
so the recipient can see what it is waiting on. Only the owner can answer (with "may propose" you
send it as a proposal and wait for approval).

The view behaves like a session mirror: **opening a shared conversation for the first time
lands on the latest message**, and if you leave part-way through, **reopening it returns you
to where you were** (reloading the browser starts from the latest again).

**Approving** (as the owner): the Shared sessions section shows **"N awaiting approval"**;
review the content and choose **"Approve and send"** or **"Reject"**.

> **Sharing exposes the whole conversation**: your prompts, the agent's replies, and tool
> output. Secrets that ended up in the conversation are not detected for you. The recipient can
> save what they see, so **unsharing cannot recall the copy they already have.** Think once about
> what is on screen before you share. Archiving a session drops it from the recipient's list (the
> rule itself remains, so restoring it makes it visible again).

# 0057. Build handing over between members as a derivative of the share ACL, and let only prose and coordinates cross the boundary

English | [日本語](0057-member-handoff.ja.md)

- Status: **adopted, not implemented** (2026-08-24). The design and the implementation stages are in [docs/77](../log/77-member-handoff.md).
- See also: [59-session-sharing.md](../log/59-session-sharing.md) (the underlying ACL, freezing the body, the discipline of expiry. There is no ADR; the design document is canonical) /
  [0041-cross-session-messaging.md](0041-cross-session-messaging.md) (messaging within one workspace — the source of the envelope, permission laundering, and "the valve you need once there are N senders") /
  [0035-session-report-v2-ledger.md](0035-session-report-v2-ledger.md) (why the arm and the ledger are not touched)

## Context

The fleet had two kinds of "handoff". `propose_session_handoff` proposes the first prompt for the next
session **inside your own workspace**, while session sharing (docs/59) shows a conversation to **another
member of the same tenant**. The shared view already displays the handoff card read-only
(`session_share.go:639`), but there is no route for B to take it and run it in their own workspace.

The starting question was "how should we design an MCP tool for handing over to a member?", but the
investigation showed it is **not a tool design problem but a UI problem of adding one move on top of
the share ACL**. What follows is the record of the options discarded on the way, and of the shape that
remained.

## Decision 1 — the session entity does not move. What crosses is prose, git coordinates and read access

"Hand over" means three things: (A) handing over the work, (B) handing over the conversation, and
(C) transferring ownership. **We discard (C).** The transcript, the worktree, the uncommitted changes
and the agent CLI's OAuth all live in A's home; the cost attaches per membership; and the
authentication is A's account. Moving it would mean "B's work runs on A's billing and A's account".

This decision largely settles the rest. Only three things are portable — (1) prose, (2) pushed git
state, (3) read access to the conversation — and decision 5's push gate falls out of that
mechanically.

## Decision 2 — the recipient is limited to someone the session is already shared with

Allowing any tenant member as the recipient drags in a chain of design problems: how the roster is
looked up, whether the conversation is handed over with it, and expiry when the relationship ends.
**Making "already shared" a precondition removes all four** (docs/77 §77.4). A share is **a trust
relationship that already exists** — "A has decided to show this person" — and reusing it as the
handoff's authorisation is the cheapest thing to do.

**Rejected**: combining "share and hand over" into one operation. It saves a step, but it opens the
whole conversation as a side effect of handing over. Sharing and handing over are different
judgements, so the extra move stays.

## Decision 3 — no "execution" flows from A to B. B launches with their own privileges

There is no shape in which the CP hits A's workspace to make something happen. B creates a session
**in their own workspace from their own Console**, using the existing launch path unchanged.

That makes docs/59's RW proposals' requirements — **the owner lease, the idempotency ledger and
double-execution prevention — entirely unnecessary**. Those were needed there because "the CP sends a
share recipient's proposal to the owner's Agent", i.e. cross-boundary execution. This feature has no
such structure.

## Decision 4 — do not distribute a new MCP tool

As a result of decisions 2 and 3, choosing the recipient, sending and receiving are all on the Console
side. The only job left for the agent is writing the handoff body, and `propose_session_handoff`
already does that. The addition is limited to an **optional `to` hint** on that same tool.

Two reasons. A tool **lives permanently in every session's system prompt** (the same cost that made
[docs/58](../log/58-cross-session-messaging.md) §58.14 cut its description down to one worked
example). And distributing a tool lets **an agent write into someone else's inbox** — a session that
read a poisoned repository being able to throw a work request at a colleague under A's name is the
permission laundering ADR 0041 took into its envelope, with a human on the other end. In this shape
the agent gains no new authority.

**Rejected**: `propose_member_handoff` plus `list_tenant_members`. The latter leaves the tenant roster
in the model's context and in the transcript. Making direct agent sending a configurable option is not
adopted either, because even off by default it leaves "only environments that turned it on are
defenceless".

## Decision 5 — unpushed commits block sending; dirty is only a warning

Even with a share, A's uncommitted changes are not on B's disk. A handoff that has not been pushed is
a lie however well written, and B's new session starts out presuming a commit that does not exist.
**Unpushed is a red stop.** Uncommitted stays a warning, because some handoffs deliberately discard it.

⚠️ **Both the check and getting the repo coordinates happen in the Console (server side); the model
does not write them.** Making the coordinates a structured field the model can write turns it, the
moment the Console makes it a clone route, into the same shape as "just by getting a diagram opened" —
a tool for making B clone an arbitrary remote just by getting a repository read.

## Decision 6 — A's local proposal survives sending (an offer, not a transfer)

Sending means "offering a copy of the local proposal", not the proposal itself leaving. So "if nobody
takes it, A starts it themselves" is **just the existing launch button in the mirror continuing to
work**, and no new route is needed. A's launch button takes three states depending on the offer
(pending → confirm and auto-withdraw; declined/expired → straight through; accepted → warn about
duplicate work).

⚠️ **The auto-withdrawal happens in the same transaction as the launch.** The race between "A got
impatient and started it themselves" and "B accepted it" is the worst failure this feature has (the
same work running twice).

**Rejected**: a transfer model that deletes the local proposal on sending. Then nothing is left at hand
when it is declined.

## Decision 7 — once sent, the canonical body is the CP's snapshot; local edits do not follow

B must be able to receive it even if A's session has gone (`removeHandoffProposals` deletes on slot
reuse) and even if A's workspace is stopped. So the canonical copy is on the CP. The local one remains
only as the card's position in the mirror (the immutable-`CreatedAt` rule) and as the starting point
for editing.

**Local edits after sending do not affect the offer.** If they did, the body would change while B was
reading it — the same reason docs/59's RW proposals freeze the body. To change it, withdraw and send
again. This asymmetry is stated in the UI (silently having no effect is the worst outcome).

## Decision 8 — notifications flow past; the badge is the inventory; the ledger is the history

Do not mix the three. **A notification may flow past once read** (there is no strict rescue for a
missed one). **The badge does not clear while it is pending** — clearing it on "read but not decided"
gets handoffs forgotten. **The ledger** (A's list of "handoffs I sent") is the place to trace it after
the notification has flowed past. Once notifications are declared to be ephemeral, the ledger is a
necessity rather than a choice.

There are only two notification kinds: `handoff-offer` to B and `handoff-accepted` to A. **No decline
notification** (what A wants to know is whether it was taken over, not why), but so that it is not left
hanging and forgotten, **one goes to A just before it expires**.

⚠️ **`InsertNotification` is called directly from the CP.** Existing session notifications go through
`drainAgentOutbox`, i.e. they do not appear unless the workspace is running — but for a handoff, both
A's and B's workspaces being stopped is the main battlefield.
⚠️ Idempotency is `ON CONFLICT(event_id)` and **does not include the membership**, so the event_id is
composed of `offer id + kind + recipient` (using the same id for two notifications silently deletes
one of them).

## Decision 9 — a separate table (it does not ride `session_share_proposal`)

`session_share_proposal` is B → A (a share recipient proposes and the owner approves), while
`session_handoff_offer` is A → B (the owner offers and the share recipient accepts) — **the direction
is reversed**. The meanings of `owner` and `proposer` swap, so putting them in the same table
guarantees a misreading. **Only the practices are reused** — linkage to the `catalog_id` for ACL
coupling, encrypting the body (where a tenant key custodian exists), erasing the body on expiry, and
`membershipCascade`.

## Decision 10 — one outstanding offer per session, to one recipient

With a share scope of `repo` or `worktree`, several viewers is normal, and being able to throw it at
several people at once produces **a race plus duplicate work**. To hand it to someone else, withdraw
and send again (decision 6's three states provide the route).

## Impact and what remains

- **The agent gains no authority.** The only addition is one optional hint on
  `propose_session_handoff`.
- **B accepting wakes B's workspace and bills B.** Accepting goes as far as opening a pre-filled launch
  modal in one click; B presses to confirm. The edited body becomes B's instruction.
- **It cannot be handed to someone it is not shared with** (decision 2). Operationally that adds one
  move, "share it first".
- It sits alongside docs/58's messaging as a different thing. Do not mix the three words
  **handoff / share / message**.
- Remaining: the implementation (P0–P3 in docs/77 §77.14).

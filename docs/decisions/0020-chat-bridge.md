# 0020. The chat bridge adopts the "outbound always-on connection" model (Slack Socket Mode / Discord Gateway), and Teams is demoted to a send-only slot

English | [日本語](0020-chat-bridge.ja.md)

- Status: **adopted, in implementation** (2026-07-22/23). Implemented: P1 and P1.5 (one-way
  Discord notifications), P2a (receiving — a thread reply injected into the session), the
  full-text bridge (reply bodies into chat, opt-in), P2b (buttons for AUQ / permissions / plan
  approval, on claude/TUI and managed), the early part of P3 (@mention → the fleet, the operator
  conversation, a dedicated thread), and the P3 approval gate (destructive operations — the
  deletions and shell — approved with a Discord button); plus **Slack brought up to full feature
  parity over Socket Mode** (2026-07-23; Discord and Slack can be connected simultaneously, so
  the stores are scoped by provider). The implementation plan is [docs/37](../log/37-chat-bridge.md).
- See also: [docs/30](../log/30-session-report.md) (completion reports — where the notification
  content comes from), docs/25 (PagerDuty/Grafana — the precedent for adding a Connection),
  build/07-security.md §7.6 (secrets pass straight through the CP).

## Context

We want a way to get "waiting for input", "finished" and "died" out of the Console and onto a
phone, and to reply, answer a multiple-choice question and approve right there. Notifications
today are browser `Notification` (foreground only) with no web-push, so the fleet stalls while
you are out.

The receiving path is not the same shape across the three candidates: Slack (Socket Mode) and
Discord (Gateway) **complete receiving and button responses over a single outbound WSS** with no
public endpoint required. Teams (Bot Framework) requires a public HTTPS endpoint plus an Entra ID
app registration, which is fundamentally incompatible with this product's deployment shapes
(native / WSL2 / behind NAT).

## Decision

1. **The provider abstraction carries capability flags (canSend/canReceive/canInteract)**, and
   v1 has only the always-connected implementations for Slack and Discord (all three flags). Teams
   goes onto the same interface later as a send-only implementation (canSend only, a Workflows
   webhook). We **do not** host a public endpoint for the sake of two-way traffic (not breaking
   the deployment shapes comes first).
2. **The bridge itself (the WSS connection, sending and receiving) lives in the workspace
   Agent.** The token is in the per-user `secrets.enc`, which maintains the principle of not
   giving the CP any secrets.
3. **Users register their own token** (their own Slack App / Discord Bot). No centrally shared
   app in v1 — this avoids the weight of tenant management, review and scope management, and lets
   us get by with the same "add a Connections card" pattern as PagerDuty/Grafana.
4. **Delivery is a fan-out beside the notification outbox** (right after `notice.Put`) and does
   not block the outbox itself. Fire-and-forget with bounded retries. The chat side is **a copy**
   of the notification centre; seen state is not synchronised.
5. **Two-way traffic is limited to the person themselves**: an explicit binding between the
   provider-side user ID and the AF user is required, and only the bound person's DMs, messages
   and button presses are routed. There is no surface that listens to a channel (the whole
   defence against prompt injection and approval by someone else is concentrated in this one
   point).
6. **Questions and approvals are mapped structurally**: the options of an AUQ /
   permission-request become buttons, and the answer goes back through the existing structured
   route (/respond, send_to_session). It does not go through sending keys to a tmux pane (the
   lesson of how fragile key-driven AUQ is).

## Results (expected, and the constraints accepted)

- Replying, answering an AUQ and approving from a phone via Slack/Discord all work, giving a
  practical route around the Console's mobile limitations (no browser pane, and so on).
- Constraints accepted: Teams users get notifications only (use Slack/Discord alongside if you
  need two-way). The setup cost of users registering their own app — mitigated by a setup wizard
  in the Connections card (validate the token → generate the invite URL → a channel picker → a
  test notification on connect; no copying numeric IDs. docs/37 P1 addendum). Read state drifts
  between the chat side and the notification centre (accepted, under the "it is a copy"
  principle). Drift in the external platforms' specifications (the live contract tests
  `AF_SLACK_LIVE` / `AF_DISCORD_LIVE` are the primary detector).
- **The constraint on receiving (P2a)**: reading Discord thread replies requires the
  **MESSAGE_CONTENT privileged intent** (for a bot in fewer than 100 servers, one checkbox in the
  developer portal, no review). Receiving is opt-in (`Discord.Receive`), and a Gateway WSS is
  opened only for users who enable it (for memory's sake). Routing on receipt uses contract 5's
  person-only rule (messages in a thread from the bound user) as its only defence, and no surface
  listens to a channel. DM-mode receiving is out of scope as there is no mapping table (a channel
  + thread arrangement is assumed).
- **The constraint on the full-text bridge** (implemented under docs/37's "future directions"):
  in a local-only environment with no external reachability the Console deep link is dead, so the
  body of the final answer-ready turn is put into chat, promoting it to "a remote UI that stands
  on its own". This breaks decision 4's "chat is a copy", so it is **limited to off-by-default
  plus an explicit per-connection opt-in (`FullText`)** and the default posture stays "a copy"
  (automatically enabling full text based on the reachability of `AF_CP_BASE_URL` was rejected —
  actively probing reachability misdiagnoses, so an explicit toggle only). Consistency with
  decision 2's "no secrets on the wire" comes from layered scrubbing (known token shapes,
  uppercase env assignments, high-entropy standalone tokens), with the primary defence being
  "the person owns both ends". Only the body at turn completion is sent (tool logs, thinking and
  raw logs are not), and 2000 characters are split. It is confined to a person putting their own
  output into their own chat. **Tidy-up (2026-07-22)**: in full-text mode **only the body** is
  posted (the heading, the "display name" and the deep-link preamble are dropped — the thread name
  carries enough context, and the link is usually dead in a local-only environment). Alongside
  that, **mentions are time-gated** (items needing action and abnormal events are always pushed;
  a read-only answer-ready only @mentions when the thread has been quiet for the default 10
  minutes) and there is **an ack on receipt** (a successful reply injection = a 👀 reaction plus
  typing; a failure = a localised reason replied into the thread).
- **The constraint on P2b (buttons)**: questions, permissions and plan approvals are answered
  with Message Components. If no Interactions Endpoint URL is set, the interaction arrives on the
  Gateway as `INTERACTION_CREATE` (consistent with local-only, no external endpoint), so it rides
  P2a's receiving Gateway and needs no public endpoint. Answers use contract 6's structured
  mapping (and even for key sending, the presser is verified by contract 5's person-only rule).
  For claude/TUI a hook records the pending payload and Go replays the key sequence verified in
  MirrorView. **Managed (codex/opencode/copilot) is also implemented (2026-07-22)**: the
  identifier mismatch we had worried about ("the rollout call_id versus the live Interaction id")
  was resolved by **not putting the id in custom_id and instead re-fetching the current
  Interaction with `Resume→Snapshot` at answer time** (the sending side peeks at questions via
  `codex.PendingInteraction` without resuming, and attaches them to the notification). Staleness
  is double-guarded by a fingerprint plus an id check in `Respond`. Only single selection is
  supported (multi-select stays text, answered in the Console), and multiple questions accumulate
  per session and submit once they are all answered. Verifying a real click on live codex remains,
  after a rebuild.
- **The P3 approval gate (approving destructive operations with a Discord button)**: only when
  the operator conversation is **Discord-driven (unattended)**, destructive operations (the
  deletions — `delete_session` / `delete_worktree` / `delete_branch` /
  `purge_cleanup_archive` — and shell execution — `create_session(kind=shell)` and
  `send_to_session` to a shell) are mapped to approve/reject buttons immediately before
  execution, and stop until the person presses one (the presser is verified by contract 5). Going
  through the Console is not gated, as before, because a human is watching — so **the
  Discord/Console distinction** is made by an origin marker that `runOperatorTurn` arms
  (`handleChatSend` does not write it), because the conversation and the spawn arguments are
  identical and there is no other signal. The writing MCP runs in **a separate subprocess** while
  the button press arrives at **the daemon**, so the two coordinate through a shared file (the
  approval record). As with P2b, if no Interactions Endpoint is set the interaction arrives on the
  Gateway as `INTERACTION_CREATE`, so no public endpoint is needed. **Fail-safe** = if there is no
  route (thread/connection) to deliver the approval, do not execute (fail-closed). Execution stays
  the subprocess's REST relay (no duplicated logic), and while waiting for approval the turn
  timeout is set longer than `chatTimeout` to allow time to approve while out.
  `stop_session` / `archive_session` are reversible and so are out of scope. Looking at a real
  click on real hardware remains, after a rebuild.
- **The early part of P3 (@mention → the operator conversation)**: you can chat not only with
  sessions but with the built-in operator conversation (`assistants.go` "operator", `af_write`).
  On the receiving side there is **a dedicated operator thread** (contract 5's person-only rule is
  maintained; `routeInbound` branches on a thread→conv match in `bridge-operator.json`, a
  different file from `ThreadToSession`). The turn machinery is `runOperatorTurn`, which runs the
  existing `runReportAutoTurn` family without HTTP (the same shape as `handleChatSend`) — nothing
  reinvented. The canonical conversation is one continuous conversation (shared with the Console,
  deep-linkable; its growth is capped by the preventive automatic compaction in docs/33). The
  reply does not ride the answer-ready notification, so the receiving side posts it explicitly
  into the same thread (`ScrubSecrets` plus a 2000-character split). Autonomous replies to reports
  from sessions the operator instructed are mirrored into the thread too, closing the loop while
  out. Disconnecting discards the thread coordinates but keeps the conversation. Approval of
  destructive operations rides P2b's button machinery in the remaining part of P3.
- **Slack brought level over Socket Mode, at full feature parity (2026-07-23)**: this collects on
  decision 1's "the abstraction assumes two providers from the start". `Provider`,
  `ResumableSender`, the file queue, `ScrubSecrets`, `custom_id` + `ParseCustomID` and
  `ReceiverDeps` are reused unmodified, and adding only the Slack-specific parts (`slack.go` for
  the sending Web API, `slack_interact.go` for Block Kit, `slack_socket.go` for Socket Mode
  receiving) makes every Discord feature work on Slack too (sending, thread = session, mentions,
  deep links, full text, two-way receiving, buttons for AUQ/permissions/plans, the operator
  conversation, the P3 approval gate). **Discord and Slack may be connected at the same time**
  (the thread/operator stores are scoped by provider — separate files; operator replies and
  approvals find their destination by scanning conv→provider). The Slack differences: two tokens
  (a bot `xoxb-` and an app-level `xapp-`); threads are thread_ts only (no archive); the Web API
  wraps everything in `{ok,error}`; there is no typing indicator (👀 only); the markup is mrkdwn;
  and there is a single bound user (the DM target, the mention, and the identity check). The bound
  user is resolved automatically by email → users.lookupByEmail. The live contract test is
  `AF_SLACK_LIVE`. Looking at real Slack on real hardware remains, after a rebuild.

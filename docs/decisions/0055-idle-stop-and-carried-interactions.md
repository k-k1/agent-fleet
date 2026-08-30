# 0055. Do not keep a container awake waiting on a human decision. Carry the interaction over before folding it up

English | [日本語](0055-idle-stop-and-carried-interactions.ja.md)

- Status: **adopted** (2026-08-24). The record of the investigation and the measurements is [docs/75](../log/75-idle-stop-and-pending-interactions.md).
- See also: [0030-turn-abort-auto-resume.md](0030-turn-abort-auto-resume.md) (splitting live state by "the next move to prompt") /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (stopping = releasing a slot = cost) /
  [docs/history/p3-9-idle-stop.md](../log/p3-9-idle-stop.md) (the prototype of the two-tier arrangement)

## Context

If a session has been left with an AskUserQuestion on screen, its workspace **never stops**.
`reaper.go`'s busy check counted `question` and also excluded it from tier 1, so both tiers were
closed. On ECS / EC2 pools a workspace that does not stop simply bills, and in
[docs/64](../log/64-ec2-persistent-workspace.md) §64.26 a workspace nobody was touching occupied an
m7i.large for 9.4 hours overnight.

The same shape was measured on 2026-07-31 with the usage-limit menu (`blocked`), and that was closed
by removing blocked from busy. Only question remained as an exception, on the premise that it is "a
dialogue that gets an answer straight away".

## Decision 1 — the only reason to keep a container awake is that a machine is working

`holdsWorkspace` looks only at `machineBusy` (working, or `backgroundBusy`). Waiting on a human
(question / plan / permission / blocked / auth / spend_limit) is not a reason. Waiting on a human can
last days, so keeping a container up for it amounts to "billing until you answer".

**Rejected**: keep treating question as a special case. In real use questions cross the night, so the
premise does not hold.

## Decision 2 — but "it may be folded up" holds only after "it can be carried over" does

Fixing the conditions alone would drop questions into the hole plan and permission had already fallen
into: the pending payload is deleted by the SessionStart hook on resume, taking with it the very fact
that a human decision was awaited. So the order **P1 (carrying over) before P2 (switching the
conditions)** is fixed as a decision.

## Decision 3 — pending and carried are different stores with different wire keys

`pending-*` means "a modal is on screen right now", and the Console answers it with a key sequence
(Down/Enter). A carried interaction has no modal, so the answer can only be delivered **as prose**.
Riding on the same key would mean telling "is it on screen?" from `alive`, and that distinction has
never once been implemented (the pending card does not look at alive today either). With separate
stores, the code that fires keys cannot reach a carried interaction.

## Decision 4 — what is restored is the intent, not the modal

**Measured (claude 2.1.241, docs/75 §75.10)**: an unanswered `tool_use` is bypassed via `parentUuid`
on resume and drops out of the conversation tree. `--resume` does not bring the modal back, and there
is no way to bring it back. So what a carried interaction carries is only the answer (for a question),
approve/reject (for a plan) or the fact (for a permission).

The delivery wording is **part of the feature**. Without the sentence "do not ask again; continue
using this answer", claude asks again (measured). The wording is a fixed value in code, bound by
tests.

## Decision 5 — approving a plan is approving an irreversible execution

**Measured**: sending approval as prose makes claude **execute directly** without re-emitting
ExitPlanMode (the pane's footer stays in plan mode). The original idea — "resume in plan mode and the
TUI will re-present it, so approve on a live modal" — was refuted. So the Console's button asks for
confirmation.

## Decision 6 — waiting on a human gets its own clock (`interaction_idle_timeout`)

Separate from `session_idle_timeout`. They answer different questions: "how much RAM may an idle
session hold?" versus "how long do we keep a container up for someone who has not come back to
decide?". The defaults are the same value. If a tenant configured only `session`, waiting on a human
follows that value too (having one of them run on the deployment default makes the number shown in the
admin screen a lie).

## Decision 7 — show "waiting for an answer" in the list even while stopped

Now that waiting on a human can be folded up, a folded question that is invisible in the list is, from
the user's point of view, the same as "silently lost". `carried` is added to the session wire and
carried on **both the CP's relay and the database mirror** (the mirror is the only source for the list
while stopped, so with only one of them **the badge would vanish the moment it stopped**).

## Decision 8 — do not guess for shell / ssm; protect them with the user's "do not auto-stop" pin

The foreground command name **cannot tell an abandoned `less` from a running build** (measured), and
ssm keeps `exec aws ssm start-session` open so it is always non-shell, i.e. always busy. Automatic
detection breaks in both directions, so it is not adopted. Instead there is `Meta.KeepAwakeUntil`,
which falls to machineBusy at the very outside of `sessionActivity` (shell/ssm have an empty state and
are not caught by the inner classification). It is **a timestamp rather than a boolean** because a pin
someone forgot to clear becomes the same thing as a terminal tab someone forgot to close: something
that silently keeps billing. It applies only to live sessions, and an unreadable value falls to "no
pin" (a corrupt string keeping a container up forever is the more expensive failure).

## Decision 10 — presence means "recent keystrokes", not "a connection exists"

Forgetting to close one Console tab with a terminal pane open keeps the presence lease refreshing
every five seconds and the workspace never stops. It is a main cause of "it does not stop" alongside
question, and worse, the user cannot see that they are still being billed. So terminal presence alone
is bound to keystrokes (`AF_PRESENCE_IDLE_TIMEOUT`, default 30 minutes, 0 disables).

- **Not per tenant**: this is not a billing policy but a constant about human attention. How long
  until it stops is still decided by `ws_idle_timeout` (which is per tenant).
- **Ping and resize do not count as keystrokes**: the Console periodically pings an open socket, so
  treating "a frame arrived" as presence would restore the original behaviour exactly.
- **Stop the database presence lease too**: fixing only the in-memory judgement leaves
  `WorkspaceHasRecentActivity` returning true from `connected_until > now`, and nothing changes.
- Non-terminal cases (a scheduled wake, the browser pane) are left unconditional. The former has no
  keystrokes, and the latter only holds a connection while visible, so visibility itself is the
  presence signal.
- **Add an activity beacon as its counterpart** (`POST /api/workspace/attention`). Restricting to
  keystrokes makes someone reading back through the mirror — neither typing nor sending — look absent,
  and if the container stops while they are reading they cannot even get the transcript. The Console
  sends **a real interaction while visible** (`isTrusted`) at most once every 60 seconds — what it
  sends is "a human interacted", not "a tab is open", and confusing the two restores exactly the
  behaviour P3 removed. **It does not trigger auto-start.**

## Decision 11 — publish the reaper's decision as it is; do not recompute it

When automatic stopping does not work, the operator could see nothing (the reaper only logs, and the
only way to investigate was `docker exec` into someone else's container to read the status file).
P0–P3 added more inputs to the judgement (waiting on a human, background work, presence, the pin), and
leaving it invisible makes "it does not stop" unexplainable.

The admin screen's member roster and detail show "when will it stop / who is holding it". **Not
recomputing on the screen side** is the substance of this decision: deriving it separately would
diverge from what the reaper actually looks at, and the screen built for investigating a cause would
give a different answer. So it only reads the observations the reaper writes on each sweep (freshness
is the sweep interval; the screen shows the observation time and does not assert to the second).

- "It will not stop" and "no schedule has been produced" are shown as different things. A tenant with
  it disabled is called "disabled" (it must be distinguishable from a misconfiguration).
- The pin is explained before working: something the user explicitly declared is a stronger
  explanation than a turn happening to be running (release it and it stops).

## Decision 12 — having widened which kinds may be folded up, widen carrying over by the same amount

Tier 1's gate widened from `kind == "claude"` to "is halt resumable" (`tier1Foldable`; only shell and
ssm are excluded). halt is a stop that any agent kind can resume from, and there is no reason to keep
only claude on the cheap reclamation path.

But the moment the gate widened, **decision 2 (fold up only when nothing is lost) became a requirement
for non-claude kinds too**. And where a pending interaction lives differs per kind, with different
lifetimes (the table in docs/75 §75.7.2): some stay on disk (agy's conversation DB, copilot's
events.jsonl, opencode's store) and some **die with the process** (kiro TUI's approval panel — a string
in the pane; and the ACP trio's `session/request_permission` — in the driver's memory). To avoid
scattering the reading logic across the folding side, the entrance is a single
`agents.ModalReporter` (`PendingModal`). claude alone is not implemented — there the `pending-*` files
the hooks write are canonical, and the same thing is not asserted from two places.

- **Capture before folding.** `halt` before `DropHandle` / `kill-session`, and `gracefulShutdown`
  before `AbortManaged`. The reverse order means it is always empty when called (it actually went in
  that way, and carrying over on managed had never once fired).
- **Do not pretend to have captured what cannot be captured.** If the container is SIGKILLed, the
  pending ACP / pane interactions are lost. Leaving traces on disk is possible, but **the destination
  for the yes/no is dead either way**, so all that could be restored is the fact — not worth adding
  writes at ask time.

## Decision 13 — permissions are not carried over in the shape of a question

Both the ACP Interaction and agy's synthesised menu are shaped as a `question` so the Console can draw
a choice card. But carrying them over in that shape would **offer "Yes / No" after folding and let the
user believe they sent it to a destination that no longer exists** (permission granted but nothing
executed, or the reverse). The destination for the yes/no (the JSON-RPC id, the TUI's modal) died with
the process, so a carried interaction degrades to `permission`, i.e. the fact alone. It is a corollary
of decision 4's "what is restored is the intent, not the modal": for a permission there is no intent
that can be carried.

The display side, conversely, **may stay `question`** (the mirror's permission card and the list's chip
both call themselves `question`). "How to show it now" and "what can be delivered after folding" are
different axes, and matching the former to carrying over would break the vocabulary of live cards.

**codex and opencode have no permission carrying** — there is no approval route at all (managed codex
auto-answers, the TUI starts in bypass, and managed opencode auto-allows unconditionally). Their
waiting-on-a-human is only the question tool, and that is carried over complete with its answer form.

## Decision 9 — an unknown state falls to neither side

`sessionActivity` defaults to `unknown`, i.e. "neither keep it awake nor fold it up". When a new state
appears, falling to the keep-awake side warms a workspace forever, and falling to the fold-up side
kills something we do not understand. The classification is pinned by a table-driven test (this
judgement has drifted twice already).

## Impact and what remains

- The behaviour changes while staying on by default: a workspace holding a question, which used not to
  stop, is folded up at `interaction_idle_timeout` (default = `session_idle_timeout` = 1h) and then
  stopped by tier 2. **The folded interaction is not lost**, and answering from the Console's card
  resumes and delivers it.
- Implemented through P3 (presence TTL). **A terminal with no keystrokes drops out of presence after 30
  minutes**, so a workspace left with a terminal open while someone stepped away now stops at
  `ws_idle_timeout`. Long work is protected by the "do not auto-stop" pin (decision 8).
- P0–P5 are implemented (the folding conditions, carrying over, the stopped badge, the pin, the
  presence TTL, the activity beacon, the operations screen, carrying over for non-claude kinds, halt
  notifications, and draining just before a stop).
- **What decisions 12 / 13 change**: non-claude sessions are now folded up at
  `interaction_idle_timeout` too, and their pending interactions are carried over. A badge appears in
  the list while stopped, and answering from the mirror's card **resumes and delivers it as prose**
  (`ThreadHandle.Send` for managed, keystrokes for a TUI). For permissions only "what was being asked"
  survives; the yes/no is not carried.
- **codex's `compacting` was added to machineBusy.** The tenth state that appears on the wire was
  missing from `sessionActivity`'s table (exactly the drift decision 9 was supposed to prevent), and it
  was a state in which the whole workspace could stop mid-compaction.
- Remaining: **one full pass on a real ECS deployment** (which needs an image rebuild and real ECS). A
  full pass against a real Agent stood up with a throwaway HOME and a dedicated tmux socket has been
  measured (docs/75 §75.10.1 G). Capturing **permissions** for cursor / kiro / copilot stops at unit
  tests, because the permission request itself could not be reproduced on real hardware (ibid. J) — we
  do not write "it should work".

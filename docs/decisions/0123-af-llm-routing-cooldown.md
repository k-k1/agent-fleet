# 0123. Agent Fleet's own LLM calls step past a rate-limited backend: one cooldown table, read by every resolver

English | [日本語](0123-af-llm-routing-cooldown.ja.md)

- Status: **accepted** (2026-10-26). Built in `workspace/agent/internal/routehealth` and wired into
  the chatx resolvers. Verified by unit and handler tests with stub providers; **not yet seen on a
  real usage limit**.
- Issues: #1564, #2780
- Related: [0030](0030-turn-abort-auto-resume.md) (CLI sessions wait for the reset instead) /
  [0117](0117-background-memory-skill-review.md) (the deny-all memory review)

## Context

Agent Fleet runs LLM calls of its own: assistant chat (Console, Discord/Slack operator turns,
scheduled assistant runs, auto-turns on session reports), the one-shot generations (titles, branch
names, reply chips, translation, edit suggestions, plan updates) and the background memory review.
Each picks a backend from a priority order (Settings > Assistant, Settings > AI assistance), but
"usable" meant only "the CLI is logged in". A turn that hit a usage limit came back to the user
as an error, and the next turn went to the same backend and failed the same way, for hours.

The rate-limit knowledge that existed (`agents/claude` abort markers and reset parsing,
`agents/muse` quota reading, `sessionx/rate_limit_resume.go`) served CLI sessions only.

## Decision

1. **One classifier, one table.** `routehealth.Classify` reads a failed call's error text into a
   class. Claude and muse go through the readers their sessions already use, so the router and the
   session auto-resume agree on what a limit looks like; other kinds use generic markers and a
   whole-word HTTP status. `routehealth.Table` (in memory, `routehealth.Default`) holds the parks.
2. **Only availability failures park.** Usage window, spend cap, request-rate 429 and 5xx /
   overload park. A 404, an unknown model or an invalid request is a configuration defect and is
   surfaced, never parked: parking would turn a deterministic bug into a flapping availability
   signal. Auth failures are left to the existing login check; unexplained failures do not park.
3. **Scope follows the quota.** Subscription windows and spend caps are account-wide, so they park
   the whole kind. A 429 or 5xx parks one (kind, model).
4. **Duration.** Until the reset the provider named (ignored beyond 8 days, a misread); otherwise
   1 minute doubling to 4 hours per consecutive strike; a spend cap parks 4 hours. A success clears
   the model's park and the kind's. A streak whose last park ended more than 4 hours ago starts over.
5. **Do not switch when recovery is near.** A park ending within 2 minutes does not move a call,
   except a usage window or spend cap whose end is only a backoff guess: those last hours.
6. **Where it applies.**
   - Chat: the send-time choice (`chatRoute`) skips a parked pin and parked order entries. A turn
     that fails with a parking verdict is re-run once on the next usable backend, with the
     provider-context sync carrying the history; a notice (`chat.notice.agent_failover`) records
     the move on success. The conversation's own agent setting is not changed, so the next turn
     goes back once the park ends. A streamed turn is re-run only if no delta or step reached the
     client yet.
   - One-shots: the same skip, and one retry on the next backend. The pin gives way as it does to
     a logged-out CLI.
   - Memory review: a parked pin answers "unavailable" rather than moving the transcript to
     another vendor (0117); no attempt is spent, and the review retries later.
   - When every candidate is parked, the call goes where it went before this change: a refused
     call helps no one.
7. **New conversations are unaffected.** The backend a conversation is created on still comes from
   the priority order and login state alone; pinning a new conversation to a substitute because of
   a limit that lifts in an hour would make the substitute permanent.

## Consequences

- A limit on the first backend no longer fails chat turns or one-shots while another backend is
  connected; the user sees which backend answered.
- Out of scope: CLI sessions (switching mid-session loses context), lcpp (runs only when pinned),
  image generation (has its own ordered fallback without a cooldown), pre-call checks from the
  limit board readings (`limitobs`), and the Settings "currently uses" line, which still ignores
  parks.
- The codex headless usage-limit wording is not measured in the tree; generic markers cover it
  until a real one is captured.
- An Agent restart forgets the table; the next failure re-learns it at the cost of one call.

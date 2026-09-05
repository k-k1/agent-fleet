package agents

// Notification seam for managed-driver state transitions (docs/log/30).
//
// On the TUI route a hook runs `workspace-agent session-status <state>` and package main's
// runSessionStatusHook does the whole thing in one breath: write the status store, then let
// recordSessionNotification emit the "answer ready" notice and the operator completion
// report (docs/log/30). Managed drivers (§3: codex app-server / opencode serve) have no hook
// and wrote status themselves, so nothing consumed the report arm and a finished turn was
// never reported at all.
//
// Drivers live under internal/agents while recordSessionNotification is in package main,
// which Go cannot import. So main registers the notifier here at startup and drivers do the
// status write and the notification together through MarkTurnStart / MarkTurnEnd. Which
// transition counts as completion, and who is told what, stays in the single implementation
// the hook route already uses (recordSessionNotification) — the driver never carries a
// second copy of that judgement.

import (
	"sync/atomic"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// StateNotifier is the notification half of a session state transition, keyed by sid:
// (previous status, new status, excerpt of the turn's output). package main registers
// recordSessionNotification — the same function the hook route calls.
type StateNotifier func(sid, previous, state, excerpt string)

// stateNotifier is registered once at startup (main) but read from driver goroutines
// (codex readLoop / opencode pump), so it goes through atomic.
var stateNotifier atomic.Pointer[StateNotifier]

// SetStateNotifier wires the notifier. main calls it before any driver can run
// (reconcile / app-server start); tests use it to observe transitions.
func SetStateNotifier(fn StateNotifier) {
	if fn == nil {
		stateNotifier.Store(nil)
		return
	}
	stateNotifier.Store(&fn)
}

// notify fires the registered notifier off the caller's goroutine. ASYNC on purpose:
// codex's dispatchNotification runs on the one readLoop goroutine shared by every handle
// (appclient.go), and a notification writes the notice file and POSTs /chat/report (HTTP to
// this same process). Called synchronously, one session's report would stall notification
// delivery for every managed codex session. previous/state are already settled synchronously
// by the caller, so the (previous, state) pair survives whatever order the goroutines run in.
func notify(sid, previous, state, excerpt string) {
	fn := stateNotifier.Load()
	if fn == nil {
		return
	}
	go (*fn)(sid, previous, state, excerpt)
}

// MarkTurnStart records that a managed turn began: status=working (the hook route's
// UserPromptSubmit). A start is never notified — recordSessionNotification ignores working
// as well, but stopping here avoids creating a goroutine for nothing.
func MarkTurnStart(sid string) {
	status.Persist(sid, "working")
}

// MarkTurnEnd records that a managed turn ended: it writes status=idle (the hook route's
// Stop) and emits the completion notification. idle is always written — without it at the end
// of a turn, the WireLive fallback and anySessionWorking stay stuck on "in progress".
//
// For st == TurnUnknown it writes idle but sends no notification: we only lost sight of the
// runtime, the turn may still be running on the other side, and reporting "the answer is
// complete" would be a lie (recovery is the reconcile in §6; an abnormal process exit belongs
// to record-exit / serve.go).
//
// excerpt is empty for managed drivers (there is no streaming capture equivalent to claude's
// MessageDisplay hook). The operator report carries the bare fact with no body excerpt for
// both TUI and managed (docs/log/30), so it is unused on the report route and only rides in
// the body of the full-text bridge (docs/log/37) on TUI. Operators read the detail with
// get_session_output.
func MarkTurnEnd(sid string, st TurnState) { MarkTurnEndErr(sid, st, "") }

// StateFailed is the transition label MarkTurnEndErr hands the notifier for a turn that
// ended in an error. It is NOT a status value — the status store still gets "idle"
// (the session really is back to waiting for input, and WireLive / anySessionWorking depend
// on that). It exists because "finished" and "finished with an error" were indistinguishable
// to every consumer: a provider-side failure was reported to the operator as a completed
// answer, which is how an exhausted balance looked exactly like a finished turn.
const StateFailed = "failed"

// StateAborted is the transition label for a turn that was CUT OFF before it produced
// an answer but can simply be re-run (a dropped connection, a transient rate limit). Like
// StateFailed it is not a status value — the status store still gets "idle". It is separate
// from StateFailed because the operator's next move differs: a failed turn must not be
// re-sent until its cause is fixed, while an aborted one only needs a nudge to
// continue — which is what makes auto-resume of an interrupted turn safe (docs/log/47).
const StateAborted = "aborted"

// StateBlocked is a LIVE WIRE state — unlike StateFailed / StateAborted above it is not a
// notifier label, and unlike "working" / "idle" it is never persisted to the status store.
// It means: the turn is over, but the CLI has parked the pane on a menu that only a human
// keypress clears (so far only claude's usage-limit menu — tmuxx.AtRateLimitModal).
//
// A third state, neither "finished (idle)" nor "running (working)", exists because folding it
// into either of those two does real damage:
//   - folded into working = the original bug. Self-healing never fires, the session stays "in
//     progress" forever, no notification and no completion report are emitted, and the reaper
//     treats it as busy so the container stays awake (measured: about 16 hours).
//   - folded into idle = worse. It looks like it is waiting for input, so the mirror, the
//     operator and scheduled execution send a prompt, and those characters turn straight into
//     selections in the menu (the same misdelivery class as AgentsViewActive).
//
// It is derived from the pane on every poll and never written to the status store: a menu is
// cleared by a human, and once it is gone the next poll can simply return an ordinary idle —
// no separate channel is needed to learn that it disappeared.
const StateBlocked = "blocked"

// StateAuth is the same kind of live-only wire state as StateBlocked, for the one cause
// that no keypress in the pane can clear: the workspace's claude login has expired
// (docs/log/47 §4-8 — both the refresh and the access credential are past their expiry).
//
// It is kept apart from StateBlocked because the next move to prompt the user for is the
// opposite: a usage limit says "wait", an expired login says "re-authenticate now". Folded
// into the same stopped badge it reads as something waiting will fix — an expired login never
// fixes itself.
//
// It must not be folded into idle for the same reason as StateBlocked, and here the damage
// was worse: it looks like it is waiting for input, so the mirror, the operator and scheduled
// execution send a prompt, the TUI accepts it and not a single turn starts (the send looks
// successful and the mirror is left holding a prompt that never lands — user report
// 2026-08-14).
//
// It is a per-workspace fact (one credential per container), so every claude session claims
// it at once. It is not written to the status store — once re-authenticated the next poll can
// simply return the ordinary state, and no separate channel is needed to learn it disappeared
// (same shape as StateBlocked).
const StateAuth = "auth"

// StateLimited is the third live-only wire state (same shape as StateBlocked / StateAuth):
// a session whose turn was cut off by a usage limit and that can only wait for the reset
// time. No menu is up (claude has already dismissed the account-window menu on its own, and a
// per-model limit never shows a menu at all; on codex managed the turn simply failed with
// usageLimitExceeded), so the pane is back at its ordinary waiting prompt.
//
// It is kept apart from StateBlocked not because it would otherwise look like "waiting for
// input", but because the next move to prompt the user for differs: blocked means "choose in
// the pane (nothing moves until a human clears it)", this one means "wait (it moves by itself
// once the time comes, and resumes automatically if the reservation in docs/log/47 §4-4
// exists)". Folded into idle instead, both the fact that the turn died on a limit and the
// planned resume vanish from the screen, and the list cannot tell it apart from a session
// that finished normally (user report 2026-08-19).
//
// Unlike blocked it does not block sending. The pane accepts characters, and recovering from
// a per-model limit is a `/model` switch or `/usage-credits` — that is, input to that very
// session — so refusing here would close off the only way out (promptBlocker reads the status
// store, so not writing this state gets that behaviour for free).
//
// It is not written to the status store — when the limit lifts and the next turn runs, the
// tail of the transcript changes and the next poll can return the ordinary state (same shape
// as StateBlocked / StateAuth).
const StateLimited = "limited"

// StateSpendLimit is StateLimited's counterpart for the limit that waiting never clears: a
// spend or balance cap (measured 2026-08-20 — "You've hit your org's monthly spend limit ·
// run /usage-credits to raise it, or visit claude.ai/admin-settings/usage").
//
// It arrives as the same 429 / `error:"rate_limit"` and its transcript record is
// indistinguishable, so the wording is the only material there is (claude.LimitSpend). It is
// still a separate state because showing "waiting for the limit to lift" makes the user wait
// for a reset that never comes — it does not lift, and it takes a billing-side move, raising
// the cap or adding credit. No auto-resume is wired up either (it would only hit the same
// 429).
//
// Since the move to prompt is one a human must make now, the chip uses the same warning
// colour as blocked / auth. It is not written to the status store (same shape as StateBlocked
// / StateAuth / StateLimited).
const StateSpendLimit = "spend_limit"

// MarkTurnEndErr is MarkTurnEnd carrying the reason a turn failed. failure is the
// one-line summary the driver built (empty for a clean turn); it rides the notifier's
// excerpt so the operator report can say the turn errored and the chat bridge can post
// the reason. Drivers that don't yet distinguish failures keep calling MarkTurnEnd.
func MarkTurnEndErr(sid string, st TurnState, failure string) {
	previous, _ := status.Read(sid)
	if st == TurnUnknown {
		// We only lost sight of the runtime. idle is written (so nothing sticks on "in
		// progress"), but this is not the end of a turn, so TurnEnd is not raised: if the
		// level-based report reconciler (docs/log/51) counted this idle as evidence of
		// completion, it would report a turn that may still be running as finished.
		status.Persist(sid, "idle")
		return
	}
	status.PersistTurnEnd(sid, "idle")
	if st == TurnFailed {
		notify(sid, previous.State, StateFailed, failure)
		return
	}
	if st == TurnAborted {
		notify(sid, previous.State, StateAborted, failure)
		return
	}
	notify(sid, previous.State, "idle", "")
}

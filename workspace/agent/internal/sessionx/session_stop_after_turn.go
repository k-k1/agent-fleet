package sessionx

// The "stop after this turn" arm (docs/log/85): a session folds itself away once the turn it
// is running ends, so an unattended run does not sit there holding the workspace awake.
//
// Three things are deliberately separate here.
//
//   - ARMING is what the user reaches: the MCP tool af_stop_after_turn when they said it in
//     prose to the session, this handler when they pressed it in the Console. Both write the
//     same instant into the meta and nothing else.
//   - DECIDING that the turn ended is not done here at all. The report reconciler
//     (chatx/chat_report_reconcile.go) already reads that level — end-of-turn marker, pending
//     question / plan / permission, subagent, transcript growth, pane spinner, interruptions
//     held by auto-resume — and a second detector next to it would answer differently the
//     first time either one changed (the shape of the docs/log/75 busy-state drift).
//   - STOPPING is haltSessionMeta, the same path the Console's halt button takes, so the
//     carry-over promotion and the graceful-stop ordering cannot be missed here.
//
// Never stop inside the tool call itself. The call runs INSIDE the turn: killing the pane
// there loses the answer that is still being written, the tool result never returns, and the
// resume that follows costs a whole turn (docs/log/75 §75.10). The arm is the answer to that
// — it makes the stop happen after the end of the turn instead of instead of it.

import (
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stopArmVisible is the arm as the wire shows it: empty once it has expired, so a row can
// never claim a session is about to stop when nothing will act on it any more. The same
// treatment the keep-awake pin gets on the render side.
func stopArmVisible(m session.Meta) string {
	if _, live := session.StopArmedAt(m, time.Now()); !live {
		return ""
	}
	return m.StopAfterTurnAt
}

// HandleSessionStopAfterTurn (POST /sessions/{name}/stop-after-turn {"on":true|false}) arms
// or releases the one-shot self-stop. Arming again while armed re-stamps the instant, which
// is also how the expiry is extended.
func HandleSessionStopAfterTurn(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	m, ok := setStopArm(name, req.On)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "stopAfterTurnAt": stopArmVisible(m)})
}

// setStopArm writes (on) or drops (off) the arm. It takes the same mutex as the deletion lock
// and the keep-awake pin: GET /sessions writes StoppedAt back as a side effect, and without
// serializing, a list that started before this call would put the old meta back and the arm
// would vanish with no error anywhere.
func setStopArm(name string, on bool) (session.Meta, bool) {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		return session.Meta{}, false
	}
	if on {
		m.StopAfterTurnAt = time.Now().Format(time.RFC3339)
	} else {
		m.StopAfterTurnAt = ""
	}
	session.WriteMeta(m)
	return m, true
}

// clearStopArm drops the arm from the meta on disk and returns the meta as written. Used by
// every path that consumes or invalidates it (the halt itself, a new instruction).
func clearStopArm(m session.Meta) session.Meta {
	if m.StopAfterTurnAt == "" {
		return m
	}
	if written, ok := setStopArm(m.Name, false); ok {
		return written
	}
	m.StopAfterTurnAt = ""
	return m
}

// cancelStopArmOnNewPrompt releases the arm when a new prompt is delivered to the session.
//
// "Stop when you are done" is said about the work in flight. Anything typed or injected
// afterwards is work the user did not include in that sentence, so honouring the old arm
// would fold the session away in the middle of it. Cancelling is also the safe direction of
// the two: the cost of being wrong is a session that keeps running (visible, one press away
// from stopping) rather than one that vanished mid-instruction.
//
// Answers to a modal (keys / seq, an Interaction reply, a carried answer) deliberately do NOT
// come through here: they continue the armed turn rather than starting new work.
func cancelStopArmOnNewPrompt(name string) {
	m, ok := session.ReadMeta(name)
	if !ok || m.StopAfterTurnAt == "" {
		return
	}
	clearStopArm(m)
}

// StopArmedSession halts a session whose arm has come due. Called by the report reconciler,
// which owns the "the turn has ended" decision; this side owns only the folding.
//
// The order is halt first, clear second. Clearing first and then failing to kill the pane
// would leave a session that was told to stop still running with nothing left to make it try
// again; this way a failed halt keeps the arm and the next sweep retries.
func StopArmedSession(name string) error {
	m, ok := session.ReadMeta(name)
	if !ok {
		return nil // the session is gone; nothing to stop and nothing to report
	}
	if _, live := session.StopArmedAt(m, time.Now()); !live {
		return nil // released or expired between the decision and here
	}
	halted, err := haltSessionMeta(m) // consumes the arm itself, on every one of its exits
	if err != nil {
		return err
	}
	notifyStopArmFired(halted)
	return nil
}

// notifyStopArmFired tells the notification centre that a session stopped itself.
//
// Without it the stop is only visible to someone already looking at the session list: the
// arm is usually set from inside the conversation, and the user who asked for it is, by the
// nature of the request, not watching. The stop is resumable, so what the message has to
// carry is that it happened and that the session can be picked back up.
func notifyStopArmFired(m session.Meta) {
	ev := notice.New("stop-after-turn", m.Name, m.Kind, session.Display(m))
	_ = notice.Put(ev)
}

package sessionx

// The move target's first prompt (ADR 0115 decision 1): the move is complete for the target only
// once it has been told where its file is. The create sends that prompt after the target's meta
// is written, synchronously on Managed and from a goroutine on Terminal, so an Agent that stops
// in between leaves a target that exists, holds the source's conversation in its directory, and
// was never pointed at it. RecoverPendingInitialPrompts then reads it as unknown.
//
// So the prompt is kept here, by target, from before the create until the target's
// InitialPromptState says delivered. The move's answer and the source's preview report it while
// it is not, and POST /sessions/{target}/move/deliver sends it again on the user's word. It is
// never resent by itself: an unknown delivery may have arrived, and a second copy is the user's
// call to make, not the Agent's.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type moveDelivery struct {
	Source string `json:"source"`
	File   string `json:"file"`
	Prompt string `json:"prompt"`
}

// moveDeliverInflight counts the background deliveries /deliver started; tests wait on it so a
// delivery cannot outlive the test (and its HOME). Production never waits.
var moveDeliverInflight sync.WaitGroup

var moveDeliveries = fstore.JSON[moveDelivery](paths.AgentStateDir, "session-move-delivery", ".json")

// moveDeliveryWire is a target whose first prompt is not known to have arrived.
type moveDeliveryWire struct {
	Target string `json:"target"`
	File   string `json:"file"`
	// InitialPromptState is the target's: pending (being sent now), failed, unknown (sent, no
	// evidence either way; also every pending a restart found), or "" (no state recorded).
	InitialPromptState string `json:"initialPromptState"`
	// Deliverable: the deliver step may send it now. For unknown it may arrive a second time.
	Deliverable bool `json:"deliverable"`
}

// moveDeliveryState answers for target, or nil when its prompt arrived (the record is dropped
// then) or there is nothing to answer for. A target whose meta file is gone drops the record
// too; one whose meta does not read right now keeps it.
func moveDeliveryState(target string) *moveDeliveryWire {
	d, ok := moveDeliveries.Read(target)
	if !ok {
		return nil
	}
	var m session.Meta
	WithSessionMetaLock(func() { m, ok = session.ReadMeta(target) })
	if !ok {
		// No meta yet is not the same as a target deleted: the record is written before the
		// create, so while the source's move is still on its way to this target it stays.
		if _, err := os.Lstat(session.MetaPath(target)); errors.Is(err, fs.ErrNotExist) && !moveToward(d.Source, target) {
			moveDeliveries.Remove(target)
		}
		return nil
	}
	st := m.InitialPromptState
	if st == session.InitialPromptDelivered {
		moveDeliveries.Remove(target)
		return nil
	}
	return &moveDeliveryWire{Target: target, File: d.File, InitialPromptState: st,
		Deliverable: st != session.InitialPromptPending}
}

// moveToward reports that source's move to target has not ended: source is moving to it, or
// the record of that move is still there.
func moveToward(source, target string) bool {
	if !session.ValidName(source) {
		return false
	}
	if m, ok := session.ReadMeta(source); ok && m.Moving == target {
		return true
	}
	rec, ok := moveRecords.Read(source)
	return ok && rec.Target == target
}

// typeMoveDelivery types a Terminal target's first prompt and answers how it ended, as the
// create's own delivery does: readiness wait, the Enter nudge, and delivered only on evidence
// that a turn started. A variable so tests need no tmux.
var typeMoveDelivery = typeInitialPrompt

type moveDeliverWire struct {
	Target             string `json:"target"`
	InitialPromptState string `json:"initialPromptState"`
	// MayDuplicate: the earlier delivery's outcome was unknown, so this may be a second copy.
	MayDuplicate bool `json:"mayDuplicate,omitempty"`
}

// HandleSessionMoveDeliver sends a move target its first prompt again, when the earlier delivery
// failed or its outcome is unknown. POST /sessions/{name}/move/deliver, on the target.
func HandleSessionMoveDeliver(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	unlock, ok := moveOpLock(name)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "move_busy", "a delivery to this session is already running")
		return
	}
	defer unlock()
	d, ok := moveDeliveries.Read(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "move_delivery_none", "this session has no move prompt to deliver")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		if moveToward(d.Source, name) {
			httpx.WriteErr(w, http.StatusConflict, "move_target_not_created",
				"the move to this session has not created it yet; finish or cancel the move from "+d.Source)
			return
		}
	}
	st := moveDeliveryState(name)
	switch {
	case st == nil:
		httpx.WriteErr(w, http.StatusConflict, "move_delivery_done", "the first prompt has already arrived, or the session is gone")
		return
	case !st.Deliverable:
		httpx.WriteErr(w, http.StatusConflict, "move_delivery_in_flight", "the first prompt is being delivered now")
		return
	}
	prev := st.InitialPromptState
	UpdateSessionMeta(name, func(m *session.Meta) bool {
		m.InitialPromptState = session.InitialPromptPending
		return true
	})
	answer := moveDeliverWire{Target: name, MayDuplicate: prev == session.InitialPromptUnknown}
	if m.DriverKind() != session.DriverManaged {
		// Typed into a pane, a 200 from /input says only that the keys went in: a booting CLI
		// eats them, or the Enter, without a trace. So the create's own delivery runs, in the
		// background as on create (its readiness wait can take a minute), and the record stays
		// until it reports delivered.
		moveDeliverInflight.Add(1)
		go func() {
			defer moveDeliverInflight.Done()
			state := typeMoveDelivery(name, d.Prompt)
			// Settle first: the record is the retry handle until the state says delivered is on
			// disk (ADR 0115).
			settleInitialPrompt(name, state)
			if state == session.InitialPromptDelivered {
				moveDeliveries.Remove(name)
			}
		}()
		answer.InitialPromptState = session.InitialPromptPending
		httpx.WriteJSON(w, http.StatusAccepted, answer)
		return
	}
	// Managed: the create's own delivery counts an accepted Send as delivered, and /input's 2xx
	// is that acceptance.
	body, _ := json.Marshal(map[string]string{"prompt": d.Prompt})
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/sessions/"+name+"/input", bytes.NewReader(body))
	req.SetPathValue("name", name)
	c := &captureWriter{header: http.Header{}}
	HandleSessionInput(c, req)
	if c.status < 200 || c.status >= 300 {
		settleInitialPrompt(name, session.InitialPromptFailed)
		httpx.WriteJSON(w, c.status, json.RawMessage(c.body.Bytes()))
		return
	}
	settleInitialPrompt(name, session.InitialPromptDelivered)
	moveDeliveries.Remove(name)
	answer.InitialPromptState = session.InitialPromptDelivered
	httpx.WriteJSON(w, http.StatusOK, answer)
}

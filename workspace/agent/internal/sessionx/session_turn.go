package sessionx

// Semantic endpoints for session operations (docs/log/27 P1.5/P2). The Console's chat
// send / steer / interrupt arrive as turn/start, turn/steer and turn/interrupt, and answers
// to questions arrive as Interaction replies (§5). A managed-driver session goes through
// driverOf → ThreadHandle; a tui (legacy) session is delegated to the existing tmux path —
// the Console makes the same call without knowing which driver it is talking to. /input
// (raw keys/seq) stays for driving TUI modals on the CLI route: /turn is the semantic
// interface, /input the raw one.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// managedDrivers is the kind → managed Driver registry (docs/log/27 §3). tui drivers are
// deliberately absent: they implement no ThreadHandle, and the /turn handler delegates them
// straight to the tmux path.
var managedDrivers = map[string]agents.Driver{
	session.KindOpencode: opencode.NewDriver(),
	session.KindCodex:    codex.NewDriver(),
	session.KindCopilot:  copilot.NewDriver(),
	session.KindCursor:   cursor.NewDriver(),
	session.KindKiro:     kiro.NewDriver(),
}

// driverOf resolves the managed driver for a session's kind.
func driverOf(m session.Meta) (agents.Driver, bool) {
	d, ok := managedDrivers[m.Kind]
	return d, ok
}

// turnReq is the wire body of POST /sessions/{name}/turn.
type turnReq struct {
	Op     string `json:"op"` // "start" | "steer" | "interrupt"
	Prompt string `json:"prompt"`
	// Attachments holds absolute paths of files to attach to this turn (docs/log/27 §10).
	// Only a managed driver interprets them (TurnInput.Attachments → API attachment); for tui
	// the Console weaves the paths into the prompt body itself, so this is accepted and ignored.
	Attachments []string `json:"attachments"`
	// ClientMessageID is an AF-assigned idempotency key (docs/log/27 §4) that lets a managed
	// driver's ledger dedupe resends. Empty means the driver assigns one.
	ClientMessageID string `json:"clientMessageID"`
}

// HandleSessionTurn (POST /sessions/{name}/turn) applies a semantic turn operation
// to a session. On tui, start and steer both collapse into the same type+submit (the TUI
// itself queues input aimed at a running turn); on managed they reach ThreadHandle.Send and
// Steer (opencode queues inside the driver and submits the steer as the next turn once the
// current one finishes).
func HandleSessionTurn(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req turnReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Op != "start" && req.Op != "steer" && req.Op != "interrupt" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_op", "op must be start, steer or interrupt")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() == session.DriverManaged {
		handleManagedTurn(w, meta, req)
		return
	}
	// tui route: delegate to the existing tmux path. The guards (not_running / no_pane /
	// question_pending) must stay identical to those on /input's {prompt} path.
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		httpx.WriteErr(w, http.StatusConflict, "not_running", "session is not running; start it first")
		return
	}
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		httpx.WriteErr(w, http.StatusInternalServerError, "no_pane", "could not resolve session pane")
		return
	}
	if req.Op == "interrupt" {
		// The chat stop button. In opencode's subagent detail view Escape turns into a
		// navigation key, so prefix Up as /input's Escape special case does.
		keys := []string{"Escape"}
		if opencodeInSubagentView(tn) {
			keys = []string{"Up", "Escape"}
		}
		if err := sendNamedKeys(pane, keys); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
		}
		// Escape starts no turn, so do not mark working (same reason as /input: no Stop
		// hook fires and the session would stick at "working").
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name, "op": req.Op})
		return
	}
	// start / steer go through the same delivery as /input's {prompt} path (submitPromptTUI),
	// guards included (question_pending, and slash commands do not mark working). A steer
	// misanswers an open modal by the same route, so sharing the guards is correct.
	if strings.TrimSpace(req.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required for start/steer")
		return
	}
	if !submitPromptTUI(w, name, pane, req.Prompt) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": name, "op": req.Op})
}

// handleManagedTurn routes a turn op to the session's ThreadHandle (docs/log/27 P2).
// Resume is idempotent — it returns the live handle if there is one — so calling it every
// time is safe, and it also brings the runtime back up on a halted → send sequence.
func handleManagedTurn(w http.ResponseWriter, meta session.Meta, req turnReq) {
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	switch req.Op {
	case "interrupt":
		if err := h.Interrupt(); err != nil {
			writeRuntimeErr(w, err)
			return
		}
	default: // start / steer
		if strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required for start/steer")
			return
		}
		in := agents.TurnInput{Prompt: req.Prompt, Attachments: req.Attachments, ClientMessageID: req.ClientMessageID}
		if req.Op == "steer" {
			err = h.Steer(in)
		} else {
			err = h.Send(in)
		}
		if err != nil {
			if errors.Is(err, agents.ErrQuestionPending) {
				// Same wire contract as tui's submitPromptTUI: the Console uses this code
				// to undo the optimistic echo and steer the user to the question card.
				// The sentinel is shared across drivers.
				httpx.WriteErr(w, http.StatusConflict, "question_pending",
					"a question is awaiting an answer; answer it via the question card, not free text")
				return
			}
			writeRuntimeErr(w, err)
			return
		}
		// The optimistic working mark has the same motive as on tui: a poll right after the
		// send must not read a stale idle.
		markSessionWorking(meta.Name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": meta.Name, "op": req.Op})
}

// HandleSessionRespond (POST /sessions/{name}/respond) answers a pending
// Interaction by id (docs/log/27 §5 — the general form of approvals and questions). This
// semantic route is for managed drivers only: a tui session's question is still answered
// through /input keys/seq, with the Console navigating the TUI modal, because a TUI modal
// offers no way to answer by id.
func HandleSessionRespond(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var reply agents.InteractionReply
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&reply); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if reply.ID == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_interaction", "interaction id is required")
		return
	}
	switch reply.Decision {
	case agents.DecisionAllow, agents.DecisionDeny, agents.DecisionCancel, agents.DecisionAnswer:
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_decision", "decision must be allow, deny, cancel or answer")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "respond_unsupported",
			"tui セッションの質問は keys/seq（/input）で TUI モーダルを駆動して答えます")
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	if err := h.Respond(reply); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "respond_failed", err.Error())
		return
	}
	// An answer resumes the turn, so mark working optimistically for the same reason tui does
	// after sending Enter.
	markSessionWorking(name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"responded": name, "id": reply.ID})
}

// settingsReq is the wire body of POST /sessions/{name}/settings — a dynamic ThreadSettings
// update (docs/log/27 §9.4-3, managed only). An empty field means "leave unchanged". Mode
// switching on tui still goes through an /input key (planCycleKey).
type settingsReq struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Mode        string `json:"mode"` // "plan" | "normal"
	ClearModel  bool   `json:"clearModel"`
	ClearEffort bool   `json:"clearEffort"`
}

// HandleSessionSettingsGet reports the settings that the managed driver will use
// for the next turn. Reading the latest transcript turn is insufficient: it is stale
// immediately after a settings change and empty before the first prompt.
func HandleSessionSettingsGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "settings_unsupported",
			"tui セッションの設定はターミナルで変更します")
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	snap, err := h.Snapshot()
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	caps := d.Capabilities()
	httpx.WriteJSON(w, http.StatusOK, managedThreadSettingsWire{
		Model: snap.Settings.Model, Effort: snap.Settings.Effort, Mode: snap.Settings.Mode,
		DynamicModel: caps.DynamicModel, DynamicEffort: caps.DynamicEffort, DynamicMode: caps.DynamicMode,
	})
}

// HandleSessionSettings (POST /sessions/{name}/settings) updates a managed
// session's dynamic thread settings: changing model, effort or mode on a running session,
// which managed drivers make possible for the first time (§9.4-3).
func HandleSessionSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req settingsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Mode != "" && req.Mode != "plan" && req.Mode != "normal" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return
	}
	if req.Model == "" && req.Effort == "" && req.Mode == "" && !req.ClearModel && !req.ClearEffort {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_settings", "nothing to update")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() != session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "settings_unsupported",
			"tui セッションの設定はターミナル（/input のキー操作）で切り替えます")
		return
	}
	// A model change on a running session is refused exactly as launch is (model_deny.go).
	// The session's current model is left alone: the hidden-model setting stops a model from
	// being chosen from now on, it does not roll back work already under way.
	if ModelHidden(meta.Kind, req.Model) {
		httpx.WriteErr(w, http.StatusBadRequest, "model_hidden", hiddenModelError(strings.TrimSpace(req.Model)))
		return
	}
	d, ok := driverOf(meta)
	if !ok {
		httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
			"managed driver はこの kind ではまだ利用できません")
		return
	}
	h, err := d.Resume(meta)
	if err != nil {
		writeRuntimeErr(w, err)
		return
	}
	patch := agents.ThreadSettings{
		Model: req.Model, Effort: req.Effort, Mode: req.Mode,
		ClearModel: req.ClearModel, ClearEffort: req.ClearEffort,
	}
	if err := h.UpdateSettings(patch); err != nil {
		writeRuntimeErr(w, err)
		return
	}
	// Persist the desired next-turn settings only after the native update succeeds.
	// This is especially important for opencode, whose driver owns variant/model state
	// in memory because serve has no thread-settings persistence endpoint.
	if req.ClearModel {
		meta.Model = ""
	} else if req.Model != "" {
		meta.Model = req.Model
	}
	if req.ClearEffort {
		meta.Effort = ""
	} else if req.Effort != "" {
		meta.Effort = req.Effort
	}
	if req.Mode != "" {
		meta.Mode = req.Mode
	}
	session.WriteMeta(meta)
	snap, _ := h.Snapshot()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"updated": name, "model": snap.Settings.Model, "effort": snap.Settings.Effort, "mode": snap.Settings.Mode,
	})
}

// managedThreadSettingsWire is the response of GET /sessions/{name}/settings (the Console's
// `ManagedThreadSettings`, console/src/core/api/client.ts).
//
// was: map[string]any{"model":…, "effort":…, "mode":…, "dynamicModel":…,
//
//	"dynamicEffort":…, "dynamicMode":…}
//
// All six keys are unconditional, so none of them may take omitempty. model, effort and
// mode can legitimately be empty (unset = follow the CLI default), and omitempty would drop
// the key entirely, changing how the Console shows "unset".
type managedThreadSettingsWire struct {
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	Mode          string `json:"mode"`
	DynamicModel  bool   `json:"dynamicModel"`
	DynamicEffort bool   `json:"dynamicEffort"`
	DynamicMode   bool   `json:"dynamicMode"`
}

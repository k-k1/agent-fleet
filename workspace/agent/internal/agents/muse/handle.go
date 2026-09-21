package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// callTimeout bounds a command's ACKNOWLEDGEMENT, never its outcome. Every MSP command is
// admission-only — `turn/start` answers "accepted" and the turn runs on afterwards — so this
// is a liveness bound on the host, not a limit on how long a turn may take.
const callTimeout = 30 * time.Second

// clientVersion is the version AF reports in the MSP handshake. It names this DRIVER's
// contract with the protocol, not the Agent build — the same choice codex's app-server client
// already makes — so it moves when the driver's wire behaviour does, and a rebuild does not
// churn what the host records about its clients.
const clientVersion = "1"

// threadHandle is one session's `muse serve` child and its MSP connection.
type threadHandle struct {
	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle

	// bypass is the launch-time permission choice. It selects the session's approval mode and
	// is resolved on every Resume rather than carried in ThreadSettings, where "empty means
	// unchanged" cannot express a three-valued bool.
	bypass bool

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cl     *msp.Client
	sid    string // the muse session id (the UUIDv7 AF minted)
	path   string // the session.jsonl session/start reported
	alive  bool
	state  agents.TurnState
	turnID string // the running turn, for steer's expectedTurnId

	running  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction
	pending  *pendingAsk // what inter is waiting on, in muse's own vocabulary
	events   chan agents.Event
}

// pendingAsk is the wire identity of the thing an Interaction is standing in for. Two
// channels arrive as separate notifications and are answered by separate commands, so the
// handle remembers which one it is holding rather than guessing from the Interaction.
type pendingAsk struct {
	// approvalID and requirement are set for an approval. requirement is echoed back
	// verbatim: it is the multi-stage race guard.
	approvalID  string
	requirement msp.ApprovalRequirementRef
	// allowChoice and abortChoice are the two choice ids onRequest mode offers.
	allowChoice string
	abortChoice string

	// userInputID and questions are set for a user-input prompt.
	userInputID string
	questions   []msp.UserInputQuestion
}

func (p *pendingAsk) isApproval() bool { return p != nil && p.approvalID != "" }

// --- spawn -------------------------------------------------------------------

// spawn starts the host, runs the handshake and starts or reloads the muse session.
func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	cmd := exec.Command(Bin(), serveArgs()...)
	cmd.Dir = h.dir
	cmd.Env = childEnv(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The host's own diagnostics go to stderr; keep them out of the Agent's log but do not
	// let a full pipe buffer block the child.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("muse serve の起動に失敗しました: %w", err)
	}

	cl := msp.NewClient(stdin, stdout, msp.Handler{
		OnNotification: h.onNotify,
		OnRequest:      h.onRequest,
	})

	res, err := msp.Handshake(cl, clientVersion, []msp.CapabilityName{msp.CapabilityNameSessionMCP})
	if err != nil {
		stopChild(cmd, stdin)
		return fmt.Errorf("Muse Code との接続に失敗しました: %w", err)
	}
	// A drifted fingerprint is a warning, not a refusal: the schema says so, and refusing to
	// launch because a patch release re-rendered the bundle would be worse than decoding the
	// parts that did not move. The build-time lock is msp's fingerprint test; this line is what
	// explains a decode failure that follows.
	if d := msp.SchemaDrift(res); d != "" {
		log.Printf("muse: %s: %s", h.name, d)
	}

	h.mu.Lock()
	h.cmd, h.stdin, h.cl = cmd, stdin, cl
	h.settings = st
	h.mu.Unlock()

	if err := h.openSession(cl, st); err != nil {
		stopChild(cmd, stdin)
		h.mu.Lock()
		h.cmd, h.stdin, h.cl = nil, nil, nil
		h.mu.Unlock()
		return err
	}

	h.mu.Lock()
	h.alive = true
	h.state = agents.TurnCompleted
	h.mu.Unlock()
	go h.watch(cmd, cl)
	return nil
}

// openSession reloads this slot's conversation, or starts one when it has none.
//
// The id is stored rather than derived: MSP refuses a retained or reserved id with
// `session_id_conflict`, so AF's usual deterministic UUIDv5 would work exactly once
// (decision 4). A stored id whose session the host no longer knows falls back to a fresh
// start rather than leaving the slot dead — losing the history is bad, refusing to launch is
// worse, and the old session.jsonl is still on disk either way.
func (h *threadHandle) openSession(cl *msp.Client, st agents.ThreadSettings) error {
	if prev, ok := readSession(h.slotSid); ok && prev.ID != "" {
		err := cl.CallInto(msp.MethodSessionResume, msp.SessionResumeParams{
			CommandID: msp.NewCommandID(),
			SessionID: prev.ID,
		}, callTimeout, nil)
		if err == nil {
			h.mu.Lock()
			h.sid, h.path = prev.ID, prev.Path
			h.mu.Unlock()
			return nil
		}
		if !msp.HasCode(err, msp.ErrCodeSessionNotFound) {
			return fmt.Errorf("Muse Code セッションの再開に失敗しました: %w", err)
		}
		log.Printf("muse: %s: stored session %s is gone; starting a fresh one", h.name, prev.ID)
	}

	sid := msp.NewCommandID()
	params := msp.SessionStartParams{
		CommandID:     msp.NewCommandID(),
		SessionID:     &sid,
		WorkspaceRoot: &h.dir,
		ApprovalMode:  approvalModeFor(h.bypass),
	}
	if st.Model != "" {
		params.ModelID = &st.Model
	}
	var res msp.SessionStartResult
	if err := cl.CallInto(msp.MethodSessionStart, params, callTimeout, &res); err != nil {
		return fmt.Errorf("Muse Code セッションの開始に失敗しました: %w", err)
	}
	h.mu.Lock()
	h.sid, h.path = res.Session.SessionID, res.Session.Path
	h.mu.Unlock()
	writeSession(h.slotSid, museSession{ID: res.Session.SessionID, Path: res.Session.Path})
	return nil
}

// approvalModeFor maps the launch-time permission choice onto the session's approval mode.
// The sandbox is off (decision 5), so this gate is the only thing between the agent and the
// container — which is why "skip permissions" selects allowAll explicitly rather than by
// omission: the server default is onRequest, and a silent default is not a choice a member made.
func approvalModeFor(bypass bool) *msp.ApprovalMode {
	m := msp.ApprovalModeOnRequest
	if bypass {
		m = msp.ApprovalModeAllowAll
	}
	return &m
}

// watch turns a dead child into a dead handle. Without it a crashed host leaves the session
// reading as live with nothing behind it, and the next Send blocks until its own timeout.
func (h *threadHandle) watch(cmd *exec.Cmd, cl *msp.Client) {
	<-cl.Closed()
	_ = cmd.Wait()
	h.mu.Lock()
	if h.cl != cl {
		h.mu.Unlock() // already replaced by a newer spawn
		return
	}
	wasRunning := h.running
	h.alive, h.running, h.cl = false, false, nil
	h.mu.Unlock()
	if wasRunning {
		// A turn cut off by a dead host is aborted, not failed: a resend fixes it, and the
		// two call for opposite actions from the operator.
		agents.MarkTurnEnd(h.slotSid, agents.TurnAborted)
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnAborted})
	}
}

// --- notifications -----------------------------------------------------------

// onNotify runs on the client's read goroutine and must never block.
//
// The declared notification table is a decode map, not an allow-list: the host emits
// `session/started` before the `session/start` response and that name is not in the schema at
// all, so an unknown method is dropped rather than treated as a protocol error.
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	switch method {
	case msp.NotificationTurnStarted:
		var p msp.TurnStartedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.mu.Lock()
		h.turnID, h.running = p.TurnID, true
		h.state = agents.TurnRunning
		h.mu.Unlock()
		agents.MarkTurnStart(h.slotSid)
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})

	case msp.NotificationTurnCompleted:
		var p msp.TurnCompletedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.finishTurn(p)

	case msp.NotificationSessionStatusChanged:
		var p msp.SessionStatusChangedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onStatus(p)

	case msp.NotificationApprovalRequested:
		var p msp.ApprovalRequestParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onApproval(p)

	case msp.NotificationApprovalResolved:
		h.clearAsk(func(p *pendingAsk) bool { return p.isApproval() })

	case msp.NotificationUserInputRequested:
		var p msp.UserInputRequestParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onUserInput(p)

	case msp.NotificationUserInputSettled:
		h.clearAsk(func(p *pendingAsk) bool { return !p.isApproval() })
	}
}

// onRequest answers the server-initiated form of the two must-answer prompts.
//
// Measured, a real host delivers both as NOTIFICATIONS and this form may never arrive at all;
// answering only this one leaves the turn parked on approvalPending forever. The receipt
// acknowledges presentation and changes no state — the decision travels as its own command —
// so it is safe to send here and let the notification path build the Interaction.
func (h *threadHandle) onRequest(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case msp.ServerRequestApprovalRequest:
		var p msp.ApprovalRequestParams
		if json.Unmarshal(params, &p) == nil {
			h.onApproval(p)
		}
	case msp.ServerRequestUserInputRequest:
		var p msp.UserInputRequestParams
		if json.Unmarshal(params, &p) == nil {
			h.onUserInput(p)
		}
	}
	h.mu.Lock()
	cl := h.cl
	h.mu.Unlock()
	if cl != nil {
		_ = cl.Respond(id, msp.RequestReceipt{})
	}
}

// onStatus maps the session's load state onto the turn state machine. It is the contract
// every other kind has to scrape for: running / idle / notLoaded, plus the attention flags
// that say the host is waiting on a human.
func (h *threadHandle) onStatus(p msp.SessionStatusChangedParams) {
	waiting := false
	for _, a := range p.Attention {
		if a == msp.AttentionFlagApprovalPending || a == msp.AttentionFlagInputPending {
			waiting = true
		}
	}
	h.mu.Lock()
	switch {
	case waiting:
		h.state = agents.TurnWaitingInteraction
	case p.Status == msp.SessionStatusRunning:
		h.state = agents.TurnRunning
		h.running = true
	case p.Status == msp.SessionStatusIdle:
		// Idle closes a turn only when one was running; a session sitting idle from the
		// start has no turn to end.
		if h.running {
			h.running = false
			h.state = agents.TurnCompleted
		}
	case p.Status == msp.SessionStatusNotLoaded:
		h.state = agents.TurnUnknown
	}
	st := h.state
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

func (h *threadHandle) finishTurn(p msp.TurnCompletedParams) {
	st := agents.TurnCompleted
	switch p.Terminal {
	case msp.TurnTerminalFailed:
		st = agents.TurnFailed
	case msp.TurnTerminalCancelled:
		st = agents.TurnCancelled
	}
	// A turn that ended only because the member is not signed in is not a failure a resend
	// cannot fix: it is aborted, so the operator is told to fix the credential and nudge it
	// rather than being shown a completed answer that never existed.
	failure := ""
	if p.Error != nil {
		failure = p.Error.Message
		if p.Error.Kind == museAuthRequired {
			st = agents.TurnAborted
		}
	}
	h.mu.Lock()
	h.running, h.state, h.turnID = false, st, ""
	h.mu.Unlock()
	agents.MarkTurnEndErr(h.slotSid, st, failure)
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
	h.pump()
}

// museAuthRequired is the error kind a turn ends with when no credential is configured. It is
// the failure a fresh deployment hits first, and it is recoverable by signing in, so it must
// not read as a completed turn.
const museAuthRequired = "authRequired"

// --- approvals and questions -------------------------------------------------

// onApproval turns a pending approval into an Interaction.
//
// It is built as the QUESTION kind, the same reuse kiro's ACP session/request_permission and
// lcpp's own approval gate already make, because AF has no approval Interaction kind yet:
// agents.Interaction.Kind still documents "approval" as future work and nothing reads that
// vocabulary. The cost is named rather than hidden — the wire carries toolName, rawArgs,
// judgeEscalated, protectedWrite and the parsed argv of every stage, and folding it into two
// labelled options discards all but the command line. Building the real kind is ADR 0095
// decision 13's own work package.
func (h *threadHandle) onApproval(p msp.ApprovalRequestParams) {
	ask := &pendingAsk{approvalID: p.ApprovalID, requirement: p.CurrentRequirementID}
	for _, c := range p.AvailableChoices {
		switch c.Decision {
		case msp.ApprovalDecisionApproved:
			if ask.allowChoice == "" {
				ask.allowChoice = c.ChoiceID
			}
		case msp.ApprovalDecisionAbort, msp.ApprovalDecisionDenied:
			if ask.abortChoice == "" {
				ask.abortChoice = c.ChoiceID
			}
		}
	}
	summary := approvalSummary(p)
	inter := &agents.Interaction{
		ID:     "approval-" + p.ApprovalID,
		Kind:   "question",
		Prompt: summary,
		Questions: []transcript.Question{{
			ID:       "approval-" + p.ApprovalID,
			Header:   "承認",
			Question: summary,
			Options:  []transcript.Option{{Label: "許可"}, {Label: "拒否"}},
		}},
	}
	h.setAsk(ask, inter)
}

// approvalSummary renders what the member has to decide about. The shell subject carries the
// command plus the parsed argv per stage; everything else falls back to the tool name, which
// is the one field every subject kind has.
func approvalSummary(p msp.ApprovalRequestParams) string {
	if p.Subject.Command != nil && *p.Subject.Command != "" {
		return *p.Subject.Command
	}
	for _, s := range p.Subject.Stages {
		if len(s.Argv) > 0 {
			return strings.Join(s.Argv, " ")
		}
	}
	if p.ToolName != "" {
		return p.ToolName
	}
	return "ツールの実行"
}

// onUserInput maps a user-input prompt onto AF's question Interaction, which it fits field
// for field — this is the channel AF already has, and it is a different one from approvals.
func (h *threadHandle) onUserInput(p msp.UserInputRequestParams) {
	inter := &agents.Interaction{ID: "ask-" + p.UserInputID, Kind: "question"}
	for _, q := range p.Questions {
		tq := transcript.Question{ID: q.ID, Header: q.Header, Question: q.Question}
		for _, o := range q.Options {
			opt := transcript.Option{Label: o.Label}
			if o.Description != nil {
				opt.Description = *o.Description
			}
			tq.Options = append(tq.Options, opt)
		}
		inter.Questions = append(inter.Questions, tq)
	}
	h.setAsk(&pendingAsk{userInputID: p.UserInputID, questions: p.Questions}, inter)
}

// setAsk records a pending prompt. Both channels RE-DELIVER, so an arriving prompt that
// matches the one already held is not a second question — replacing it in place keeps the
// Console from stacking duplicates.
func (h *threadHandle) setAsk(ask *pendingAsk, inter *agents.Interaction) {
	h.mu.Lock()
	h.pending, h.inter = ask, inter
	h.state = agents.TurnWaitingInteraction
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
}

// clearAsk drops the pending prompt once the host says it is settled — including when it was
// settled by someone else, which is why it is driven by the notification rather than by our
// own answer returning.
func (h *threadHandle) clearAsk(match func(*pendingAsk) bool) {
	h.mu.Lock()
	if h.pending == nil || !match(h.pending) {
		h.mu.Unlock()
		return
	}
	h.pending, h.inter = nil, nil
	if h.running {
		h.state = agents.TurnRunning
	}
	st := h.state
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

// --- ThreadHandle ------------------------------------------------------------

// Send starts a turn, queueing it when one is already running. MSP would accept a queued turn
// itself (turn/start has an ifBusy policy), but AF owns the queue for every managed kind and
// the Console renders it, so the queue stays here.
func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in, false) }

// Steer injects input into the RUNNING turn. MSP carries it natively, so unlike the ACP kinds
// this is not a queue in disguise — but a steer with no turn to steer is a plain send.
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in, true) }

func (h *threadHandle) accept(in agents.TurnInput, steer bool) error {
	if id := agents.NormalizeMsgID(in.ClientMessageID); id != "" && ledger.SeenOrRecord(h.name, id) {
		return nil // a resend after a reconnect must not start a second turn
	}
	h.mu.Lock()
	if !h.alive || h.cl == nil {
		h.mu.Unlock()
		return errors.New("Muse Code のホストが起動していません")
	}
	running, turnID := h.running, h.turnID
	if running && !steer {
		h.queue = append(h.queue, in)
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	if steer && running && turnID != "" {
		return h.steerNow(in, turnID)
	}
	return h.startTurn(in)
}

func (h *threadHandle) startTurn(in agents.TurnInput) error {
	h.mu.Lock()
	cl, sid, effort := h.cl, h.sid, h.settings.Effort
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	params := msp.TurnStartParams{
		CommandID: msp.NewCommandID(),
		SessionID: sid,
		Input:     inputParts(in),
	}
	if e := reasoningEffort(effort); e != nil {
		params.ReasoningEffort = e
	}
	if err := cl.CallInto(msp.MethodTurnStart, params, callTimeout, nil); err != nil {
		return err
	}
	// turn/started follows as a notification and is what actually moves the state; marking
	// running here would race it into a stuck "working" if the host refused the turn after
	// accepting the command.
	return nil
}

func (h *threadHandle) steerNow(in agents.TurnInput, turnID string) error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	return cl.CallInto(msp.MethodTurnSteer, msp.TurnSteerParams{
		CommandID:      msp.NewCommandID(),
		SessionID:      sid,
		ExpectedTurnID: turnID,
		Input:          inputParts(in),
	}, callTimeout, nil)
}

// pump starts the next queued turn once the previous one has settled.
func (h *threadHandle) pump() {
	h.mu.Lock()
	if !h.alive || h.running || len(h.queue) == 0 {
		h.mu.Unlock()
		return
	}
	next := h.queue[0]
	h.queue = h.queue[1:]
	h.mu.Unlock()
	if err := h.startTurn(next); err != nil {
		log.Printf("muse: %s: queued turn failed to start: %v", h.name, err)
	}
}

// inputParts renders a TurnInput as MSP input parts. Attachments ride as their own text parts
// naming the path: managed attaches through the API rather than pasting into a pane, and the
// image part type takes base64 rather than a path, so a path is text until the transcript work
// package teaches this to read the file.
func inputParts(in agents.TurnInput) []msp.TurnInputPart {
	parts := []msp.TurnInputPart{{Type: msp.TurnInputPartTypeText, Text: strPtr(in.Prompt)}}
	for _, a := range in.Attachments {
		parts = append(parts, msp.TurnInputPart{Type: msp.TurnInputPartTypeText, Text: strPtr(a)})
	}
	return parts
}

func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	cl, sid, turnID := h.cl, h.sid, h.turnID
	h.queue = nil
	if h.running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	params := msp.TurnInterruptParams{CommandID: msp.NewCommandID(), SessionID: sid}
	if turnID != "" {
		params.TurnID = &turnID
	}
	return cl.CallInto(msp.MethodTurnInterrupt, params, callTimeout, nil)
}

// UpdateSettings changes the model and the reasoning effort of a RUNNING session. Mode is not
// accepted: MSP has no method that sets AF's plan mode, and DynamicMode is false for that
// reason — silently ignoring it here instead would make the Console show a control that does
// nothing.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	if s.Model != "" {
		err := cl.CallInto(msp.MethodSessionSetModel, msp.SessionSetModelParams{
			CommandID: msp.NewCommandID(),
			SessionID: sid,
			Model:     msp.ModelSelection{ModelID: s.Model},
		}, callTimeout, nil)
		if err != nil {
			return err
		}
		h.mu.Lock()
		h.settings.Model = s.Model
		h.mu.Unlock()
	}
	if s.Effort != "" {
		e := reasoningEffort(s.Effort)
		if e == nil {
			return fmt.Errorf("Muse Code が解釈できる reasoning effort ではありません: %s", s.Effort)
		}
		err := cl.CallInto(msp.MethodSessionSetReasoningEffort, msp.SessionSetReasoningEffortParams{
			CommandID:       msp.NewCommandID(),
			SessionID:       sid,
			ReasoningEffort: *e,
		}, callTimeout, nil)
		if err != nil {
			return err
		}
		h.mu.Lock()
		h.settings.Effort = s.Effort
		h.mu.Unlock()
	}
	h.mu.Lock()
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// reasoningEffort maps AF's effort string onto the wire enum, or nil when the string names
// nothing MSP knows. Returning nil rather than a default is deliberate: sending a guessed
// effort is a silent behaviour change the member did not ask for.
func reasoningEffort(s string) *msp.ReasoningEffort {
	switch msp.ReasoningEffort(strings.ToLower(strings.TrimSpace(s))) {
	case msp.ReasoningEffortNone, msp.ReasoningEffortMinimal, msp.ReasoningEffortLow,
		msp.ReasoningEffortMedium, msp.ReasoningEffortHigh, msp.ReasoningEffortXhigh,
		msp.ReasoningEffortMax, msp.ReasoningEffortUltra:
		e := msp.ReasoningEffort(strings.ToLower(strings.TrimSpace(s)))
		return &e
	}
	return nil
}

// Respond answers whichever prompt is pending.
//
// Both channels re-deliver and both refuse a second answer with their own "already settled"
// code. That is success, not failure: the member's click landed, and reporting an error would
// send them to click again on a prompt nobody is waiting for.
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	cl, sid, ask, inter := h.cl, h.sid, h.pending, h.inter
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	if ask == nil || inter == nil {
		return errors.New("応答を待っている質問がありません")
	}
	if reply.ID != "" && reply.ID != inter.ID {
		return fmt.Errorf("応答先の質問が一致しません: %s", reply.ID)
	}

	var err error
	if ask.isApproval() {
		err = h.decideApproval(cl, sid, ask, reply)
	} else {
		err = h.answerUserInput(cl, sid, ask, reply)
	}
	if err != nil && !msp.Settled(err) {
		return err
	}
	h.clearAsk(func(*pendingAsk) bool { return true })
	return nil
}

func (h *threadHandle) decideApproval(cl *msp.Client, sid string, ask *pendingAsk, reply agents.InteractionReply) error {
	choice := ask.abortChoice
	if approved(reply) {
		choice = ask.allowChoice
	}
	if choice == "" {
		return errors.New("Muse Code が提示した選択肢に該当するものがありません")
	}
	return cl.CallInto(msp.MethodApprovalDecide, msp.ApprovalDecideParams{
		CommandID:     msp.NewCommandID(),
		SessionID:     sid,
		ApprovalID:    ask.approvalID,
		ChoiceID:      choice,
		RequirementID: ask.requirement, // echoed verbatim: the multi-stage race guard
	}, callTimeout, nil)
}

// approved reads an InteractionReply as allow or deny. The reply arrives in either vocabulary
// because the Interaction is built as a question: an explicit allow/deny decision, or an
// answer selecting option 0 (allow) or 1 (deny).
func approved(reply agents.InteractionReply) bool {
	switch reply.Decision {
	case agents.DecisionAllow:
		return true
	case agents.DecisionDeny, agents.DecisionCancel:
		return false
	}
	for _, a := range reply.Answers {
		for _, i := range a.Options {
			return i == 0
		}
	}
	return false
}

func (h *threadHandle) answerUserInput(cl *msp.Client, sid string, ask *pendingAsk, reply agents.InteractionReply) error {
	answers := make([]msp.UserInputAnswer, 0, len(ask.questions))
	for i, q := range ask.questions {
		a := msp.UserInputAnswer{QuestionID: q.ID}
		if i < len(reply.Answers) {
			r := reply.Answers[i]
			if r.Text != "" {
				a.FreeText = strPtr(r.Text)
			}
			// The wire takes LABELS, not indexes, so an out-of-range index is dropped
			// rather than sent as a label the host would refuse.
			for _, idx := range r.Options {
				if idx >= 0 && idx < len(q.Options) {
					a.SelectedLabels = append(a.SelectedLabels, q.Options[idx].Label)
				}
			}
			if len(a.SelectedLabels) == 1 {
				a.SelectedLabel = strPtr(a.SelectedLabels[0])
			}
		}
		answers = append(answers, a)
	}
	return cl.CallInto(msp.MethodUserInputAnswer, msp.UserInputAnswerParams{
		CommandID:   msp.NewCommandID(),
		SessionID:   sid,
		UserInputID: ask.userInputID,
		Answers:     answers,
	}, callTimeout, nil)
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

// Snapshot is the reconciliation view (§6): where this thread stands, for settling the turn
// state after a disconnect. It reads the handle rather than the wire — the host is the source
// of truth for the live state, and it reaches the handle through the notifications above.
func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// emit publishes an event, dropping it when nobody is draining. The channel is deliberately
// lossy: Events is a live subscription with no replay (EventReplay is false for every kind),
// and recovery is Snapshot reconciliation, so blocking the read goroutine on a full channel
// would stall the whole connection to save an event the design already says may be missed.
func (h *threadHandle) emit(ev agents.Event) {
	select {
	case h.events <- ev:
	default:
	}
}

func strPtr(s string) *string { return &s }

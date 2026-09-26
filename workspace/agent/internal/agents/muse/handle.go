package muse

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

	// forkFrom / forkAt carry the pending fork for a slot that has never opened a session.
	// They live on the handle rather than being read from meta inside openSession because
	// openSession is also the RESUME path, and a fork is a one-time act at birth.
	forkFrom string
	forkAt   string

	// sessionMCP records whether the host GRANTED the sessionMcp capability. It is asked once
	// at handshake and remembered: a capability the host did not grant is not a thing to send
	// anyway, and sending servers into a host that cannot take them is how a decode failure
	// becomes "the integration is broken".
	sessionMCP bool

	// bypass is the launch-time permission choice. It selects the session's approval mode and
	// is resolved on every Resume rather than carried in ThreadSettings, where "empty means
	// unchanged" cannot express a three-valued bool.
	bypass bool

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	cl    *msp.Client
	sid   string // the muse session id (the UUIDv7 AF minted)
	path  string // the session.jsonl session/start reported
	model string // the model the host last reported as selected
	// turnModel is model as it stood when the running turn started, stamped on that turn's
	// items: session/setModel is acknowledged at once but applied at the next model call, so
	// the latest model would mislabel the call already in flight.
	turnModel string
	alive     bool
	state     agents.TurnState
	turnID    string // the running turn, for steer's expectedTurnId

	running  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction
	pending  *pendingAsk // what inter is waiting on, in muse's own vocabulary
	// settledUserInputID and settledApprovalID block re-deliveries after an answer; one per
	// channel is enough because IDs are unique per session — only the last settled prompt can
	// arrive again before the host sends userInput/settled or approval/resolved.
	settledUserInputID string
	settledApprovalID  string
	events             chan agents.Event

	// streaming holds the item/delta fragments of items that have not completed yet, keyed
	// by item id. In memory only — see onDelta.
	streaming map[string]string

	// Live context fill (session/contextUsage). Separate lock from mu so onNotify
	// can record context without contending with turn plumbing. Read by ManagedContext.
	ctxMu       sync.Mutex
	ctxUsed     int64  // usedTokens from the latest session/contextUsage notification
	ctxWindow   *int64 // windowTokens; nil when the basis carries no limit
	ctxHasUsage bool   // false until the first notification arrives
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
	// One host per session: this puts the name in the model's own shell as well, the way a
	// Terminal session has it. The af server gets it on the wire (mcp.go), since muse scrubs
	// its MCP children's environment.
	cmd.Env = agents.WithSessionName(childEnv(os.Environ()), h.name)
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
	h.sessionMCP = msp.Granted(res, msp.CapabilityNameSessionMCP)
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

// openSession reloads this slot's conversation, forks one, or starts a fresh one.
//
// The id is stored rather than derived: MSP refuses a retained or reserved id with
// `session_id_conflict`, so AF's usual deterministic UUIDv5 would work exactly once
// (decision 4). A stored id whose session the host no longer knows falls back to a fresh
// start rather than leaving the slot dead — losing the history is bad, refusing to launch is
// worse, and the old session.jsonl is still on disk either way.
func (h *threadHandle) openSession(cl *msp.Client, st agents.ThreadSettings) error {
	if prev, ok := readSession(h.slotSid); ok && prev.ID != "" {
		var res msp.SessionResumeResult
		err := cl.CallInto(msp.MethodSessionResume, msp.SessionResumeParams{
			CommandID: msp.NewCommandID(),
			SessionID: prev.ID,
		}, callTimeout, &res)
		if err == nil {
			h.mu.Lock()
			h.sid, h.path = prev.ID, prev.Path
			h.setModelLocked(res.Session.ModelID)
			h.mu.Unlock()
			return nil
		}
		if !msp.HasCode(err, msp.ErrCodeSessionNotFound) {
			return fmt.Errorf("Muse Code セッションの再開に失敗しました: %w", err)
		}
		log.Printf("muse: %s: stored session %s is gone; starting a fresh one", h.name, prev.ID)
	}

	// A slot born from a fork opens by copying the source rather than starting empty. It is
	// tried once, at birth: after this the slot has a stored session and takes the resume
	// path above, so a later failure can never re-fork an already-lived conversation.
	if h.forkFrom != "" {
		if err := h.forkSession(cl); err != nil {
			return err
		}
		return nil
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
	} else if safe := SafeDefaultModel(cl); safe != "" {
		// 🔴 Omitting modelId is not the neutral choice it looks like: the host's own default
		// is the contributor variant, whose catalogue description says the conversation may be
		// used for product improvement (decision 6 clamp 8). So "the member chose no model"
		// resolves HERE, to the newest row the vendor makes no such claim about, and it
		// resolves on every path that starts a session rather than in the Console — a
		// scheduled run and an MCP-created session get the same answer as a launch menu.
		params.ModelID = &safe
	}
	// Integration (MCP) servers ride the wire, per session (decision 11 / mcp.go). A registry
	// failure logs and launches anyway — the posture materialisation takes for every other
	// kind — because a broken integration must not cost the member their session.
	h.mu.Lock()
	granted := h.sessionMCP
	h.mu.Unlock()
	if granted {
		servers, err := sessionMCPServers(h.name)
		if err != nil {
			log.Printf("muse: %s: MCP servers unavailable, starting without them: %v", h.name, err)
		} else if len(servers) > 0 {
			params.Config = &msp.SessionConfig{MCPServers: servers}
		}
	}
	var res msp.SessionStartResult
	if err := cl.CallInto(msp.MethodSessionStart, params, callTimeout, &res); err != nil {
		return fmt.Errorf("Muse Code セッションの開始に失敗しました: %w", err)
	}
	h.mu.Lock()
	h.sid, h.path = res.Session.SessionID, res.Session.Path
	h.setModelLocked(res.Session.ModelID)
	h.mu.Unlock()
	writeSession(h.slotSid, museSession{ID: res.Session.SessionID, Path: res.Session.Path})
	return nil
}

// setModelLocked adopts the model the host reports; nil or empty keeps the last known one.
// Caller holds h.mu.
func (h *threadHandle) setModelLocked(id *string) {
	if id != nil && *id != "" {
		h.model = *id
	}
}

// approvalModeFor maps the launch-time permission choice onto the session's approval mode. It
// is still sent explicitly rather than by omission, so the wire records a choice rather than a
// default — but 🔴 what it buys in a Workspace is measured, and it is nothing.
//
// Measured on 1.3.0-R3401.1 (ADR 0095 P2-6): the mode IS echoed back on the started session
// (`session.approvalMode.mode` = what was asked, source `startup`), yet the host's committed
// enforcement profile reports `approval: "on_request"` for every one of the four wire values —
// `allowAll`, `onRequest`, `promptUnmatched`, `denyUnmatched` — so the two layers disagree and
// which one governs is not settled here. What IS settled is that it does not matter under
// `--disable-sandbox`: with the filesystem unrestricted, tool calls resolve `allow:policy`
// before any approval layer, and no approval is ever raised (muse.go's Caps header carries the
// two turns that measured it).
//
// It stays wired anyway, at zero cost: the driver can answer an approval, so if a deployment
// ever regains a restricted posture the gate is already selected rather than needing a change.
//
// AF maps two of the four values. `promptUnmatched` and `denyUnmatched` are unmapped because
// nothing has measured what they do — and `denyUnmatched` is not even in the host's own
// `component_ceilings.approval` list (`on_request`, `prompt_unmatched`, `allow_all`).
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
// The declared notification table is a decode map, not an allow-list: a host can emit a
// notification its bundle does not declare (1.3.0-R3401.1 sends `session/started` before the
// `session/start` response), so an unknown method is dropped rather than treated as a protocol
// error.
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
		h.turnModel = h.model
		h.mu.Unlock()
		agents.MarkTurnStart(h.slotSid)
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})

	case msp.NotificationTurnCompleted:
		var p msp.TurnCompletedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.finishTurn(p)
		h.mu.Lock()
		h.turnModel = ""
		h.mu.Unlock()

	case msp.NotificationSessionStatusChanged:
		var p msp.SessionStatusChangedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onStatus(p)

	case msp.NotificationItemStarted:
		var p msp.ItemStartedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onItem(p.Item)

	case msp.NotificationItemUpdated, msp.NotificationItemCompleted:
		// Both carry a whole item; `completed` is the last word on it and `updated` is a
		// revision of one already recorded. The store folds them by revision on read, so
		// they take the same path in.
		var p msp.ItemCompletedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onItem(p.Item)

	case msp.NotificationItemDelta:
		var p msp.ItemDeltaParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.onDelta(p)

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

	case msp.NotificationSessionContextUsage:
		// Context-window pressure for THIS session — stored on the handle, not process-wide.
		// windowTokens is absent when the basis has no limit; never fabricate a value for it.
		var p msp.SessionContextUsageParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.ctxMu.Lock()
		h.ctxUsed = p.UsedTokens
		h.ctxWindow = p.WindowTokens
		h.ctxHasUsage = true
		h.ctxMu.Unlock()

	case msp.NotificationSessionModelChanged:
		var p msp.SessionModelChangedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.mu.Lock()
		h.setModelLocked(&p.ModelID)
		h.mu.Unlock()

	case msp.NotificationUsageChanged:
		// Unsolicited, and about the ACCOUNT rather than this session — so it is recorded
		// process-wide (usage.go) rather than on the handle. This is the only route by which
		// the quota chip learns anything without being asked.
		var p msp.SubscriptionUsage
		if json.Unmarshal(params, &p) != nil {
			return
		}
		recordQuota(p)
	}
}

// onItem persists one item and drops any streaming buffer it had. A store failure is logged
// once and never fails the turn: the host owns the conversation of record, and refusing to
// carry on because AF could not mirror a line would trade a rendering gap for a dead session.
func (h *threadHandle) onItem(it msp.Item) {
	h.mu.Lock()
	delete(h.streaming, it.ItemID)
	sid, model := h.slotSid, h.turnModel
	if model == "" {
		model = h.model
	}
	h.mu.Unlock()
	if err := openStore(sid).AppendFrom(it, model); err != nil {
		log.Printf("muse: %s: transcript append: %v", h.name, err)
	}
}

// onDelta accumulates a streaming fragment. Deltas are NOT persisted: the `item/completed`
// that follows carries the whole text, so writing every fragment would multiply the store by
// the streaming granularity and then be thrown away. They live in memory only, and Transcript
// overlays them so the mirror streams while the turn runs.
func (h *threadHandle) onDelta(p msp.ItemDeltaParams) {
	// The schema names which member a delta extends; a delta for anything but the item's own
	// text is not something the mirror can splice, so it is dropped rather than appended to
	// the wrong field.
	if p.Field != nil && *p.Field != "" && *p.Field != "text" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streaming == nil {
		h.streaming = map[string]string{}
	}
	h.streaming[p.ItemID] += p.Delta
}

// streamingText returns a copy of the in-flight fragments, for Transcript's overlay.
func (h *threadHandle) streamingText() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.streaming) == 0 {
		return nil
	}
	out := make(map[string]string, len(h.streaming))
	for k, v := range h.streaming {
		out[k] = v
	}
	return out
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

// onApproval turns a pending approval into an approval-kind Interaction.
//
// This is the kind ADR 0095 decision 13 calls AF's first: an approval asks whether a tool may
// run, and refusing it stops that tool, so it carries the SUBJECT rather than a list of
// options. Folding it into a two-option question — the reuse kiro's ACP
// session/request_permission and lcpp's own gate make — would keep the command line and
// discard the tool name, the protected-write marking, the judge escalation and the parsed
// argv of every stage of a pipeline, which is most of what a member decides with.
func (h *threadHandle) onApproval(p msp.ApprovalRequestParams) {
	h.mu.Lock()
	settled := h.settledApprovalID
	h.mu.Unlock()
	if p.ApprovalID == settled {
		return // re-delivery after Respond cleared the ask; approval/resolved is still in flight
	}
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
	req := &agents.ApprovalRequest{
		Summary:        summary,
		Tool:           p.ToolName,
		ProtectedWrite: p.ProtectedWrite,
		JudgeEscalated: p.JudgeEscalated,
	}
	if p.Subject.Command != nil {
		req.Command = *p.Subject.Command
	}
	for _, st := range p.Subject.Stages {
		if len(st.Argv) > 0 {
			req.Stages = append(req.Stages, st.Argv)
		}
	}
	h.setAsk(ask, &agents.Interaction{
		ID:       "approval-" + p.ApprovalID,
		Kind:     agents.InteractionApproval,
		Prompt:   summary,
		Approval: req,
	})
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
	h.mu.Lock()
	settled := h.settledUserInputID
	h.mu.Unlock()
	if p.UserInputID == settled {
		return // re-delivery after Respond cleared the ask; userInput/settled is still in flight
	}
	inter := &agents.Interaction{ID: "ask-" + p.UserInputID, Kind: agents.InteractionQuestion}
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
	if h.pending.isApproval() {
		h.settledApprovalID = h.pending.approvalID
	} else {
		h.settledUserInputID = h.pending.userInputID
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
		Input:     skillPart(cl, sid, inputParts(in)),
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
		Input:          skillPart(cl, sid, inputParts(in)),
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

// maxInlineImageBytes caps what AF will base64 into a single frame. The upload endpoint accepts
// 64 MiB (fs.go's defaultMaxUpload) and base64 inflates by a third, so an uncapped read would
// put an 85 MB line on a pipe whose reader is bounded at 16 MiB (msp/client.go). Eight is far
// above any screenshot and well under both limits; anything larger takes the path route below,
// which still works — the workspace is trusted and the host's filesystem is unrestricted, so
// muse can open the file itself.
const maxInlineImageBytes = 8 << 20

// inputParts renders a TurnInput as MSP input parts: the prompt, then one part per attachment.
//
// An IMAGE becomes a real `image` part — the wire takes base64 rather than a path, so the file
// is read here. Everything else rides as a text part naming the path, which is the same
// treatment codex gives a non-image attachment (buildInput): mentioning a path is enough for an
// agent that can read files.
//
// Every failure falls back to that text part rather than failing the turn or dropping the
// attachment. A member who pasted a screenshot must never end up with a turn that mentions
// nothing at all, and the path is still useful to the model.
func inputParts(in agents.TurnInput) []msp.TurnInputPart {
	parts := []msp.TurnInputPart{{Type: msp.TurnInputPartTypeText, Text: strPtr(in.Prompt)}}
	for _, a := range in.Attachments {
		if strings.TrimSpace(a) == "" {
			continue
		}
		if p, ok := imagePart(a); ok {
			parts = append(parts, p)
			continue
		}
		parts = append(parts, msp.TurnInputPart{Type: msp.TurnInputPartTypeText, Text: strPtr(a)})
	}
	return parts
}

// imagePart reads an attachment into an `image` part, or reports false so the caller keeps the
// path as text.
func imagePart(path string) (msp.TurnInputPart, bool) {
	mediaType, ok := imageMediaType(path)
	if !ok {
		return msp.TurnInputPart{}, false
	}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		log.Printf("muse: attachment %s is not readable, sending its path instead: %v", path, err)
		return msp.TurnInputPart{}, false
	case !st.Mode().IsRegular():
		return msp.TurnInputPart{}, false
	case st.Size() > maxInlineImageBytes:
		log.Printf("muse: attachment %s is %d bytes (> %d), sending its path instead", path, st.Size(), maxInlineImageBytes)
		return msp.TurnInputPart{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("muse: attachment %s could not be read, sending its path instead: %v", path, err)
		return msp.TurnInputPart{}, false
	}
	// An empty payload is `invalidParams` on the wire, and that costs the whole turn rather
	// than the attachment. Checked after the read rather than off the stat above, so a file
	// emptied in between is caught by the same line (and so that the branch has one reason to
	// exist rather than two, one of which no test could tell apart).
	if len(b) == 0 {
		return msp.TurnInputPart{}, false
	}
	data := base64.StdEncoding.EncodeToString(b)
	return msp.TurnInputPart{
		Type:       msp.TurnInputPartTypeImage,
		MediaType:  &mediaType,
		Base64Data: &data,
	}, true
}

// imageMediaType is the fixed extension → media type map, deliberately not
// `mime.TypeByExtension`: that reads the container's /etc/mime.types, which may be absent, and
// `mediaType` is REQUIRED on an image part — an empty or surprising one costs the turn, not the
// attachment. The four types are exactly what the paste endpoint itself accepts
// (sessionx/session_paste.go's imageExt), so a file that arrived by paste always has one.
func imageMediaType(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	}
	return "", false
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
//
// "Back to the default" is a real request, not an absence: ClearModel and ClearEffort exist
// because an empty string means "unchanged" and cannot also mean "reset". Both are honoured
// below, and for the model that means AF's default (the non-data-sharing row), never the
// host's — reverting to the host's default would move the member onto the contributor variant
// by way of a control labelled "Default".
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	model := s.Model
	if s.ClearModel {
		model = SafeDefaultModel(cl)
	}
	if model != "" {
		err := cl.CallInto(msp.MethodSessionSetModel, msp.SessionSetModelParams{
			CommandID: msp.NewCommandID(),
			SessionID: sid,
			Model:     msp.ModelSelection{ModelID: model},
		}, callTimeout, nil)
		if err != nil {
			return err
		}
		h.mu.Lock()
		h.settings.Model = model
		h.model = model
		h.mu.Unlock()
	}
	if s.ClearEffort {
		// No wire call: `session/setReasoningEffort` sets a value and has no "unset", and the
		// effort a turn runs at is `turn/start.reasoningEffort`, which AF omits when it holds
		// none. Forgetting it here is therefore exactly "let the host decide from now on".
		h.mu.Lock()
		h.settings.Effort = ""
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
//
// The accepted set is the generated one, not a copy: the same list is what the Console's
// picker offers (models.go), and a hand-kept second copy is how a value the vendor adds in a
// later bundle ends up offered but refused, or accepted but never offered.
func reasoningEffort(s string) *msp.ReasoningEffort {
	want := msp.ReasoningEffort(strings.ToLower(strings.TrimSpace(s)))
	for _, e := range msp.ReasoningEffortValues {
		if e == want {
			return &e
		}
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

// forkSession opens this slot by copying the source conversation (decision 13 — MSP carries
// `session/fork`).
//
// Two copies happen, and both are needed: the HOST's history through `session/fork`, and AF's
// own item store through store.ForkAt, because the store is what `Transcript` reads and a
// forked session with an empty one would show the member nothing (transcript.go's header).
//
// Unlike `session/start`, AF does not mint the id: the host returns the new session, so the
// stored id is read out of the result rather than chosen. That also means a retry cannot be
// made idempotent by the id — which is why this runs only for a slot with no stored session.
func (h *threadHandle) forkSession(cl *msp.Client) error {
	prev, ok := readSession(h.forkFrom)
	if !ok || prev.ID == "" {
		return errors.New("フォーク元の Muse Code セッションが見つかりません")
	}
	params := msp.SessionForkParams{
		CommandID: msp.NewCommandID(),
		SessionID: prev.ID,
	}
	if h.forkAt != "" {
		params.CutPoint = &msp.ForkCutPoint{LastTurnID: h.forkAt}
	}
	var res msp.SessionForkResult
	if err := cl.CallInto(msp.MethodSessionFork, params, callTimeout, &res); err != nil {
		return fmt.Errorf("Muse Code セッションのフォークに失敗しました: %w", err)
	}
	h.mu.Lock()
	h.sid, h.path = res.Session.SessionID, res.Session.Path
	h.setModelLocked(res.Session.ModelID)
	h.mu.Unlock()
	writeSession(h.slotSid, museSession{ID: res.Session.SessionID, Path: res.Session.Path})
	// The store copy is deliberately AFTER the host's fork succeeded and is deliberately not
	// fatal: the conversation exists either way, and refusing the session because AF could not
	// mirror its history would trade a rendering gap for a dead session — the same posture
	// onItem takes for every other write to this store.
	if err := openStore(h.forkFrom).ForkAt(h.slotSid, h.forkAt); err != nil {
		log.Printf("muse: %s: fork: transcript copy failed: %v", h.name, err)
	}
	return nil
}

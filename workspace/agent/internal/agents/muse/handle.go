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

	running bool
	// runGen moves every time the host reports a turn running, so the idle fallback
	// (settleIdle) closes only the turn it saw go idle.
	runGen uint64
	// q is the input queue and the whole of ADR 0105's stop rules (agents.TurnQueue); every
	// call holds mu. Read it through tq. Its head is the input whose turn/start is out (or
	// whose turn runs); turn/started is where the host holds it.
	q *agents.TurnQueue
	// starting is the commandId of the head's turn/start until the host reports it as
	// turn/started, and the handle counts as busy until it does. The ack comes back before
	// turn/started (measured on 1.4.0: ~30 ms apart), and a turn/start sent in that gap lands in
	// the host's own queue (disposition "queued"), which this driver neither shows nor stops.
	starting string
	// calling is set while the head's turn/start call is out: a host lost meanwhile leaves the
	// head to the caller, whose call fails with ErrClosed.
	calling  bool
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

	// sentImages maps a commandId to the paths of the attachments that went out as `image`
	// parts under it, until the host's `userMessage` for that command arrives. The item echoes
	// image metadata only (no path, `[Image #N]` in its text), and the mirror finds thumbnails
	// by path, so without this the member's own screenshots vanish from the bubble.
	sentImages map[string][]string

	// bg is the tool calls still running, by item id, for BackgroundWork (background.go).
	bg map[string]bgEntry

	// Live context fill (session/contextUsage). Separate lock from mu so onNotify
	// can record context without contending with turn plumbing. Read by ManagedContext.
	ctxMu       sync.Mutex
	ctxUsed     int64       // usedTokens from the latest session/contextUsage notification
	ctxWindow   *int64      // windowTokens; nil when the basis carries no limit
	ctxHasUsage bool        // false until the first notification arrives
	spends      []turnSpend // per-turn token trend from session/tokenUsage, newest last (context.go)
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

// tq returns the handle's queue, creating it on first use. Caller holds h.mu.
func (h *threadHandle) tq() *agents.TurnQueue {
	if h.q == nil {
		h.q = agents.NewTurnQueue(h.name, ledger, agents.LedgerAtAccept)
	}
	return h.q
}

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
	// The host's own diagnostics go to stderr: keep them out of the Agent's log, but hold the
	// tail so a failed start can say why.
	tail, err := agents.StartWithStderrTail(cmd)
	if err != nil {
		return fmt.Errorf("muse serve の起動に失敗しました: %w", err)
	}
	defer tail.Settle() // after any failure snapshot; see StderrTail.Release

	cl := msp.NewClient(stdin, stdout, msp.Handler{
		OnNotification: h.onNotify,
		OnRequest:      h.onRequest,
	})

	res, err := msp.Handshake(cl, clientVersion, []msp.CapabilityName{msp.CapabilityNameSessionMCP})
	if err != nil {
		err = tail.Wrap(fmt.Errorf("Muse Code との接続に失敗しました: %w", err))
		stopChild(cmd, stdin)
		tail.Release() // no watch owns this child yet
		return err
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
		err = tail.Wrap(err)
		stopChild(cmd, stdin)
		tail.Release() // no watch owns this child yet
		h.mu.Lock()
		h.cmd, h.stdin, h.cl = nil, nil, nil
		h.mu.Unlock()
		return err
	}

	h.mu.Lock()
	h.alive = true
	h.state = agents.TurnCompleted
	h.mu.Unlock()
	go h.watch(cmd, tail, cl)
	go h.pump() // what a lost host left queued, in order, ahead of whatever Resume is for
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
			h.rebuildBgLocked(res.History)
			h.mu.Unlock()
			return nil
		}
		if !msp.HasCode(err, msp.ErrCodeSessionNotFound) {
			return fmt.Errorf("Muse Code セッションの再開に失敗しました: %w", err)
		}
		log.Printf("muse: %s: stored session %s is gone; starting a fresh one", h.name, prev.ID)
	}
	h.resetUsage() // a different conversation from here on
	h.mu.Lock()
	h.bg = nil
	h.mu.Unlock()

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
	safe := ""
	if st.Model == "" {
		var err error
		if safe, err = SafeDefaultModel(cl); err != nil {
			return err
		}
	}
	if st.Model != "" {
		params.ModelID = &st.Model
	} else if safe != "" {
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
func (h *threadHandle) watch(cmd *exec.Cmd, tail *agents.StderrTail, cl *msp.Client) {
	<-cl.Closed()
	_ = cmd.Wait()
	tail.Release()
	h.hostLost(cl)
}

// hostLost is watch's verdict once the child behind cl is gone.
func (h *threadHandle) hostLost(cl *msp.Client) {
	h.mu.Lock()
	if h.cl != cl {
		h.mu.Unlock() // already replaced by a newer spawn
		return
	}
	wasRunning := h.running || h.starting != ""
	// The queue stays: the next Resume respawns the host and spawn drains it, in order. The
	// head's turn, if it had one, died with the host. A head the host admitted but never
	// started goes back to the front for the respawn, unless a stop was waiting for it. A head
	// whose turn/start is still out is left to the caller, whose call fails with ErrClosed.
	if t := h.tq().Head(); t != nil && !h.calling {
		if h.starting != "" {
			h.tq().Requeue(t)
		} else {
			h.tq().Settle(t)
		}
	}
	h.alive, h.running, h.cl = false, false, nil
	h.starting = ""
	h.sentImages = nil // nothing this host was sent can be echoed any more
	h.bg = nil         // its tasks went with it; a resume rebuilds from the next host's fold
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
		h.runGen++
		h.state = agents.TurnRunning
		h.turnModel = h.model
		// An empty commandId is a host that does not say whose turn this is; the turn is
		// running either way, so it is taken as ours rather than leaving the handle busy.
		// The head's turn/started is where the host holds that input (ADR 0105 decision 3): a
		// stop that found it committed left the delivery to this point.
		stop := false
		if h.starting != "" && (p.CommandID == "" || p.CommandID == h.starting || p.TurnID == h.starting) {
			h.starting = ""
			if t := h.tq().Head(); t != nil {
				stop = h.tq().Received(t)
			}
		}
		if stop {
			h.state = agents.TurnInterrupting
		}
		st := h.state
		h.mu.Unlock()
		agents.MarkTurnStart(h.slotSid)
		h.emit(agents.Event{Kind: "turn_state", TurnState: st})
		if stop {
			// Off the read goroutine: interrupt waits for the host's answer.
			go func() { _ = h.interruptTurn(p.TurnID) }()
		}

	case msp.NotificationTurnCompleted:
		var p msp.TurnCompletedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.finishTurn(p)
		h.mu.Lock()
		h.turnModel = ""
		h.mu.Unlock()

	case msp.NotificationSessionClosed:
		// An orderly unload: nothing of the session runs any more. It is broadcast to every
		// connection, so it is checked against this handle's own session.
		var p msp.SessionClosedParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.mu.Lock()
		if p.SessionID == h.sid {
			h.bg = nil
		}
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

	case msp.NotificationSessionTokenUsage:
		var p msp.SessionTokenUsageParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		h.recordTokenUsage(p)

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
	h.trackBgLocked(it)
	sid, model := h.slotSid, h.turnModel
	if model == "" {
		model = h.model
	}
	var images []string
	if it.Kind == msp.ItemKindUserMessage && it.CommandID != nil {
		// Taken on the first revision only; the store's fold keeps the first stamp for the
		// revisions that follow, the same way it keeps the model.
		images = h.sentImages[*it.CommandID]
		delete(h.sentImages, *it.CommandID)
	}
	h.mu.Unlock()
	if err := openStore(sid).appendRecord(record{Item: it, Model: model, Images: images}); err != nil {
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
		h.runGen++
	case p.Status == msp.SessionStatusIdle:
		// Idle closes a turn only when one was running; a session sitting idle from the
		// start has no turn to end. It shows the turn as done but does not release the queue:
		// the host sends idle BEFORE turn/completed (measured on 1.4.0, same millisecond), so
		// releasing here would start the next turn ahead of the completion that ends this one.
		// settleIdle is the fallback for a completion that never comes.
		if h.running {
			h.state = agents.TurnCompleted
			gen := h.runGen
			time.AfterFunc(idleSettleGrace, func() { h.settleIdle(gen) })
		}
	case p.Status == msp.SessionStatusNotLoaded:
		h.state = agents.TurnUnknown
	}
	st := h.state
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

// idleSettleGrace is how long an idle status waits for its turn/completed before settleIdle
// closes the turn by itself. The two arrive together on a healthy host, so this only bounds
// how long a lost completion can hold the queue.
var idleSettleGrace = 3 * time.Second

// settleIdle closes a turn that went idle without a turn/completed, and releases the queue.
func (h *threadHandle) settleIdle(gen uint64) {
	h.mu.Lock()
	if !h.running || h.runGen != gen {
		h.mu.Unlock()
		return
	}
	h.running, h.turnID, h.turnModel = false, "", ""
	h.state = agents.TurnCompleted
	h.settleHeadLocked()
	h.dropResumedLocked(true)
	h.mu.Unlock()
	agents.MarkTurnEnd(h.slotSid, agents.TurnCompleted)
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnCompleted})
	h.pump()
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
	// A completion for a turn this handle no longer tracks (settleIdle already closed it and
	// the queue has moved on) must not end the turn that replaced it.
	if p.TurnID != "" && (h.turnID != "" || h.starting != "") && p.TurnID != h.turnID && p.TurnID != h.starting {
		h.mu.Unlock()
		return
	}
	if p.TurnID != "" && p.TurnID == h.starting {
		h.starting = "" // ended before it was ever reported started
	}
	h.settleHeadLocked()
	h.dropResumedLocked(true)
	h.running, h.state, h.turnID = false, st, ""
	h.mu.Unlock()
	agents.MarkTurnEndErr(h.slotSid, st, failure)
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
	// Never on this goroutine: finishTurn runs on the client's reader, and pump waits for the
	// turn/start ack that only the reader can deliver — the reader would stall until the call
	// timed out, with every notification behind it.
	go h.pump()
}

// settleHeadLocked releases the head once the turn it became has ended. A head still waiting
// for its turn/started (a completion of a turn this handle did not start, ahead of it in the
// host's queue) stays. Caller holds h.mu.
func (h *threadHandle) settleHeadLocked() {
	if t := h.tq().Head(); t != nil && h.starting == "" {
		h.tq().Settle(t)
	}
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
func (h *threadHandle) Send(in agents.TurnInput) error {
	_, err := h.accept(in, false)
	return err
}

// SendQueued is Send reporting whether the input was held behind a running turn.
func (h *threadHandle) SendQueued(in agents.TurnInput) (bool, error) { return h.accept(in, false) }

// Steer injects input into the RUNNING turn. MSP carries it natively, so unlike the ACP kinds
// this is not a queue in disguise — but a steer with no turn to steer is a plain send.
func (h *threadHandle) Steer(in agents.TurnInput) error {
	_, err := h.accept(in, true)
	return err
}

// accept starts, steers or queues the input. queued reports that it waits behind a running
// turn: in this driver's queue, or in the host's own (disposition "queued").
//
// Starting at once still goes through the queue (Accept, then Take): input whose turn/start is
// out while no other turn runs is the turn a first stop stops (ADR 0105 decision 1), and the
// queue is where that rule lives.
func (h *threadHandle) accept(in agents.TurnInput, steer bool) (queued bool, err error) {
	h.mu.Lock()
	if !h.alive || h.cl == nil {
		h.mu.Unlock()
		return false, errors.New("Muse Code のホストが起動していません")
	}
	running, turnID := h.running, h.turnID
	if steer && running && turnID != "" {
		// A native steer bypasses the queue, but not its resend check and ledger record, and new
		// member input ends a stop episode however it is delivered.
		if _, dup := h.tq().AcceptOutside(in); dup {
			h.mu.Unlock()
			return false, nil // a resend after a reconnect must not steer twice
		}
		h.mu.Unlock()
		return false, h.steerNow(in, turnID)
	}
	if _, dup := h.tq().Accept(in); dup {
		h.mu.Unlock()
		return false, nil // a resend after a reconnect must not start a second turn
	}
	// Behind a running turn, a turn/start still out, or older queued input: it waits. The last
	// case is a queue a lost host left behind; checking only running would let this input start
	// at once, ahead of it.
	if running || h.tq().Head() != nil || h.tq().Len() > 1 {
		h.mu.Unlock()
		h.pump()
		return true, nil
	}
	t := h.tq().Take()
	id := h.commitLocked(t)
	h.mu.Unlock()

	queued, err = h.launch(t, id)
	if err != nil {
		// The caller is told, so the input is not kept for a retry it does not know about.
		h.startFailed(t, id, err, false)
		go h.pump() // input queued behind the failed start would otherwise wait for the next Send
	}
	return queued, err
}

// commitLocked commits the taken head and marks its turn/start out, returning the commandId.
// Nothing waits between taking and sending, so the two share one critical section: from here a
// stop reaches the input only through the turn it becomes. Caller holds h.mu.
func (h *threadHandle) commitLocked(t *agents.Taken) string {
	h.tq().Commit(t)
	h.starting = msp.NewCommandID()
	h.calling = true
	return h.starting
}

// launch sends the head's turn/start and records the host's answer. queued is the host's own
// answer that the input waits behind a turn running there (disposition "queued"): one this
// handle did not start, since its own turns keep it busy until turn/started.
func (h *threadHandle) launch(t *agents.Taken, id string) (queued bool, err error) {
	disp, err := h.startTurn(t.In, id)
	h.mu.Lock()
	h.calling = false
	var stop bool
	redirectTo, redirect := "", false
	if err == nil && h.starting == id {
		switch disp {
		case msp.TurnStartDispositionSteered:
			// It joined a turn already running, so no turn/started of its own will come. A stop
			// aimed at it is a stop of that turn.
			h.starting = ""
			stop = h.tq().Received(t)
			h.tq().Settle(t)
		case msp.TurnStartDispositionQueued:
			// Held in the host's queue behind a turn this handle did not start: queued, not the
			// turn being stopped, so a first stop lets it continue (decision 1). A first stop that
			// came before this answer took the input for the turn being started; it belongs to
			// the turn running ahead of it, and goes there the way Interrupt stops that turn.
			if h.tq().Hold(t, true) {
				redirect, redirectTo = true, h.turnID
			}
		}
	}
	h.mu.Unlock()
	if stop {
		_ = h.interruptTurn("")
	}
	if redirect {
		_ = h.interruptTurn(redirectTo)
	}
	return disp == msp.TurnStartDispositionQueued, err
}

// startFailed settles the head after its turn/start failed. requeue puts it back at the front
// when the host went away with it, for the respawn to start. started reports that the turn
// exists after all (turn/started arrived before the error): its completion settles it.
func (h *threadHandle) startFailed(t *agents.Taken, id string, err error, requeue bool) (started bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// A turn that did not start never echoes a userMessage, and a requeued one goes out again
	// under a new commandId, so this id's images would never be collected.
	defer func() {
		if !started {
			delete(h.sentImages, id)
		}
	}()
	if h.tq().Head() != t {
		return false
	}
	hostGone := !h.alive || h.cl == nil || errors.Is(err, msp.ErrClosed)
	if h.starting != id && !hostGone {
		return true
	}
	if h.starting == id {
		h.starting = ""
	}
	if hostGone && requeue {
		h.tq().Requeue(t) // false: a stop was waiting for it, and it is not sent again
		return false
	}
	h.tq().Settle(t)
	return false
}

// startTurn submits a turn under commandId id and returns the host's disposition.
func (h *threadHandle) startTurn(in agents.TurnInput, id string) (msp.TurnStartDisposition, error) {
	h.mu.Lock()
	cl, sid, effort := h.cl, h.sid, h.settings.Effort
	h.mu.Unlock()
	if cl == nil {
		return "", errors.New("Muse Code のホストが起動していません")
	}
	parts, images := inputPartsImages(in)
	h.noteImages(id, images)
	params := msp.TurnStartParams{
		CommandID: id,
		SessionID: sid,
		Input:     skillPart(cl, sid, parts),
	}
	if e := reasoningEffort(effort); e != nil {
		params.ReasoningEffort = e
	}
	raw, err := cl.Call(msp.MethodTurnStart, params, callTimeout)
	if err != nil {
		return "", err
	}
	// Read leniently: the turn is already admitted, so an answer without a disposition (or in
	// another shape) must not turn it into a failed send. It reads as started, as before.
	// turn/started follows as a notification and is what actually moves the state; marking
	// running here would race it into a stuck "working" if the host refused the turn after
	// accepting the command.
	var res struct {
		Disposition msp.TurnStartDisposition `json:"disposition"`
	}
	_ = json.Unmarshal(raw, &res)
	return res.Disposition, nil
}

func (h *threadHandle) steerNow(in agents.TurnInput, turnID string) error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("Muse Code のホストが起動していません")
	}
	id := msp.NewCommandID()
	parts, images := inputPartsImages(in)
	h.noteImages(id, images)
	err := cl.CallInto(msp.MethodTurnSteer, msp.TurnSteerParams{
		CommandID:      id,
		SessionID:      sid,
		ExpectedTurnID: turnID,
		Input:          skillPart(cl, sid, parts),
	}, callTimeout, nil)
	if err != nil {
		h.mu.Lock()
		delete(h.sentImages, id) // a refused steer echoes nothing
		h.mu.Unlock()
	}
	return err
}

// noteImages remembers which paths went out as image parts under commandId id, for onItem to
// stamp on the `userMessage` the host echoes back. Noted before the call rather than after:
// the item can arrive before the call returns.
func (h *threadHandle) noteImages(id string, images []string) {
	if len(images) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sentImages == nil {
		h.sentImages = map[string][]string{}
	}
	h.sentImages[id] = images
}

// pump starts the next queued turn once the previous one has settled. It is safe to call at
// any time: while a turn runs or a start is out it does nothing.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if !h.alive || h.cl == nil || h.running || h.tq().Head() != nil {
			h.mu.Unlock()
			return
		}
		t := h.tq().Take()
		if t == nil {
			h.mu.Unlock()
			return
		}
		id := h.commitLocked(t)
		h.mu.Unlock()
		_, err := h.launch(t, id)
		if err == nil {
			return
		}
		h.mu.Lock()
		hostGone := !h.alive || h.cl == nil || errors.Is(err, msp.ErrClosed)
		h.mu.Unlock()
		// The host went away with the input: it goes back to the head of the queue for the
		// respawn to start, rather than being lost to a crash nobody saw.
		if h.startFailed(t, id, err, true) || hostGone {
			return // or the turn started after all (a late ack); its completion pumps the rest
		}
		// Refused by a live host: resending the same input would be refused again, so it is
		// dropped — but as a failed turn the member can see, not a log line — and the queue
		// moves on.
		log.Printf("muse: %s: queued turn failed to start: %v", h.name, err)
		h.mu.Lock()
		h.state = agents.TurnFailed
		h.mu.Unlock()
		agents.MarkTurnEndErr(h.slotSid, agents.TurnFailed, err.Error())
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnFailed})
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
	parts, _ := inputPartsImages(in)
	return parts
}

// inputPartsImages is inputParts plus the paths that became `image` parts — only those: a
// path that fell back to a text part is already in the item's text, and listing it again
// would show it twice.
func inputPartsImages(in agents.TurnInput) ([]msp.TurnInputPart, []string) {
	parts := []msp.TurnInputPart{{Type: msp.TurnInputPartTypeText, Text: strPtr(in.Prompt)}}
	var images []string
	for _, a := range in.Attachments {
		if strings.TrimSpace(a) == "" {
			continue
		}
		if p, ok := imagePart(a); ok {
			parts = append(parts, p)
			images = append(images, a)
			continue
		}
		parts = append(parts, msp.TurnInputPart{Type: msp.TurnInputPartTypeText, Text: strPtr(a)})
	}
	return parts, images
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

// Interrupt is the Console's stop (ADR 0105): the queue decides what the stop is and what
// happens to the queued input; this delivers it to the host. A first stop ends the running turn
// and the queue continues; a second one (or DiscardQueue) also discards what is still queued.
func (h *threadHandle) Interrupt(opts agents.InterruptOpts) (agents.InterruptResult, error) {
	return h.interrupt(opts, false)
}

// RemoveQueued takes a queued entry out while it is cancellable (decision 5). Input the host
// holds (disposition "queued") is not: turn/unqueue is unmeasured.
func (h *threadHandle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().Remove(id)
}

// DismissDiscard drops a kept discard once the member restored or dismissed it (decision 4).
func (h *threadHandle) DismissDiscard(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().DismissDiscard(id)
}

// interruptAll is the stop for teardown (DropHandle, Agent shutdown): the whole queue goes and
// nothing is kept for return (decision 8), because it would be started on the host being shut
// down.
func (h *threadHandle) interruptAll() error {
	_, err := h.interrupt(agents.InterruptOpts{DiscardQueue: true}, true)
	return err
}

func (h *threadHandle) interrupt(opts agents.InterruptOpts, teardown bool) (agents.InterruptResult, error) {
	h.mu.Lock()
	if teardown {
		h.tq().DropAll()
	}
	turnID, running := h.turnID, h.running
	out := h.tq().Interrupt(opts, running)
	pending := out.Head == agents.HeadStopPending
	if pending && running && out.Result.Stop == agents.StopFirst {
		// The head's turn/start is out while a turn already runs on the host, so the host will
		// queue it behind that turn: this first stop is the running turn's, delivered below, and
		// the head continues. Holding it now takes the pending stop off it, so neither its
		// "queued" answer (launch's redirect) nor its turn/started delivers the same stop again.
		if t := h.tq().Head(); t != nil && h.tq().Hold(t, true) {
			pending = false
		}
	}
	cancelled := out.Head == agents.HeadCancelled && !running
	switch {
	case cancelled:
		h.state = agents.TurnCancelled
	case running || pending:
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if cancelled {
		// Accepted into an idle host but not yet taken by the pump: the input was the turn being
		// started, it never becomes one, and there is nothing on the host to interrupt.
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnCancelled})
		return out.Result, nil
	}
	if pending && !running {
		// The head's turn/start is out and no turn exists yet: a bare turn/interrupt would find
		// nothing to stop. turn/started delivers it.
		return out.Result, nil
	}
	// Everything else stops the turn that runs: the head's own, or one this handle did not start
	// (taken over, or the one the host queued the head behind).
	return out.Result, h.interruptTurn(turnID)
}

// interruptTurn sends turn/interrupt, for turnID when known.
func (h *threadHandle) interruptTurn(turnID string) error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
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
		var err error
		if model, err = SafeDefaultModel(cl); err != nil {
			return err
		}
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
	switch {
	case ask.isApproval():
		err = h.decideApproval(cl, sid, ask, reply)
	case reply.Decision == agents.DecisionCancel || reply.Decision == agents.DecisionDeny:
		// Declining a question is the runtime's own refusal (ADR 0105 decision 7): the tool call
		// resolves as cancelled and the turn goes on, so the queue is not touched. Answering
		// every question with nothing instead would read to the model as a real answer.
		err = cl.CallInto(msp.MethodUserInputCancel, msp.UserInputCancelParams{
			CommandID:   msp.NewCommandID(),
			SessionID:   sid,
			UserInputID: ask.userInputID,
		}, callTimeout, nil)
	default:
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

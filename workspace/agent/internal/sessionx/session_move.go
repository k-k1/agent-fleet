package sessionx

// Moving a session to another agent kind (ADR 0115, P2a): POST /sessions/{name}/move closes the
// source's input, snapshots it, writes the handover file into the target's throwaway directory,
// creates the target with a first prompt that points at the file, and only then stops the
// source. The source-side transitions are session_move_gate.go's; the file is
// session_handover*.go's. This file orders them and answers for the outcome.
//
// A move that fails before a target can exist rolls back: the file goes, input opens again and a
// usage-limit booking spent meanwhile is booked again. A create that may have started a target
// is not guessed at: the source stays closed with its file, and the user picks retry (the same
// file, the same target name) or cancel. Both are recorded in moveRecords, so they survive an
// Agent restart that happens mid-move.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// moveTargetKinds are the kinds a move may target: each can read a file outside its working
// folder without a prompt, which is where the handover lives (decision 9; the measurements are
// in its 2026-10-20 addendum). The list is per kind, not per driver: a driver measured for a kind
// speaks for both of its drivers unless the addendum says otherwise.
var moveTargetKinds = []string{
	session.KindClaude, session.KindCodex, session.KindOpencode,
	session.KindCopilot, session.KindAgy, session.KindMuse, session.KindKiro,
}

// moveUnmeasuredTargets stay off until the read has been measured through the real driver; the
// bare CLI's result does not cover the driver's trust handling and flags. Moving a kind into
// moveTargetKinds is the whole change once that measurement exists.
var moveUnmeasuredTargets = []string{session.KindCursor}

// Reasons a kind is not offered as a target, as the preview reports them.
const (
	moveTargetSameKind     = "same_kind"        // decision 5: same quota; fork where the kind can
	moveTargetCannotRead   = "cannot_read_file" // lcpp: its file tools refuse paths outside the working folder
	moveTargetNotMeasured  = "not_measured"     // moveUnmeasuredTargets
	moveTargetNotAnAgent   = "not_an_agent"     // shell, ssm: nothing that reads a file
	moveWarnCursorMirror   = "managed_cursor_mirror_empty"
	moveWarnChildrenActive = "children_running"
)

// moveTargetReason is "" when target may receive a move from source, else why not.
func moveTargetReason(source, target string) string {
	switch {
	case target == source:
		return moveTargetSameKind
	case slices.Contains(moveTargetKinds, target):
		return ""
	case slices.Contains(moveUnmeasuredTargets, target):
		return moveTargetNotMeasured
	case target == session.KindLcpp:
		return moveTargetCannotRead
	}
	return moveTargetNotAnAgent
}

// moveCanFork reports whether a same-kind move should point at fork instead (decision 5).
func moveCanFork(kind string) bool {
	ag := AgentOf(kind)
	_, ok := ag.(agents.Forker)
	return ok && ag.Caps().CanFork
}

// moveRecord is a move between begin and its end, on disk: what retry needs to create the
// target again from the same file, and what cancel needs to remove.
type moveRecord struct {
	Target  string   `json:"target"`
	Kind    string   `json:"kind"`
	Model   string   `json:"model,omitempty"`
	Driver  string   `json:"driver,omitempty"`
	File    string   `json:"file,omitempty"`
	Earlier []string `json:"earlier,omitempty"`
	// PeerIDs are the spooled peer messages the file carries, taken out of the spool at commit.
	PeerIDs []string `json:"peerIds,omitempty"`
	// State is moveRunning while a request drives it, moveUncertain once the create may have
	// started a target, moveCommitted once movedTo is written and the steps after it remain. A
	// running record found with no request driving it (moveOpLock free) is an Agent that
	// stopped mid-move, which is uncertain too.
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
}

const (
	moveRunning   = "running"
	moveUncertain = "uncertain"
	// moveCommitted: movedTo is on the meta; the peer messages and the source's stop may not be.
	moveCommitted = "committed"
)

var moveRecords = fstore.JSON[moveRecord](paths.AgentStateDir, "session-move", ".json")

// moveOps keeps one operation per source at a time (move, retry, cancel); a second is refused
// rather than queued, because each of them can take a create's whole launch.
var moveOps sync.Map // source name → *sync.Mutex

func moveOpLock(name string) (unlock func(), ok bool) {
	v, _ := moveOps.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

func moveOpBusy(name string) bool {
	unlock, ok := moveOpLock(name)
	if ok {
		unlock()
	}
	return !ok
}

// --- errors ----------------------------------------------------------------------------

// moveError is a move's refusal or failure as the API answers it.
type moveError struct {
	Status  int
	Code    string
	Message string
	Target  string
	File    string
	State   string
	CanFork bool
}

func (e *moveError) Error() string { return e.Message }

// moveErrWire is httpx.WriteErr's shape plus what the dialog needs to offer the next step.
type moveErrWire struct {
	Error moveErrBody `json:"error"`
}

type moveErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Target  string `json:"target,omitempty"`
	File    string `json:"file,omitempty"`
	State   string `json:"state,omitempty"`
	CanFork bool   `json:"canFork,omitempty"`
}

// asMoveError maps every error a move step returns onto its answer.
func asMoveError(err error) *moveError {
	var me *moveError
	var moved *lifegate.MovedError
	var st *moveStateError
	switch {
	case errors.As(err, &me):
		return me
	case errors.As(err, &moved):
		return &moveError{Status: http.StatusConflict, Code: errCodeSessionMoved, Message: moved.Error(), Target: moved.Target}
	case errors.As(err, &st):
		return &moveError{Status: http.StatusConflict, Code: "move_source_state", Message: st.Error(), State: st.State}
	case errors.Is(err, errMoveCursorStopped):
		return &moveError{Status: http.StatusConflict, Code: "move_cursor_stopped", Message: err.Error()}
	case errors.Is(err, errMoveSourceGone):
		return &moveError{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()}
	case errors.Is(err, ErrHandoverNoTurns):
		return &moveError{Status: http.StatusConflict, Code: "move_empty_transcript", Message: err.Error()}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &moveError{Status: http.StatusServiceUnavailable, Code: "session_gate", Message: err.Error()}
	}
	return &moveError{Status: http.StatusInternalServerError, Code: "move_failed", Message: err.Error()}
}

func writeMoveErr(w http.ResponseWriter, err error) {
	e := asMoveError(err)
	httpx.WriteJSON(w, e.Status, moveErrWire{Error: moveErrBody{
		Code: e.Code, Message: e.Message, Target: e.Target, File: e.File, State: e.State, CanFork: e.CanFork,
	}})
}

// --- the move --------------------------------------------------------------------------

// moveRequest is the body of POST /sessions/{name}/move.
type moveRequest struct {
	Kind   string `json:"kind"`
	Model  string `json:"model,omitempty"`
	Driver string `json:"driver,omitempty"`
}

// sessionMoveWire answers a move (or a retry) that created its target.
type sessionMoveWire struct {
	Source string          `json:"source"`
	Target json.RawMessage `json:"target"` // the created session, as POST /sessions answers it
	File   string          `json:"file"`
	// Earlier are the handover files copied forward from the source (decision 9's chain).
	Earlier   []string `json:"earlier,omitempty"`
	SizeBytes int      `json:"sizeBytes,omitempty"`
	Cut       int      `json:"cut,omitempty"`
	// SourceStopped is false when the stop failed after the commit: the source is closed and
	// moved either way, and the user can stop it from its row.
	SourceStopped bool   `json:"sourceStopped"`
	StopError     string `json:"stopError,omitempty"`
	// Delivery is set while the target's first prompt is not known to have arrived (pending on
	// a Terminal target, whose prompt is typed once its CLI has booted; failed; unknown).
	Delivery *moveDeliveryWire `json:"delivery,omitempty"`
}

// Seams for tests: the create of the target, and the stop of the source after the commit.
var (
	createMoveTarget = func(ctx context.Context, req CreateReq) (status int, body []byte) {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("move: creating %s panicked: %v", req.name, p)
				status = 0 // unknown: whether a target started is not known
			}
		}()
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/sessions", nil)
		c := &captureWriter{header: http.Header{}}
		createSession(c, r, req)
		return c.status, c.body.Bytes()
	}
	stopMoveSource = func(m session.Meta) error {
		_, err := haltSessionMeta(m)
		return err
	}
	writeMoveHandover   = WriteHandoverFile
	writeMoveTargetMeta = session.WriteMetaChecked
)

// HandleSessionMove moves a session to another agent kind. POST /sessions/{name}/move
// {kind, model?, driver?}.
func HandleSessionMove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req moveRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	src, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if err := checkMoveRequest(src, req); err != nil {
		writeMoveErr(w, err)
		return
	}
	unlock, ok := moveOpLock(name)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "move_busy", "a move of this session is already running")
		return
	}
	defer unlock()
	res, err := runMove(r.Context(), src, req, time.Now())
	if err != nil {
		writeMoveErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// checkMoveRequest refuses what no state of the source could make possible.
func checkMoveRequest(src session.Meta, req moveRequest) error {
	if !AgentOf(src.Kind).Caps().CanTranscript {
		return &moveError{Status: http.StatusBadRequest, Code: "move_unsupported_source",
			Message: "only a session with a readable conversation can be moved"}
	}
	if _, ok := RegisteredAgent(req.Kind); !ok {
		return &moveError{Status: http.StatusBadRequest, Code: "bad_kind", Message: "unknown target kind: " + req.Kind}
	}
	switch moveTargetReason(src.Kind, req.Kind) {
	case "":
	case moveTargetSameKind:
		msg := "a move needs another agent kind; this one draws on the same account"
		if moveCanFork(src.Kind) {
			msg += " (fork the session to continue in the same kind)"
		}
		return &moveError{Status: http.StatusBadRequest, Code: "move_same_kind", Message: msg, CanFork: moveCanFork(src.Kind)}
	default:
		return &moveError{Status: http.StatusBadRequest, Code: "move_target_unsupported",
			Message: req.Kind + " cannot be a move target: it cannot read the handover file"}
	}
	if !session.DirExists(src.Dir) {
		return &moveError{Status: http.StatusBadRequest, Code: "move_missing_dir", Message: "the working folder does not exist"}
	}
	return nil
}

// runMove is the move itself, called with the source's operation lock held.
func runMove(ctx context.Context, src session.Meta, req moveRequest, now time.Time) (*sessionMoveWire, error) {
	target := bookMoveTarget(src.Dir)
	mv, err := beginSessionMove(ctx, src.Name, target)
	if err != nil {
		releaseMoveTarget(target)
		return nil, err
	}
	// Input is closed from here on. Nothing below may be cut short by the caller going away:
	// a half-done move is what the rollback and the retry exist for, not something to add to.
	ctx = context.WithoutCancel(ctx)
	rec := moveRecord{Target: target, Kind: req.Kind, Model: req.Model, Driver: req.Driver,
		State: moveRunning, StartedAt: now.Format(time.RFC3339)}
	if err := moveRecords.Write(src.Name, rec); err != nil {
		return nil, abortMove(mv, rec, now, fmt.Errorf("recording the move: %w", err))
	}
	m, ok := session.ReadMeta(src.Name)
	if !ok {
		return nil, abortMove(mv, rec, now, errMoveSourceGone)
	}
	in := readMoveSource(m)
	// The chain is copied first so the header can name what was copied (decision 9).
	earlier, err := CopyHandoverChain(src.Name, target)
	rec.Earlier = earlier
	if err != nil {
		return nil, abortMove(mv, rec, now, fmt.Errorf("copying the earlier handover files: %w", err))
	}
	in.Earlier = earlier
	in.Instructions = instructionGap(m.Dir, m.Kind, req.Kind)
	in.Now, in.Loc = now, time.Local
	h, err := BuildHandover(in)
	if err != nil {
		return nil, abortMove(mv, rec, now, err)
	}
	md, h := RenderHandover(h, "")
	file, err := writeMoveHandover(target, src.Name, md)
	if err != nil {
		return nil, abortMove(mv, rec, now, &moveError{Status: http.StatusInternalServerError, Code: "move_write_failed",
			Message: "writing the handover file: " + err.Error()})
	}
	rec.File = file
	for _, p := range in.PeerMessages {
		rec.PeerIDs = append(rec.PeerIDs, p.ID)
	}
	if err := moveRecords.Write(src.Name, rec); err != nil {
		return nil, abortMove(mv, rec, now, fmt.Errorf("recording the move: %w", err))
	}
	res, err := createAndCommit(ctx, mv, m, rec, now, false)
	if err != nil {
		return nil, err
	}
	res.SizeBytes, res.Cut = len(md), h.Cut
	return res, nil
}

// createAndCommit creates the target and commits the move once the target's meta is on disk. A
// create that answered and left nothing under the target's name rolls back (unless this is a
// retry, which the user can still cancel); anything else that may have started a target leaves
// the move pending. A 2xx alone is not enough: the meta is what lists the target and what
// retry, cancel and a restart find it by.
func createAndCommit(ctx context.Context, mv *sourceMove, src session.Meta, rec moveRecord, now time.Time, retry bool) (*sessionMoveWire, error) {
	req := moveCreateReq(src, rec)
	// The first prompt is recorded before the create: a target whose prompt never arrives is
	// still pointed at its file by the deliver step (HandleSessionMoveDeliver).
	if err := moveDeliveries.Write(rec.Target, moveDelivery{Source: mv.source, File: rec.File, Prompt: req.InitialPrompt}); err != nil {
		if !retry {
			return nil, abortMove(mv, rec, now, fmt.Errorf("recording the target's first prompt: %w", err))
		}
		return nil, fmt.Errorf("recording the target's first prompt: %w", err)
	}
	status, body := createMoveTarget(ctx, req)
	presence := moveTargetPresence(rec)
	if status >= 200 && status < 300 && presence == targetRecorded {
		return finishMove(mv, rec, body)
	}
	cause := createErrorOf(status, body)
	if status >= 200 && status < 300 {
		cause = &moveError{Status: http.StatusBadGateway, Code: errCodeMetaWriteFailed,
			Message: "the target started but its meta is not on disk"}
	}
	refused := status >= 300 && cause.Code != errCodeMetaWriteFailed && presence == targetAbsent
	if refused && !retry {
		return nil, abortMove(mv, rec, now, cause)
	}
	rec.State, rec.Error = moveUncertain, cause.Message
	_ = moveRecords.Write(mv.source, rec)
	if refused {
		cause.Target, cause.File = rec.Target, rec.File
		return nil, cause
	}
	log.Printf("move: %s: the create of %s may have started it (%d %s); the move waits for retry or cancel",
		mv.source, rec.Target, status, cause.Code)
	return nil, &moveError{Status: http.StatusBadGateway, Code: "move_create_uncertain",
		Message: "the target may have started; retry the move or cancel it (" + cause.Message + ")",
		Target:  rec.Target, File: rec.File}
}

// errCodeMetaWriteFailed: a move's target started (a runtime or a pane) but its meta could not
// be written, so it may exist without being listed.
const errCodeMetaWriteFailed = "meta_write_failed"

// finishMove commits a move whose target is recorded, then completes it. A commit that fails
// leaves the move pending: the target exists, so retry commits it.
func finishMove(mv *sourceMove, rec moveRecord, target []byte) (*sessionMoveWire, error) {
	if m, ok := session.ReadMeta(mv.source); !ok || m.MovedTo != rec.Target {
		if err := mv.commit(); err != nil {
			rec.State, rec.Error = moveUncertain, err.Error()
			_ = moveRecords.Write(mv.source, rec)
			return nil, &moveError{Status: http.StatusInternalServerError, Code: "move_commit_failed",
				Message: "the target exists but the move could not be recorded; retry it: " + err.Error(),
				Target:  rec.Target, File: rec.File}
		}
	}
	// The record outlives the commit until the steps below are done: an Agent that stops in
	// between finds movedTo plus this record and finishes them (RecoverMoves, retry, reopen).
	rec.State, rec.Error = moveCommitted, ""
	_ = moveRecords.Write(mv.source, rec)
	return completeMove(mv.source, rec, target), nil
}

// completeMove is what follows the commit (decision 11.5): the spooled peer messages leave the
// spool (they are in the file; their senders are not told), the source is stopped, and the
// record and the booked name go. Every step may run again after a restart.
func completeMove(source string, rec moveRecord, target []byte) *sessionMoveWire {
	for _, id := range rec.PeerIDs {
		takePending(source, id)
	}
	res := &sessionMoveWire{Source: source, Target: target, File: rec.File, Earlier: rec.Earlier, SourceStopped: true}
	if m, ok := session.ReadMeta(source); ok {
		if err := stopMoveSource(m); err != nil {
			res.SourceStopped, res.StopError = false, err.Error()
			log.Printf("move: %s: moved to %s but could not be stopped: %v", source, rec.Target, err)
		}
	}
	moveRecords.Remove(source)
	releaseMoveTarget(rec.Target)
	res.Delivery = moveDeliveryState(rec.Target)
	return res
}

// abortMove rolls back a move that created no target and returns cause. If the rollback itself
// fails, the move stays pending (cancel can try again), and that is what is answered.
func abortMove(mv *sourceMove, rec moveRecord, now time.Time, cause error) error {
	err := RemoveHandoverFiles(rec.Target, append(slices.Clone(rec.Earlier), HandoverFileName(mv.source)))
	if err == nil {
		err = mv.rollback(now)
	}
	if err != nil {
		rec.State, rec.Error = moveUncertain, err.Error()
		_ = moveRecords.Write(mv.source, rec)
		log.Printf("move: %s: rolling back the move to %s failed: %v (after: %v)", mv.source, rec.Target, err, cause)
		return &moveError{Status: http.StatusInternalServerError, Code: "move_rollback_failed",
			Message: "the move failed and could not be undone; cancel it to try again: " + err.Error(),
			Target:  rec.Target, File: rec.File}
	}
	moveRecords.Remove(mv.source)
	moveDeliveries.Remove(rec.Target)
	releaseMoveTarget(rec.Target)
	return cause
}

// targetPresence is what exists under a move target's name.
type targetPresence int

const (
	targetAbsent     targetPresence = iota
	targetRecorded                  // its meta reads: a listed session
	targetUnreadable                // a meta file exists and does not read now (a rewrite in flight, damage)
	targetOrphan                    // no meta file, but a pane or a managed runtime under the name
)

// moveTargetPresence reads the target's state. The meta is read under the meta lock, so a
// rewrite by an ordinary writer is never seen half-done; a file that still does not read is
// unreadable, never absent: only a meta file that does not exist lets cancel treat a running
// pane or runtime as an orphan to stop. Nothing else writes a meta under a booked name while
// the move's operation lock is held, so the answer holds until the caller acts on it. A
// variable so tests need neither tmux nor a runtime.
var moveTargetPresence = func(rec moveRecord) targetPresence {
	var ok bool
	WithSessionMetaLock(func() { _, ok = session.ReadMeta(rec.Target) })
	if ok {
		return targetRecorded
	}
	if _, err := os.Lstat(session.MetaPath(rec.Target)); !errors.Is(err, fs.ErrNotExist) {
		return targetUnreadable
	}
	if tmuxx.HasSession(session.TmuxName(rec.Target)) ||
		rec.Kind != "" && ManagedAlive(session.Meta{Name: rec.Target, Kind: rec.Kind, Driver: session.DriverManaged}) {
		return targetOrphan
	}
	return targetAbsent
}

// stopOrphanTarget ends what an unrecorded create started, so that cancelling leaves one session
// on the working copy. Its first prompt is sent only after the meta is written, so an orphan has
// not been told to work.
var stopOrphanTarget = func(rec moveRecord) error {
	if tn := session.TmuxName(rec.Target); tmuxx.HasSession(tn) {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
	}
	if rec.Kind != "" {
		dropManagedRuntime(session.Meta{Name: rec.Target, Kind: rec.Kind, Driver: session.DriverManaged})
	}
	return nil
}

// listMoveRecords reads every pending move's record, keyed by source.
func listMoveRecords() map[string]moveRecord {
	ents, err := os.ReadDir(moveRecords.Dir())
	if err != nil {
		return nil
	}
	out := map[string]moveRecord{}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !session.ValidName(name) {
			continue
		}
		if rec, ok := moveRecords.Read(name); ok {
			out[name] = rec
		}
	}
	return out
}

// pendingMoveTargets are the names pending moves hold on disk, which no allocation may hand out.
func pendingMoveTargets() map[string]bool {
	out := map[string]bool{}
	for _, rec := range listMoveRecords() {
		out[rec.Target] = true
	}
	// A source's meta names its target from begin on, before the record is first written: an
	// Agent that stopped in between leaves only that.
	for _, m := range session.ListMetas() {
		if m.Moving != "" {
			out[m.Moving] = true
		}
	}
	return out
}

// RecoverMoves completes the moves a previous process committed and did not finish: their
// sources may still be running and their peer messages still spooled. Called once at boot.
func RecoverMoves() int {
	n := 0
	for name := range listMoveRecords() {
		unlock, ok := moveOpLock(name)
		if !ok {
			continue
		}
		if _, rec, phase, err := pendingMove(name); err == nil && phase == moveCommitted {
			completeMove(name, rec, nil)
			n++
		}
		unlock()
	}
	return n
}

// createErrorOf reads the create's error answer.
func createErrorOf(status int, body []byte) *moveError {
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &resp)
	e := &moveError{Status: status, Code: resp.Error.Code, Message: resp.Error.Message}
	if e.Status < 400 {
		e.Status = http.StatusBadGateway
	}
	if e.Code == "" {
		e.Code = "move_create_failed"
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("creating the target failed (%d)", status)
	}
	return e
}

// moveCreateReq is the target's create: the source's working copy and title, the requested
// kind, and the first prompt that points at the file.
func moveCreateReq(src session.Meta, rec moveRecord) CreateReq {
	return CreateReq{
		name: rec.Target, Kind: rec.Kind, Model: rec.Model, Driver: rec.Driver,
		Dir: src.Dir, Subdir: src.Subdir, Title: src.Title, Color: src.Color,
		InitialPrompt: moveInitialPrompt(src, rec.File),
	}
}

// moveInitialPrompt is the target's first prompt (decisions 1 and 15): where the file is, what it
// is, to read its head first, and never to open the source's own files.
func moveInitialPrompt(src session.Meta, file string) string {
	return fmt.Sprintf("This session continues work moved here from the session %s (%s), which has stopped.\n\n"+
		"Its whole record is the handover file %s. Before doing anything else, read the head of that "+
		"file, everything before its \"# Conversation\" heading: where the work stopped, the working "+
		"copy's state, the changed files and what is still open. Read further into the conversation "+
		"only when you need it.\n\n"+
		"That file is the only record. Never open the source session's own transcript or its CLI's "+
		"state directories.\n\n"+
		"Then continue from where it stopped. If what to do next is unclear, ask the user.",
		src.Name, src.Kind, file)
}

// --- retry, cancel, reopen ---------------------------------------------------------------

// HandleSessionMoveRetry finishes a pending move: it completes one that was committed, commits
// when the target is recorded, and otherwise creates it again under the same name from the same
// file. POST /sessions/{name}/move/retry.
func HandleSessionMoveRetry(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	unlock, ok := moveOpLock(name)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "move_busy", "a move of this session is already running")
		return
	}
	defer unlock()
	m, rec, phase, err := pendingMove(name)
	if err != nil {
		writeMoveErr(w, err)
		return
	}
	if phase == moveCommitted {
		httpx.WriteJSON(w, http.StatusOK, completeMove(name, rec, targetWire(rec.Target)))
		return
	}
	mv := &sourceMove{source: name, target: m.Moving}
	switch moveTargetPresence(rec) {
	case targetRecorded:
		res, err := finishMove(mv, rec, targetWire(rec.Target))
		if err != nil {
			writeMoveErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, res)
		return
	case targetUnreadable:
		writeMoveErr(w, &moveError{Status: http.StatusConflict, Code: "move_target_unreadable",
			Message: "the target's meta exists but cannot be read right now; try again", Target: rec.Target, File: rec.File})
		return
	case targetOrphan:
		// Creating again would collide with what is running under the name; finishing it would
		// need a meta nobody wrote. Cancel stops it and opens the source again.
		writeMoveErr(w, &moveError{Status: http.StatusConflict, Code: "move_target_orphaned",
			Message: "a target was started but never recorded; cancel the move to stop it, then move again",
			Target:  rec.Target, File: rec.File})
		return
	}
	if _, err := os.Stat(rec.File); rec.File == "" || err != nil {
		writeMoveErr(w, &moveError{Status: http.StatusConflict, Code: "move_file_missing",
			Message: "the handover file is gone; cancel the move and start it again", Target: rec.Target})
		return
	}
	rec.State, rec.Error = moveRunning, ""
	_ = moveRecords.Write(name, rec)
	res, err := createAndCommit(context.WithoutCancel(r.Context()), mv, m, rec, time.Now(), true)
	if err != nil {
		writeMoveErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// targetWire is a recorded target as POST /sessions answers it.
func targetWire(name string) json.RawMessage {
	t, ok := session.ReadMeta(name)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(wireSession(t, SessionAlive(t)))
	return b
}

// pendingMove is a source's unfinished move and its phase: moveRunning (moving, with a record
// naming the same target) or moveCommitted (movedTo, and the record of that move still there).
func pendingMove(name string) (session.Meta, moveRecord, string, error) {
	m, ok := session.ReadMeta(name)
	if !ok {
		return m, moveRecord{}, "", errMoveSourceGone
	}
	rec, hasRec := moveRecords.Read(name)
	switch {
	case m.Moving != "":
		if !hasRec || rec.Target != m.Moving {
			return m, moveRecord{}, "", &moveError{Status: http.StatusConflict, Code: "move_record_missing",
				Message: "the record of this move is gone; cancel it", Target: m.Moving}
		}
		return m, rec, moveRunning, nil
	case m.MovedTo != "" && hasRec && rec.Target == m.MovedTo:
		return m, rec, moveCommitted, nil
	case m.MovedTo != "":
		return m, moveRecord{}, "", &moveError{Status: http.StatusConflict, Code: "move_not_pending",
			Message: "this session was already moved to " + m.MovedTo, Target: m.MovedTo}
	}
	return m, moveRecord{}, "", &moveError{Status: http.StatusConflict, Code: "move_not_pending",
		Message: "this session has no move to finish"}
}

// HandleSessionMoveCancel abandons a pending move whose target is not recorded: an orphan the
// create left is stopped, the file goes and the source's input opens again, as in a rollback.
// POST /sessions/{name}/move/cancel.
func HandleSessionMoveCancel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	unlock, ok := moveOpLock(name)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "move_busy", "a move of this session is already running")
		return
	}
	defer unlock()
	m, rec, phase, err := pendingMove(name)
	var me *moveError
	if err != nil && !(errors.As(err, &me) && me.Code == "move_record_missing") {
		writeMoveErr(w, err)
		return
	}
	if phase == moveCommitted {
		writeMoveErr(w, &moveError{Status: http.StatusConflict, Code: "move_not_pending",
			Message: "this session was already moved to " + m.MovedTo, Target: m.MovedTo})
		return
	}
	target := m.Moving
	if rec.Target == "" {
		rec.Target = target
	}
	presence := moveTargetPresence(rec)
	if presence == targetRecorded || presence == targetUnreadable {
		// Two sessions on one working copy is what the move exists to prevent: the target has to
		// go first, or the move be finished with retry.
		writeMoveErr(w, &moveError{Status: http.StatusConflict, Code: "move_target_exists",
			Message: "the target session exists; finish the move with retry, or delete the target first", Target: target})
		return
	}
	if presence == targetOrphan {
		if err := stopOrphanTarget(rec); err != nil {
			writeMoveErr(w, &moveError{Status: http.StatusInternalServerError, Code: "move_orphan_stop_failed",
				Message: "could not stop the unrecorded target: " + err.Error(), Target: target})
			return
		}
		log.Printf("move: %s: stopped the unrecorded target %s before cancelling", name, target)
	}
	if err := abortMove(&sourceMove{source: name, target: target}, rec, time.Now(), nil); err != nil {
		writeMoveErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, moveCancelWire{Cancelled: name, Target: target})
}

// HandleSessionMoveReopen is "resume here anyway" (decision 11.6): the user takes the moved
// source back. It runs under the same operation lock as the move, so it never opens input
// between a commit and the source's stop; a committed move left unfinished is completed first,
// so a peer message the target already has is never delivered to the source again.
// POST /sessions/{name}/move/reopen.
func HandleSessionMoveReopen(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	unlock, ok := moveOpLock(name)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "move_busy", "a move of this session is still running")
		return
	}
	defer unlock()
	if _, rec, phase, err := pendingMove(name); err == nil && phase == moveCommitted {
		completeMove(name, rec, nil)
	}
	if err := reopenMovedSession(name); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "move_not_moved", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, moveReopenWire{Reopened: name})
}

type moveCancelWire struct {
	Cancelled string `json:"cancelled"`
	Target    string `json:"target"`
}

type moveReopenWire struct {
	Reopened string `json:"reopened"`
}

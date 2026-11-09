package memconsol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
)

// Manual run (#3702; ADR 0130 decision 6, 2026-11-09 note). The member starts one from the Console
// when the workspace is never idle. It skips the idle check and MinInterval and is not deferred by
// other sessions' turns; the switch, the per-run token budget, validation, no revival and
// approval-only publishing are the ones of an automatic run. The only way in is the signed route
// below: there is no MCP tool and no unsigned path, so a session cannot start one.

// Refusals of StartManual; the handler maps them to 409 codes.
var (
	ErrRunInFlight = errors.New("a consolidation run is already in flight")
	ErrDisabled    = errors.New("memory consolidation is switched off")
)

// Codes of the 409 answers.
const (
	CodeRunInFlight = "run_in_flight"
	CodeDisabled    = "disabled"
)

// RunProgress is the in-flight run as the status shows it.
type RunProgress struct {
	Running bool      `json:"running"`
	Manual  bool      `json:"manual,omitempty"`
	All     bool      `json:"all,omitempty"`
	Started time.Time `json:"started,omitempty"`
	// Done of Total selected topics are finished; Total is 0 until the run has chosen its topics.
	Done  int `json:"done"`
	Total int `json:"total"`
}

var (
	progMu sync.Mutex
	prog   RunProgress
	baseMu sync.Mutex
	base   = context.Background()
)

// beginRun and endRun bracket a run while its owner holds runMu, so Running is true exactly while
// runMu is held.
func beginRun(manual, all bool, now time.Time) {
	progMu.Lock()
	prog = RunProgress{Running: true, Manual: manual, All: manual && all, Started: now}
	progMu.Unlock()
}

func endRun() {
	progMu.Lock()
	prog = RunProgress{}
	progMu.Unlock()
}

func setProgress(done, total int) {
	progMu.Lock()
	if prog.Running {
		prog.Done, prog.Total = done, total
	}
	progMu.Unlock()
}

func currentProgress() RunProgress {
	progMu.Lock()
	defer progMu.Unlock()
	return prog
}

// StartManual validates and starts a manual run in the background and returns at once. It writes
// LastManualRun, never Trigger.LastRun, so the automatic 3 h window is untouched.
func StartManual(all bool, now time.Time) error {
	d := currentDeps()
	if d.Enabled == nil || !d.Enabled() {
		return ErrDisabled
	}
	if !runMu.TryLock() {
		return ErrRunInFlight
	}
	beginRun(true, all, now)
	stateMu.Lock()
	if st, err := loadState(); err == nil {
		st.Trigger.LastManualRun = now
		_ = st.save()
	}
	stateMu.Unlock()
	baseMu.Lock()
	ctx := base
	baseMu.Unlock()
	go func() {
		defer runMu.Unlock()
		defer endRun()
		doRun(ctx, d, LoadConfig(), now, runOpts{manual: true, all: all})
	}()
	return nil
}

// runRequest is the optional body of the run route.
type runRequest struct {
	All bool `json:"all"`
}

// RunStarted is the 202 answer.
type RunStarted struct {
	Started bool `json:"started"`
	All     bool `json:"all"`
}

// HandleRun is POST /agents/memory-consolidation/run {all?}: a member write, answered only with the
// Control Plane's member-origin mark (checked here as well as by memberorigin.Require).
func HandleRun(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DecideMaxRequest)
	var req runRequest
	if b, err := io.ReadAll(r.Body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "could not read the request body")
		return
	} else if len(b) > 0 && json.Unmarshal(b, &req) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	switch err := StartManual(req.All, time.Now()); {
	case err == nil:
		httpx.WriteJSON(w, http.StatusAccepted, RunStarted{Started: true, All: req.All})
	case errors.Is(err, ErrRunInFlight):
		httpx.WriteErr(w, http.StatusConflict, CodeRunInFlight, "a consolidation run is already in progress")
	case errors.Is(err, ErrDisabled):
		httpx.WriteErr(w, http.StatusConflict, CodeDisabled, "memory consolidation is switched off")
	default:
		internalErr(w, "the run could not be started", err)
	}
}

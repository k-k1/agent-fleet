package memconsol

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
)

// DecideMaxRequest bounds the body memberorigin.Require buffers on the decision routes.
const DecideMaxRequest = 4 << 10

// ListView is the list route's answer.
type ListView struct {
	Proposals []Proposal `json:"proposals"`
}

// RetryView is the retry route's answer: the proposal and how many observations were reopened.
type RetryView struct {
	Proposal Proposal `json:"proposal"`
	Reopened int      `json:"reopened"`
}

type idRequest struct {
	ID string `json:"id"`
}

// StatusView is the read-only status for the member's screen (the next PR's UI).
type StatusView struct {
	Enabled bool `json:"enabled"`
	Config  struct {
		IdleAfter       string `json:"idleAfter"`
		MinInterval     string `json:"minInterval"`
		NewObservations int    `json:"newObservations"`
		MaxWait         string `json:"maxWait"`
		TokenBudget     int    `json:"tokenBudget"`
		ProposalTTL     string `json:"proposalTTL"`
	} `json:"config"`
	LastRun *Report        `json:"lastRun,omitempty"`
	Trigger TriggerState   `json:"trigger"`
	Counts  map[string]int `json:"counts"`
	// Waiting are the ripe topics that no run has taken, each with the cause that delays it.
	Waiting []Delay `json:"waiting"`
}

// BuildStatus assembles the status. It writes nothing (Gather and the topic build are reads).
func BuildStatus(now time.Time) (StatusView, error) {
	var v StatusView
	d := currentDeps()
	cfg := LoadConfig()
	v.Enabled = d.Enabled != nil && d.Enabled()
	v.Config.IdleAfter, v.Config.MinInterval, v.Config.MaxWait = cfg.IdleAfter.String(), cfg.MinInterval.String(), cfg.MaxWait.String()
	v.Config.NewObservations, v.Config.TokenBudget, v.Config.ProposalTTL = cfg.NewObservations, cfg.TokenBudget, cfg.ProposalTTL.String()
	v.Counts = map[string]int{}
	v.Waiting = []Delay{}
	if r, ok := LastReport(); ok {
		v.LastRun = &r
	}
	ps, err := ListProposals("", now)
	if err != nil {
		return v, err
	}
	for _, p := range ps {
		v.Counts[p.Status]++
	}
	stateMu.Lock()
	st, err := loadState()
	stateMu.Unlock()
	if err != nil {
		return v, err
	}
	v.Trigger = st.Trigger
	in, err := Gather(now)
	if err != nil {
		return v, err
	}
	for i := range in.Observations {
		if s := st.Observations[in.Observations[i].ID]; s != nil {
			in.Observations[i].FirstSeen = s.FirstSeen
		}
	}
	pending, err := pendingTopics(now)
	if err != nil {
		return v, err
	}
	delayed := map[string]Delay{}
	if v.LastRun != nil {
		for _, dl := range v.LastRun.Delayed {
			delayed[dl.Topic] = dl
		}
	}
	for _, t := range BuildTopics(in.Observations, in.Memories) {
		open := t.OpenObservations(st)
		if !Ripe(open, pending[t.ID], cfg, now) {
			continue
		}
		dl := Delay{Topic: t.ID, Scope: ScopeKey(t.Scope, t.ProjectID), Memory: t.Memory, Cause: CauseWaiting}
		switch {
		case delayed[t.ID].Cause != "":
			dl.Cause, dl.Detail = delayed[t.ID].Cause, delayed[t.ID].Detail
		case st.Trigger.StoppedGap:
			dl.Cause = CauseStopped
		case st.Trigger.BusyTicks > 0:
			dl.Cause = CauseNeverIdle
		}
		v.Waiting = append(v.Waiting, dl)
	}
	sort.Slice(v.Waiting, func(i, j int) bool { return v.Waiting[i].Topic < v.Waiting[j].Topic })
	return v, nil
}

func internalErr(w http.ResponseWriter, what string, err error) {
	log.Printf("memory consolidation: %s: %v", what, err)
	httpx.WriteErr(w, http.StatusInternalServerError, "internal", what)
}

// HandleStatus is GET /agents/memory-consolidation: read-only.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	v, err := BuildStatus(time.Now())
	if err != nil {
		internalErr(w, "the status could not be read", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// HandleList is GET /agents/memory-consolidation/proposals[?status=]: read-only.
func HandleList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", StatusPending, StatusApproved, StatusRejected, StatusExpired, StatusRefused, StatusPartial:
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "status is not a proposal status")
		return
	}
	ps, err := ListProposals(status, time.Now())
	if err != nil {
		internalErr(w, "the proposals could not be listed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ListView{Proposals: ps})
}

// HandleGet is GET /agents/memory-consolidation/proposal?id=: one proposal with a diff per item.
// Read-only.
func HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !ValidID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "id is not a proposal id")
		return
	}
	v, err := View(id, time.Now())
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such proposal")
	case err != nil:
		internalErr(w, "the proposal could not be read", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, v)
	}
}

// decide runs a member decision. A member write: it answers only a request carrying the Control
// Plane's member-origin mark (ADR 0113), checked here as well as by memberorigin.Require so a route
// registered without the wrapper still refuses. An agent holds AGENT_TOKEN but cannot make the
// mark, so no session can approve or reject its own (or anyone's) proposal.
func decide(w http.ResponseWriter, r *http.Request, f func(id string) (any, error)) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DecideMaxRequest)
	var req idRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !ValidID(req.ID) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "id is not a proposal id")
		return
	}
	out, err := f(req.ID)
	var pr *Problem
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, out)
	case errors.Is(err, ErrNotFound):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such proposal")
	case errors.Is(err, ErrNotPending):
		httpx.WriteErr(w, http.StatusConflict, "not_pending", "the proposal was decided or has expired")
	case errors.As(err, &pr):
		httpx.WriteErr(w, http.StatusConflict, pr.Kind, pr.Msg)
	default:
		internalErr(w, "the decision could not be made", err)
	}
}

// HandleApprove is POST /agents/memory-consolidation/proposals/approve {id}.
func HandleApprove(w http.ResponseWriter, r *http.Request) {
	decide(w, r, func(id string) (any, error) { return Approve(id, time.Now()) })
}

// HandleReject is POST /agents/memory-consolidation/proposals/reject {id}.
func HandleReject(w http.ResponseWriter, r *http.Request) {
	decide(w, r, func(id string) (any, error) { return Reject(id, time.Now()) })
}

// HandleRetry is POST /agents/memory-consolidation/proposals/retry {id}: reopen the observations
// of a rejected, expired or refused proposal so the next run looks at the topic again.
func HandleRetry(w http.ResponseWriter, r *http.Request) {
	decide(w, r, func(id string) (any, error) {
		p, n, err := Retry(id, time.Now())
		if err != nil {
			return nil, err
		}
		return RetryView{Proposal: p, Reopened: n}, nil
	})
}

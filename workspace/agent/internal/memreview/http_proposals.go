package memreview

// The approval routes (ADR 0117 decision 6). Every one of them, the list included, answers only a
// request carrying the Control Plane's member-origin mark (ADR 0113). The mark is the whole
// authorisation: AGENT_TOKEN is held by every session's shell, so a session (or a model that talked
// one into it) can neither publish its own proposal, reject, nor undo a run, and cannot read the
// queue either: a proposal is text the model drew from other sessions' conversations that the member
// has not yet chosen to publish. Each handler checks the mark itself as well as through
// memberorigin.Require, so a route registered without the wrapper still refuses. The list is a POST
// with an empty JSON body because the mark is made for JSON requests; it changes nothing.

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// DecideMaxRequest bounds the body memberorigin.Require buffers on the decision routes.
const DecideMaxRequest = 4 << 10

// ProposalsResponse is the answer of HandleProposals.
type ProposalsResponse struct {
	Proposals []ProposalView `json:"proposals"`
}

// HandleProposals is POST /agents/memory-review/proposals/list: the queue with each proposal's live
// state and diff.
func HandleProposals(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	out := ProposalsResponse{Proposals: Proposals()}
	if out.Proposals == nil {
		out.Proposals = []ProposalView{}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type idRequest struct {
	ID string `json:"id"`
}

type runRefRequest struct {
	Run string `json:"run"`
}

// ApprovedResponse is the answer of HandleApprove.
type ApprovedResponse struct {
	Commit   string `json:"commit"`
	Revision int    `json:"revision,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	// Already: an earlier approval had published it and only its removal from the queue was left.
	Already bool `json:"already,omitempty"`
}

// HandleApprove is POST /agents/memory-review/proposals/approve {id}: publish one proposal.
func HandleApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := decodeID(w, r)
	if !ok {
		return
	}
	pub, err := Approve(id, time.Now())
	var rf *memoryx.ReviewRefusal
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, ApprovedResponse{Commit: pub.Commit, Revision: pub.Revision, Deleted: pub.Deleted, Already: pub.Already})
	case errors.Is(err, ErrNoProposal):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such pending proposal")
	case errors.As(err, &rf):
		status, code := http.StatusConflict, "proposal_stale"
		switch rf.Kind {
		case memoryx.ReviewForgotten:
			code = "proposal_forgotten"
		case memoryx.ReviewSecret:
			status, code = http.StatusUnprocessableEntity, "proposal_secret"
		case memoryx.ReviewInvalid:
			status, code = http.StatusUnprocessableEntity, "proposal_invalid"
		}
		httpx.WriteErr(w, status, code, rf.Msg)
	case pub.Commit != "":
		// Published, but the queue entry could not be removed. Say so: the next attempt finds the
		// commit by its proposal id and finishes the removal without writing again.
		log.Printf("memory review: a proposal was published but its queue entry was not removed")
		httpx.WriteErr(w, http.StatusInternalServerError, "decision_incomplete", "the proposal was published, but removing it from the queue failed; approve it again to finish")
	default:
		log.Printf("memory review: approve failed")
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", "the proposal could not be published")
	}
}

// RejectedResponse is the answer of HandleReject.
type RejectedResponse struct {
	Rejected bool `json:"rejected"`
}

// HandleReject is POST /agents/memory-review/proposals/reject {id}.
func HandleReject(w http.ResponseWriter, r *http.Request) {
	id, ok := decodeID(w, r)
	if !ok {
		return
	}
	switch err := Reject(id, time.Now()); {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, RejectedResponse{Rejected: true})
	case errors.Is(err, ErrNoProposal):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such pending proposal")
	default:
		log.Printf("memory review: reject failed")
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", "the proposal could not be rejected")
	}
}

// HandleRejectAll is POST /agents/memory-review/proposals/reject-all {run?}: every pending
// proposal, or those of one run.
func HandleRejectAll(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req struct {
		Run string `json:"run"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Run != "" && !memoryx.ValidReviewID(req.Run) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "run is not a run id")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, RejectAll(req.Run, time.Now()))
}

// HandleRevertRun is POST /agents/memory-review/runs/revert {run}: undo what a run published, as
// new commits. Each commit is answered separately; a conflict is not an error of the request.
func HandleRevertRun(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req runRefRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !memoryx.ValidReviewID(req.Run) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "run is not a run id")
		return
	}
	rep, err := RevertRun(req.Run, time.Now())
	if err != nil {
		log.Printf("memory review: revert run failed")
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", "the run could not be reverted")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, DecideMaxRequest)
	return httpx.DecodeJSON(w, r, v)
}

// decodeID checks the mark and reads a proposal id.
func decodeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !memberorigin.RefuseUnverified(w, r) {
		return "", false
	}
	var req idRequest
	if !decodeBody(w, r, &req) {
		return "", false
	}
	if !memoryx.ValidReviewID(req.ID) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "id is not a proposal id")
		return "", false
	}
	return req.ID, true
}

// Package memreview is what is left of ADR 0117's background memory review after ADR 0130
// decision 2 removed it: the member's undo of a past run (revert.go) and the one-time export of
// the review's pending queue (export.go). There is no loop, no queue and no helper call.
package memreview

import (
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// DecideMaxRequest bounds the body memberorigin.Require buffers on the revert route.
const DecideMaxRequest = 4 << 10

type runRefRequest struct {
	Run string `json:"run"`
}

// HandleRevertRun is POST /agents/memory-review/runs/revert {run}: undo what a run published, as
// new commits (memoryx.RevertReviewRun). Each commit is answered separately; a conflict is not an
// error of the request. A member write: it answers only a request carrying the Control Plane's
// member-origin mark (ADR 0113), checked here as well as by memberorigin.Require so a route
// registered without the wrapper still refuses.
func HandleRevertRun(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DecideMaxRequest)
	var req runRefRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !memoryx.ValidReviewID(req.Run) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "run is not a run id")
		return
	}
	rep, err := memoryx.RevertReviewRun(req.Run, time.Now())
	if err != nil {
		log.Printf("memory review: revert run failed")
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", "the run could not be reverted")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

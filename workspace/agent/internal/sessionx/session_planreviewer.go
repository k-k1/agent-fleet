package sessionx

// Which session reviewed a planner's plan file (Meta.PlanReviewers): the memory behind the
// Console's "Review again in <reviewer>" shortcut. It is kept on the planner's meta so it
// reaches every device that reads the session list, not just the browser that launched the
// reviewer. The Agent only stores the link; whether the reviewer still exists is judged by the
// reader against the live list, so a deleted reviewer needs no cleanup here.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const maxPlanReviewerPath = 1024

// withPlanReviewer returns list with path linked to reviewer (empty reviewer = unlink). The
// newest link is last and the oldest are dropped beyond session.MaxPlanReviewers. It never
// modifies list: the slice may be shared with a meta read elsewhere.
func withPlanReviewer(list []session.PlanReviewer, path, reviewer string) []session.PlanReviewer {
	out := make([]session.PlanReviewer, 0, len(list)+1)
	for _, e := range list {
		if e.Path != path {
			out = append(out, e)
		}
	}
	if reviewer != "" {
		out = append(out, session.PlanReviewer{Path: path, Reviewer: reviewer})
	}
	if over := len(out) - session.MaxPlanReviewers; over > 0 {
		out = out[over:]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HandleSetPlanReviewer (POST /sessions/{name}/plan-reviewer) links or unlinks the reviewer of
// one plan file: {"path": "...", "reviewer": "<session name>"}, an empty reviewer unlinks.
func HandleSetPlanReviewer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Path     string `json:"path"`
		Reviewer string `json:"reviewer"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "invalid request body")
		return
	}
	if req.Path == "" || len(req.Path) > maxPlanReviewerPath || !utf8.ValidString(req.Path) || strings.ContainsFunc(req.Path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "path is empty, too long or contains control characters")
		return
	}
	if req.Reviewer != "" && (!session.ValidName(req.Reviewer) || req.Reviewer == name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_reviewer", "reviewer must be another session's name")
		return
	}
	m, ok := UpdateSessionMeta(name, func(m *session.Meta) bool {
		m.PlanReviewers = withPlanReviewer(m.PlanReviewers, req.Path, req.Reviewer)
		return true
	})
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, SessionAlive(m)))
}

// planReviewersOrEmpty makes the list row carry [] rather than null (see session.Session).
func planReviewersOrEmpty(l []session.PlanReviewer) []session.PlanReviewer {
	if l == nil {
		return []session.PlanReviewer{}
	}
	return l
}

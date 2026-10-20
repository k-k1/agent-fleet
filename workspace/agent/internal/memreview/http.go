package memreview

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// DefaultLoop is the Agent's loop; package main sets it at boot. A route answers 503 while it is
// nil (a build that never started one).
var DefaultLoop *Loop

// RunMaxRequest bounds the body memberorigin.Require buffers on the run route.
const RunMaxRequest = 4 << 10

type runRequest struct {
	Session string `json:"session"`
}

// HandleRun is POST /agents/memory-review/run: the member's "Review now" for one session. It is a
// member action - it spends tokens and sends a conversation to a model provider - so it answers only
// a request carrying the Control Plane's member-origin mark (ADR 0113), like the other member
// writes; a session's own AGENT_TOKEN is not enough. It starts the review in the background and
// answers 202; the outcome is in HandleStatus.
func HandleRun(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	if DefaultLoop == nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "memory_review_unavailable", "the memory review is not running in this Agent")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, RunMaxRequest)
	var req runRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !session.ValidName(req.Session) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "session is not a valid session name")
		return
	}
	// Only a definite "unavailable" refuses; "unknown" lets the run start and fail closed itself.
	if state, _ := backendState(); state == BackendUnavailable {
		httpx.WriteErr(w, http.StatusConflict, "memory_review_no_backend", "no backend that can run the review without tools is available here (only claude qualifies)")
		return
	}
	switch err := DefaultLoop.ReviewNow(req.Session); {
	case err == nil:
		httpx.WriteJSON(w, http.StatusAccepted, StartedResponse{Started: true})
	case errors.Is(err, ErrDisabled):
		httpx.WriteErr(w, http.StatusConflict, "memory_review_off", "the memory review is switched off (it needs Agent memory and a review mode)")
	case errors.Is(err, ErrNoSession):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session to review")
	case errors.Is(err, ErrShuttingDown):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "shutting_down", "the Agent is shutting down")
	case errors.Is(err, ErrBusy):
		httpx.WriteErr(w, http.StatusConflict, "memory_review_busy", "a review is already running")
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", "the review could not be started")
	}
}

// StartedResponse is the answer of HandleRun.
type StartedResponse struct {
	Started bool `json:"started"`
}

// StatusResponse is the answer of HandleStatus.
type StatusResponse struct {
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
	Pending int    `json:"pending"`
	// ReleasePending counts releases of rejected hashes that are owed and not done (a forgotten
	// memory whose hashes could not be removed yet); it is retried every wake-up.
	ReleasePending int `json:"releasePending,omitempty"`
	// Backend is whether a deny-all backend can run the review here: "available", "unavailable"
	// (none qualifies: the pin names a backend that cannot, or no qualifying one is reachable)
	// or "unknown" (the availability cache is cold). It is the resolution GET /ai-assist/resolution
	// reports for the memory review, so the two never disagree; BackendKind names the backend
	// when one resolved.
	Backend     string `json:"backend"`
	BackendKind string `json:"backendKind,omitempty"`
	Session     *Info  `json:"session,omitempty"`
}

// Backend states of StatusResponse.Backend.
const (
	BackendAvailable   = "available"
	BackendUnavailable = "unavailable"
	BackendUnknown     = "unknown"
)

// ResolveBackend and WarmBackend are the Agent's deny-all resolution (chatx.ResolveDenyAllCached and
// chatx.WarmDenyAllKind), set at boot by package main: chatx needs its own wiring, which this package
// does not carry. Left nil the state is "unknown".
var (
	ResolveBackend func(feature string) (kind, source string, ok bool)
	WarmBackend    func()
)

// backendState answers from the availability cache alone (never starts a CLI). A cold cache
// answers "unknown" and is warmed in the background for the next poll.
func backendState() (state, kind string) {
	if ResolveBackend == nil {
		return BackendUnknown, ""
	}
	kind, source, ok := ResolveBackend(usagex.FeatureMemoryReview)
	switch {
	case !ok:
		if WarmBackend != nil {
			WarmBackend()
		}
		return BackendUnknown, ""
	case source == chatx.OneShotSourceUnavailable:
		return BackendUnavailable, ""
	}
	return BackendAvailable, kind
}

// HandleStatus is GET /agents/memory-review?session=<name>: the review's mode and, for the named
// session, whether a review is running and how the last one ended. Counts and states only: no
// proposal text and no model output.
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	out := StatusResponse{Mode: uiprefs.MemoryReview(), Enabled: uiprefs.MemoryReviewEnabled(), Pending: pendingCount(), ReleasePending: ReleasePending()}
	out.Backend, out.BackendKind = backendState()
	if name := r.URL.Query().Get("session"); name != "" {
		if !session.ValidName(name) {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "session is not a valid session name")
			return
		}
		if DefaultLoop != nil {
			info := DefaultLoop.Status(name)
			out.Session = &info
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

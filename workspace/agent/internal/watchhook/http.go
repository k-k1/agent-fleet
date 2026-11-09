package watchhook

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

func writeErr(w http.ResponseWriter, err error) {
	var ue *Error
	if errors.As(err, &ue) {
		httpx.WriteErr(w, ue.Status, ue.Code, ue.Message)
		return
	}
	httpx.WriteErr(w, http.StatusInternalServerError, "internal", "internal error")
}

// listResp is GET /watch/hooks' answer.
type listResp struct {
	Hooks []Hook `json:"hooks"`
}

// Handlers are the Agent routes behind af-watch-hook: GET/POST /watch/hooks and
// DELETE /watch/hooks/{id}. The Control Plane token stays in the Agent; the shell holds only
// AGENT_TOKEN.
type Handlers struct{ M *Manager }

// List is GET /watch/hooks.
func (h Handlers) List(w http.ResponseWriter, r *http.Request) {
	hooks, err := h.M.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if hooks == nil {
		hooks = []Hook{}
	}
	httpx.WriteJSON(w, http.StatusOK, listResp{Hooks: hooks})
}

// Add is POST /watch/hooks.
func (h Handlers) Add(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	res, err := h.M.Add(r.Context(), in.Source, in.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if res.Existing {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, res)
}

// Remove is DELETE /watch/hooks/{id}.
func (h Handlers) Remove(w http.ResponseWriter, r *http.Request) {
	res, err := h.M.Remove(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

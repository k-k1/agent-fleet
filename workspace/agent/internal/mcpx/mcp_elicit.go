package mcpx

// The member's side of the relay's held calls (ADR 0146 decision 6): the list of calls waiting
// for a person's confirmation, and the answer. Both are proxied by the Control Plane (an explicit
// allowlist, relays_connections.go), which audits the answer as mcp.elicitation from what the
// answer route returns.
//
// The answer requires the member-origin mark: an agent can reach the Agent's routes with its own
// AGENT_TOKEN, and without the mark one running in bypass mode could accept its own call.

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
)

// Adding or renaming one means updating "err.<code>" in
// console/src/lib/i18n/locales/{ja,en}/errors.ts at the same time.
const (
	errCodeElicitGone   = "mcp_elicitation_gone"
	errCodeElicitAnswer = "mcp_elicitation_answer"
)

// ElicitAnswerMaxRequest bounds POST /mcp-elicitations/answer's body: one choice per field
// of a form the relay already cut to size.
const ElicitAnswerMaxRequest = 64 << 10

// The relay's list and answer; a test replaces them.
var (
	pendingElicitations = mcpreg.PendingElicitations
	answerElicitation   = mcpreg.AnswerElicitation
)

// elicitationsWire is the GET /mcp-elicitations response.
type elicitationsWire struct {
	Elicitations []mcpreg.PendingElicitation `json:"elicitations"`
}

// HandleElicitationsGet (GET /mcp-elicitations) lists the held calls, oldest first.
func HandleElicitationsGet(w http.ResponseWriter, r *http.Request) {
	// The arguments can be a mail's text: no cache keeps them.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, elicitationsWire{Elicitations: pendingElicitations()})
}

// elicitAnswerReq is the member's answer to the held call id: action "accept" with a choice per
// input key and field name, or "decline" with nothing else.
type elicitAnswerReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// A pointer, so a null is told apart from "": a server may offer "" as a choice, and a null
	// must not stand for it.
	Choices map[string]map[string]*string `json:"choices,omitempty"`
}

// elicitAnsweredWire says which call was answered and how; the Control Plane's audit reads it
// (server, tool, choice and session; never the arguments or the choices themselves).
type elicitAnsweredWire struct {
	ID       string `json:"id"`
	ServerID string `json:"serverId"`
	Tool     string `json:"tool"`
	Session  string `json:"session,omitempty"`
	Action   string `json:"action"`
}

// HandleElicitationAnswer (POST /mcp-elicitations/answer) hands the member's answer to the
// held call.
func HandleElicitationAnswer(w http.ResponseWriter, r *http.Request) {
	if _, ok := memberorigin.FromContext(r.Context()); !ok {
		httpx.WriteErr(w, http.StatusForbidden, memberorigin.CodeRequired, "answering a confirmation needs the member's own request")
		return
	}
	var in elicitAnswerReq
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.ID == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	var a mcpreg.ElicitAnswer
	switch in.Action {
	case "accept":
		a.Choices = map[string]map[string]string{}
		for key, fields := range in.Choices {
			if fields == nil {
				a.Choices[key] = nil
				continue
			}
			a.Choices[key] = map[string]string{}
			for name, v := range fields {
				if v == nil {
					httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "a choice must be a string, not null")
					return
				}
				a.Choices[key][name] = *v
			}
		}
	case "decline":
		if len(in.Choices) > 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "a decline carries no choices")
			return
		}
		a.Decline = true
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", `action must be "accept" or "decline"`)
		return
	}
	p, err := answerElicitation(in.ID, a)
	switch {
	case errors.Is(err, mcpreg.ErrElicitationNotFound):
		httpx.WriteErr(w, http.StatusNotFound, errCodeElicitGone, err.Error())
		return
	case errors.Is(err, mcpreg.ErrElicitationAnswer):
		httpx.WriteErr(w, http.StatusBadRequest, errCodeElicitAnswer, err.Error())
		return
	case err != nil:
		httpx.WriteErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, elicitAnsweredWire{ID: p.ID, ServerID: p.ServerID, Tool: p.Tool,
		Session: p.Session, Action: in.Action})
}

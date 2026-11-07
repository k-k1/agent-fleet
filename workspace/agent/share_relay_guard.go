package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// shareRelayFields lists, per route, the only body fields an approved shared-session proposal
// may carry. The Control Plane enforces the same list when the proposal is created and again
// when it is approved (control-plane/session_share_proposal.go); this is the second check, on
// the side that owns the files. Keep the two in step.
var shareRelayFields = map[string][]string{
	"turn":            {"op", "prompt", "clientMessageID"},
	"respond":         {"id", "decision", "scope", "answers"},
	"answer-question": {"choices"},
	"plan-respond":    {"decision", "feedback"},
}

// refuseShareRelay reports why an approved proposal (a request carrying the share-operation
// id header) must not reach the handler, or "" when it may. Attachments are refused outright:
// a share has no area of files the proposer could have put there, so any path would name the
// owner's own files. Empty or malformed bodies pass through; the handler rejects those itself.
func refuseShareRelay(path string, body []byte) (code, msg string) {
	action := path[strings.LastIndex(path, "/")+1:]
	allowed, ok := shareRelayFields[action]
	if !ok {
		return "", ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return "", ""
	}
	if _, has := fields["attachments"]; has {
		return "share_attachments_refused", "attachments are not accepted on a shared-session operation"
	}
	for key := range fields {
		known := false
		for _, a := range allowed {
			known = known || a == key
		}
		if !known {
			return "share_field_refused", "field not allowed on a shared-session operation: " + key
		}
	}
	if action == "turn" {
		var op struct {
			Op string `json:"op"`
		}
		_ = json.Unmarshal(body, &op)
		if op.Op != "start" && op.Op != "steer" {
			return "share_op_refused", "only start and steer may be relayed from a shared session"
		}
	}
	return "", ""
}

// guardShareRelay applies refuseShareRelay, and refuses terminal sessions, before the
// operation is claimed. Requests without the header (the owner's own Console) are untouched.
func guardShareRelay(w http.ResponseWriter, r *http.Request) bool {
	if name := r.PathValue("name"); session.ValidName(name) {
		if meta, ok := session.ReadMeta(name); ok && (meta.Kind == session.KindShell || meta.Kind == session.KindSSM) {
			httpx.WriteErr(w, http.StatusBadRequest, "share_session_refused", "shared-session operations are not accepted on a terminal session")
			return false
		}
	}
	if r.Body == nil {
		return true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, shareOperationMaxBody+1))
	if err != nil {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "share_body_too_large", "operation body is too large")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if code, msg := refuseShareRelay(r.URL.Path, body); code != "" {
		httpx.WriteErr(w, http.StatusBadRequest, code, msg)
		return false
	}
	return true
}

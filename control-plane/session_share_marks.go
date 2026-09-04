package main

// Transcript markers on a shared session (docs/log/69 / ADR 0050).
//
// A mark itself lives in the owner's Workspace (the Agent's session-marks); the CP
// evaluates the ACL on every call and relays. Exactly as for the transcript and the handoff
// proposal, no copy of the body is held in the CP DB.
//
// Writes — an RW recipient drawing their own line — deliberately stay off the "propose →
// owner approves" path of docs/log/59 §2. That approval exists because a proposal moves an
// agent, spending someone else's session and tokens; a mark reaches no agent and never
// enters the transcript. Queuing an approval per line drawn would only dilute what approval
// means (ADR 0050 decision 4).
//
// Two things are stricter instead:
//   - author is never taken from the client's claim; the CP always overwrites it with the
//     authenticated login id.
//   - you may delete only your own mark. The author is passed to the Agent, which matches
//     on it.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// markProseKinds is the set of part kinds a mark may be placed on. It must hold the same
// content as the Agent's table of the same name and the Console's MARKABLE_KINDS.
//
// It is checked here so that a mark's quote cannot smuggle out the coordinates the shared
// DTO drops — cwd, file, diffs (docs/log/69 §69.4). The Console and the Agent already limit
// where a mark can go; keeping the check at the relay's exit too means one side going lax
// is not yet a leak.
var markProseKinds = map[string]bool{"": true, "text": true, "plan": true, "answer": true, "output": true, "prompt": true}

const sharedMarkMaxBytes = 8 << 10

func (a sessionShareAPI) marks(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	switch r.Method {
	case http.MethodGet:
		a.marksRead(w, r, mv)
	case http.MethodPost:
		a.marksAdd(w, r, ident, mv)
	case http.MethodDelete:
		a.marksDelete(w, r, ident, mv)
	default:
		writeAPIErr(w, &apiError{http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"})
	}
}

func (a sessionShareAPI) marksRead(w http.ResponseWriter, r *http.Request, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	// Counted in the same bucket as the transcript, so adding a surface does not add round
	// trips into the owner's Workspace per recipient.
	if !a.allowRead(mv.MembershipID + ":" + c.ID) {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, "shared_read_rate_limited", "too many shared transcript reads"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	payload, status, e := a.ownerGET(r.Context(), res, "/sessions/"+url.PathEscape(c.Name)+"/marks")
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, sharedMarksDTO(payload))
}

func (a sessionShareAPI) marksAdd(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, sharedMarkMaxBytes+1))
	if err != nil || len(raw) > sharedMarkMaxBytes {
		writeAPIErr(w, &apiError{413, "mark_too_large", "mark exceeds 8 KiB"})
		return
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		writeAPIErr(w, &apiError{400, "bad_mark", "invalid mark"})
		return
	}
	kind, _ := in["kind"].(string)
	if !markProseKinds[kind] {
		writeAPIErr(w, &apiError{400, "mark_kind_not_markable", "this part kind cannot be marked"})
		return
	}
	// Discard the declared author and stamp the authenticated login id: a recipient must
	// not be able to impersonate the owner or another recipient.
	who := strings.TrimSpace(ident.Email)
	if who == "" {
		writeAPIErr(w, &apiError{403, "mark_no_identity", "no login id to attribute this mark to"})
		return
	}
	in["author"] = who
	body, _ := json.Marshal(in)
	payload, status, e := a.ownerSend(r.Context(), res, http.MethodPost, "/sessions/"+url.PathEscape(c.Name)+"/marks", body)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	one, _ := payload["mark"].(map[string]any)
	writeJSON(w, status, map[string]any{"mark": sharedMarkDTO(one)})
}

func (a sessionShareAPI) marksDelete(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	who := strings.TrimSpace(ident.Email)
	if id == "" || who == "" {
		writeAPIErr(w, &apiError{400, "mark_id_missing", "id is required"})
		return
	}
	// Always attaching author is what makes the Agent narrow the delete to the caller's own
	// marks. The owner path (/api/sessions/…) does not pass it, so an owner can delete any.
	q := url.Values{"id": {id}, "author": {who}}
	_, status, e := a.ownerSend(r.Context(), res, http.MethodDelete,
		"/sessions/"+url.PathEscape(c.Name)+"/marks?"+q.Encode(), nil)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if status == http.StatusForbidden {
		writeAPIErr(w, &apiError{403, "mark_not_yours", "this mark belongs to someone else"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownerSend relays a write to the owner's Agent. Like ownerGET the caller owns
// authorization, the DTO and the rate limit; unlike it, an empty body is normal
// (DELETE) and the decoded payload may be empty (204).
func (a sessionShareAPI) ownerSend(ctx context.Context, res *resolved, method, path string, body []byte) (map[string]any, int, *apiError) {
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, res.rt.Endpoint()+path, rdr)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return nil, 0, &apiError{502, "owner_workspace_unreachable", "owner workspace is unreachable"}
	}
	defer resp.Body.Close()
	var payload map[string]any
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return payload, resp.StatusCode, nil
}

// sharedMarksDTO is the marker allowlist. Display needs only the position
// (turn/part/kind/quote/nth), the look (color) and who/when (author/created_at). Marks
// never carried coordinates to begin with, but a mark on a non-prose kind is dropped here
// as well — the second half of the net in docs/log/69 §69.4.
func sharedMarksDTO(payload map[string]any) map[string]any {
	items, _ := payload["marks"].([]any)
	out := make([]any, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		if !markProseKinds[kind] {
			continue
		}
		out = append(out, sharedMarkDTO(m))
	}
	return map[string]any{"marks": out}
}

func sharedMarkDTO(m map[string]any) map[string]any {
	q := map[string]any{}
	if m == nil {
		return q
	}
	copyAllowed(q, m, "id", "turn", "part", "kind", "quote", "nth", "color", "author", "created_at")
	return q
}

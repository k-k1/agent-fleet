package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	shareProposalMaxBytes = 32 << 10
	shareProposalMaxOpen  = 20
)

type shareReadWindow struct {
	at time.Time
	n  int
}

type sessionShareAPI struct {
	memberAuth
	readMu *sync.Mutex
	reads  map[string]shareReadWindow
}

func newSessionShareAPI(m *manager) sessionShareAPI {
	return sessionShareAPI{memberAuth: memberAuth{m}, readMu: &sync.Mutex{}, reads: map[string]shareReadWindow{}}
}

func (a sessionShareAPI) allowRead(key string) bool {
	a.readMu.Lock()
	defer a.readMu.Unlock()
	now := time.Now()
	if len(a.reads) > 10_000 {
		for k, candidate := range a.reads {
			if now.Sub(candidate.at) >= time.Minute {
				delete(a.reads, k)
			}
		}
	}
	win := a.reads[key]
	if win.at.IsZero() || now.Sub(win.at) >= time.Minute {
		win = shareReadWindow{at: now}
	}
	if win.n >= 120 {
		a.reads[key] = win
		return false
	}
	win.n++
	a.reads[key] = win
	return true
}

func (a sessionShareAPI) syncCatalog(ctx context.Context, res *resolved) error {
	if res.rt.State(ctx) != "running" {
		return nil
	}
	body, err := agentText(ctx, res.rt, http.MethodGet, "/sessions/catalog", nil)
	if err != nil {
		return err
	}
	var wire struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return err
	}
	now := nowTS()
	rows := make([]SharedSessionCatalog, 0, len(wire.Sessions))
	for _, s := range wire.Sessions {
		state := "stopped"
		if s.Alive {
			state = "running"
		}
		rows = append(rows, SharedSessionCatalog{ID: newID(), WorkspaceID: res.ws.ID,
			OwnerMembershipID: res.mv.MembershipID, Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
			WorkingCopyID: s.WorkingCopyID, Title: s.Title, Label: s.Label, CreatedAt: s.CreatedAt,
			State: state, Archived: s.Archived, LastSeen: now})
	}
	if err := a.mgr.store.ReplaceSharedSessionCatalog(ctx, res.ws.ID, res.mv.MembershipID, rows); err != nil {
		return err
	}
	// A deleted working copy terminates its dynamic rule. Only prune after a
	// successful live inventory, never on a transient Agent error.
	reposBody, err := agentText(ctx, res.rt, http.MethodGet, "/repos", nil)
	if err != nil {
		return nil
	}
	var inventory struct {
		Repos []struct {
			WorkingCopyID string `json:"workingCopyId"`
			Worktree      bool   `json:"worktree"`
		} `json:"repos"`
	}
	if json.Unmarshal([]byte(reposBody), &inventory) != nil {
		return nil
	}
	valid := map[string]bool{}
	for _, repo := range inventory.Repos {
		valid[repo.WorkingCopyID] = true
	}
	shares, _ := a.mgr.store.ListSessionSharesByOwner(ctx, res.mv.MembershipID)
	for _, share := range shares {
		if share.ScopeType != "session" && !valid[share.ScopeKey] {
			_ = a.mgr.store.DeleteSessionSharesByScope(ctx, res.mv.MembershipID, share.ScopeType, share.ScopeKey)
		}
	}
	return nil
}

func memberByUserKey(ctx context.Context, st Store, tenantID, key string) (MemberInfo, bool, error) {
	rows, err := st.ListMembersByTenant(ctx, tenantID)
	if err != nil {
		return MemberInfo{}, false, err
	}
	for _, m := range rows {
		if m.UserKey == key {
			return m, true, nil
		}
	}
	return MemberInfo{}, false, nil
}

func (a sessionShareAPI) recipientKey(ctx context.Context, tenantID, membershipID string) string {
	rows, _ := a.mgr.store.ListMembersByTenant(ctx, tenantID)
	for _, m := range rows {
		if m.MembershipID == membershipID {
			return m.UserKey
		}
	}
	return ""
}

func shareDTO(s SessionShare, recipient string) map[string]any {
	return map[string]any{"id": s.ID, "recipientUserKey": recipient, "scope": map[string]string{
		"type": s.ScopeType, "key": s.ScopeKey}, "permission": s.Permission, "createdAt": s.CreatedAt, "updatedAt": s.UpdatedAt}
}

func (a sessionShareAPI) listOwned(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	rows, err := a.mgr.store.ListSessionSharesByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, shareDTO(s, a.recipientKey(r.Context(), mv.TenantID, s.RecipientMembershipID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

type sharePutBody struct {
	RecipientUserKey string `json:"recipientUserKey"`
	Scope            struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	} `json:"scope"`
	Permission string `json:"permission"`
}

func validSharePermission(p string) bool { return p == "ro" || p == "rw" }
func validShareScope(s string) bool      { return s == "session" || s == "repo" || s == "worktree" }

func (a sessionShareAPI) put(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in sharePutBody
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in) != nil || !validSharePermission(in.Permission) ||
		!validShareScope(in.Scope.Type) || strings.TrimSpace(in.Scope.Key) == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_share", "invalid share request"})
		return
	}
	recipient, ok, err := memberByUserKey(r.Context(), a.mgr.store, res.mv.TenantID, strings.TrimSpace(in.RecipientUserKey))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "member_not_found", "recipient is not an active tenant member"})
		return
	}
	if recipient.MembershipID == res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "share_self", "cannot share with yourself"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to create a share"})
		return
	}
	if err := a.syncCatalog(r.Context(), res); err != nil {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to create a share"})
		return
	}
	catalog, err := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	found := false
	if in.Scope.Type == "session" {
		for _, c := range catalog {
			if c.Name == in.Scope.Key {
				found = true
				break
			}
		}
	} else {
		body, ferr := agentText(r.Context(), res.rt, http.MethodGet, "/repos", nil)
		if ferr == nil {
			var inventory struct {
				Repos []struct {
					WorkingCopyID string `json:"workingCopyId"`
					Worktree      bool   `json:"worktree"`
				} `json:"repos"`
			}
			if json.Unmarshal([]byte(body), &inventory) == nil {
				for _, repo := range inventory.Repos {
					if repo.WorkingCopyID == in.Scope.Key && repo.Worktree == (in.Scope.Type == "worktree") {
						found = true
						break
					}
				}
			}
		}
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "share_target_not_found", "share target not found"})
		return
	}
	now := nowTS()
	row := SessionShare{ID: newID(), TenantID: res.mv.TenantID, OwnerMembershipID: res.mv.MembershipID,
		RecipientMembershipID: recipient.MembershipID, ScopeType: in.Scope.Type, ScopeKey: in.Scope.Key,
		Permission: in.Permission, CreatedAt: now, UpdatedAt: now}
	status := http.StatusCreated
	owned, err := a.mgr.store.ListSessionSharesByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, existing := range owned {
		if existing.RecipientMembershipID == row.RecipientMembershipID && existing.ScopeType == row.ScopeType && existing.ScopeKey == row.ScopeKey {
			row.ID = existing.ID
			row.CreatedAt = existing.CreatedAt
			status = http.StatusOK
			break
		}
	}
	if err := a.mgr.store.PutSessionShare(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: res.mv.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "session.share", Target: in.Scope.Type + ":" + in.Scope.Key, Detail: "permission=" + in.Permission, HTTPStatus: http.StatusCreated, At: now})
	writeJSON(w, status, shareDTO(row, recipient.UserKey))
}

func (a sessionShareAPI) patch(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	var in struct {
		Permission string `json:"permission"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil || !validSharePermission(in.Permission) {
		writeAPIErr(w, &apiError{400, "bad_share", "invalid permission"})
		return
	}
	s, ok, err := a.mgr.store.GetSessionShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || s.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	s.Permission = in.Permission
	s.UpdatedAt = nowTS()
	changed, err := a.mgr.store.UpdateSessionSharePermission(r.Context(), s.ID, mv.MembershipID, s.Permission, s.UpdatedAt)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.share.permission", Target: s.ID, Detail: "permission=" + in.Permission, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, shareDTO(s, a.recipientKey(r.Context(), mv.TenantID, s.RecipientMembershipID)))
}
func (a sessionShareAPI) delete(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	s, ok, err := a.mgr.store.GetSessionShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || s.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "share not found"})
		return
	}
	if err := a.mgr.store.DeleteSessionShare(r.Context(), s.ID, mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.unshare", Target: s.ID, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, map[string]any{"deleted": s.ID})
}

func effectivePermission(shares []SessionShare, c SharedSessionCatalog) string {
	p := ""
	for _, s := range shares {
		match := s.OwnerMembershipID == c.OwnerMembershipID && ((s.ScopeType == "session" && s.ScopeKey == c.Name) || ((s.ScopeType == "repo" || s.ScopeType == "worktree") && s.ScopeKey == c.WorkingCopyID))
		if match {
			if s.Permission == "rw" {
				return "rw"
			}
			p = "ro"
		}
	}
	return p
}

func (a sessionShareAPI) ownerResolved(ctx context.Context, owner string) (*resolved, *apiError) {
	iid, ok, err := a.mgr.store.IdentityIDForMembership(ctx, owner)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		return nil, &apiError{404, "not_found", "owner not found"}
	}
	return a.mgr.resolveByMembership(ctx, iid, owner)
}

func (a sessionShareAPI) refreshOwnerCatalog(ctx context.Context, owner string) {
	res, e := a.ownerResolved(ctx, owner)
	if e == nil && res.rt.State(ctx) == "running" {
		_ = a.syncCatalog(ctx, res)
	}
}

func (a sessionShareAPI) listReceived(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	shares, err := a.mgr.store.ListSessionSharesByRecipient(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	owners := map[string]bool{}
	for _, s := range shares {
		owners[s.OwnerMembershipID] = true
	}
	for owner := range owners {
		a.refreshOwnerCatalog(r.Context(), owner)
	}
	members, _ := a.mgr.store.ListMembersByTenant(r.Context(), mv.TenantID)
	ownerKeys := map[string]string{}
	for _, m := range members {
		ownerKeys[m.MembershipID] = m.UserKey
	}
	out := []any{}
	for owner := range owners {
		catalog, _ := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), owner)
		res, e := a.ownerResolved(r.Context(), owner)
		wsState := "stopped"
		if e == nil {
			wsState = res.rt.State(r.Context())
		}
		for _, c := range catalog {
			p := effectivePermission(shares, c)
			if p == "" {
				continue
			}
			out = append(out, map[string]any{"id": c.ID, "ownerUserKey": ownerKeys[owner], "name": c.Name, "kind": c.Kind, "repo": c.Repo, "workingCopyId": c.WorkingCopyID, "title": c.Title, "label": c.Label, "createdAt": c.CreatedAt, "state": c.State, "archived": c.Archived, "permission": p, "workspaceState": wsState})
		}
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

func (a sessionShareAPI) authorizeCatalog(ctx context.Context, mv MembershipView, id string, wantRW bool) (SharedSessionCatalog, *resolved, *apiError) {
	c, ok, err := a.mgr.store.GetSharedSessionCatalog(ctx, id)
	if err != nil {
		return c, nil, internalErr(err)
	}
	if !ok {
		return c, nil, &apiError{404, "not_found", "shared session not found"}
	}
	res, e := a.ownerResolved(ctx, c.OwnerMembershipID)
	if e != nil {
		return c, nil, e
	}
	// Direct catalog URLs must not bypass live inventory reconciliation. A deleted
	// session or working copy removes its catalog/rule before ACL evaluation.
	if res.rt.State(ctx) == "running" {
		if err := a.syncCatalog(ctx, res); err != nil {
			return c, nil, &apiError{502, "owner_workspace_unreachable", "owner workspace inventory is unavailable"}
		}
		c, ok, err = a.mgr.store.GetSharedSessionCatalog(ctx, id)
		if err != nil {
			return c, nil, internalErr(err)
		}
		if !ok {
			return c, nil, &apiError{404, "not_found", "shared session not found"}
		}
	}
	shares, err := a.mgr.store.ListSessionSharesByRecipient(ctx, mv.MembershipID)
	if err != nil {
		return c, nil, internalErr(err)
	}
	p := effectivePermission(shares, c)
	if p == "" || (wantRW && p != "rw") {
		return c, nil, &apiError{404, "not_found", "shared session not found"}
	}
	return c, res, nil
}

func (a sessionShareAPI) messages(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if !a.allowRead(mv.MembershipID + ":" + c.ID) {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, "shared_read_rate_limited", "too many shared transcript reads"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	path := "/sessions/" + url.PathEscape(c.Name) + "/messages"
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, res.rt.Endpoint()+path, nil)
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		writeAPIErr(w, &apiError{502, "owner_workspace_unreachable", "owner workspace is unreachable"})
		return
	}
	defer resp.Body.Close()
	var payload map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload) != nil {
		writeAPIErr(w, &apiError{502, "bad_owner_response", "invalid owner response"})
		return
	}
	payload = sharedTranscriptDTO(payload)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, resp.StatusCode, payload)
}

// sharedTranscriptDTO is intentionally an allowlist, not a list of known path
// spellings. Agent kinds have historically emitted file, files, file_path and
// filePath; a future structured coordinate must stay private by default. Prose,
// tool summaries/output and edit text are conversation content and remain visible.
func sharedTranscriptDTO(payload map[string]any) map[string]any {
	out := map[string]any{}
	copyAllowed(out, payload, "cursor", "reset", "status", "alive", "firstLine", "hasMore")
	messages, _ := payload["messages"].([]any)
	shared := make([]any, 0, len(messages))
	for _, raw := range messages {
		turn, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		t := map[string]any{}
		copyAllowed(t, turn, "role", "text", "source", "model", "effort", "ctxWindow", "sidechain",
			"inTok", "outTok", "cacheRead", "cacheCreate", "ts", "endTs", "idx", "anchorId")
		parts, _ := turn["parts"].([]any)
		visibleParts := make([]any, 0, len(parts))
		for _, partRaw := range parts {
			part, ok := partRaw.(map[string]any)
			if !ok {
				continue
			}
			p := map[string]any{}
			copyAllowed(p, part, "kind", "text", "tool", "info", "cause", "output", "prompt", "agentType",
				"status", "model", "answer", "plan", "caption", "qid")
			p["questions"] = sharedQuestions(part["questions"])
			p["edits"] = sharedEdits(part["edits"])
			visibleParts = append(visibleParts, p)
		}
		t["parts"] = visibleParts
		shared = append(shared, t)
	}
	out["messages"] = shared
	return out
}

func copyAllowed(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

func sharedQuestions(raw any) []any {
	items, _ := raw.([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		question, ok := item.(map[string]any)
		if !ok {
			continue
		}
		q := map[string]any{}
		copyAllowed(q, question, "id", "header", "question", "multiSelect")
		options, _ := question["options"].([]any)
		visibleOptions := make([]any, 0, len(options))
		for _, optionRaw := range options {
			option, ok := optionRaw.(map[string]any)
			if !ok {
				continue
			}
			o := map[string]any{}
			copyAllowed(o, option, "label", "description", "preview")
			visibleOptions = append(visibleOptions, o)
		}
		q["options"] = visibleOptions
		out = append(out, q)
	}
	return out
}

func sharedEdits(raw any) []any {
	items, _ := raw.([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		edit, ok := item.(map[string]any)
		if !ok {
			continue
		}
		e := map[string]any{}
		copyAllowed(e, edit, "old", "new")
		out = append(out, e)
	}
	return out
}

func (a sessionShareAPI) sealProposal(ctx context.Context, tenant string, body []byte) (string, string, error) {
	if a.mgr.custodian == nil {
		return base64.StdEncoding.EncodeToString(body), "", nil
	}
	ct, err := a.mgr.custodian.Wrap(ctx, tenant, body)
	return ct, tenant, err
}
func (a sessionShareAPI) openProposal(ctx context.Context, p SessionShareProposal) ([]byte, error) {
	if p.KeyRef == "" {
		return base64.StdEncoding.DecodeString(p.Ciphertext)
	}
	return a.mgr.custodian.Unwrap(ctx, p.KeyRef, p.Ciphertext)
}

var proposalActions = map[string]string{"turn": "turn", "respond": "respond", "answer-question": "answer-question", "plan-respond": "plan-respond"}

func (a sessionShareAPI) propose(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	c, res, e := a.authorizeCatalog(r.Context(), mv, r.PathValue("id"), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, shareProposalMaxBytes+1))
	if err != nil || len(raw) > shareProposalMaxBytes {
		writeAPIErr(w, &apiError{413, "proposal_too_large", "proposal exceeds 32 KiB"})
		return
	}
	var in struct {
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &in) != nil || proposalActions[in.Action] == "" || len(in.Payload) == 0 {
		writeAPIErr(w, &apiError{400, "bad_proposal", "invalid proposal"})
		return
	}
	if err := a.mgr.store.ExpireSessionShareProposals(r.Context(), c.OwnerMembershipID, nowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	ct, kr, err := a.sealProposal(r.Context(), mv.TenantID, in.Payload)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	now := time.Now().UTC()
	p := SessionShareProposal{ID: newID(), TenantID: mv.TenantID, CatalogID: c.ID, OwnerMembershipID: c.OwnerMembershipID, ProposerMembershipID: mv.MembershipID, Action: in.Action, Ciphertext: ct, KeyRef: kr, Status: "pending", CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}
	created, err := a.mgr.store.CreateSessionShareProposalLimited(r.Context(), p, shareProposalMaxOpen)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !created {
		writeAPIErr(w, &apiError{429, "too_many_proposals", "too many pending proposals"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "session.share.proposal.create", Target: c.Name, Detail: "action=" + p.Action, HTTPStatus: http.StatusCreated, At: nowTS()})
	writeJSON(w, 201, map[string]any{"id": p.ID, "status": p.Status, "expiresAt": p.ExpiresAt})
}

func (a sessionShareAPI) listProposals(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	w.Header().Set("Cache-Control", "private, no-store")
	if err := a.mgr.store.ExpireSessionShareProposals(r.Context(), mv.MembershipID, nowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	rows, err := a.mgr.store.ListSessionShareProposalsByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := []any{}
	for _, p := range rows {
		body := json.RawMessage(nil)
		if p.Ciphertext != "" {
			body, _ = a.openProposal(r.Context(), p)
		}
		out = append(out, map[string]any{"id": p.ID, "sessionId": p.CatalogID, "proposerUserKey": a.recipientKey(r.Context(), mv.TenantID, p.ProposerMembershipID), "action": p.Action, "payload": body, "status": p.Status, "createdAt": p.CreatedAt, "expiresAt": p.ExpiresAt})
	}
	writeJSON(w, 200, map[string]any{"proposals": out})
}

func (a sessionShareAPI) reject(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	p, ok, err := a.mgr.store.GetSessionShareProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || p.OwnerMembershipID != mv.MembershipID || p.Status != "pending" {
		writeAPIErr(w, &apiError{404, "not_found", "proposal not found"})
		return
	}
	if changed, err := a.mgr.store.TransitionSessionShareProposal(r.Context(), p.ID, "pending", "rejected", ident.ID, nowTS(), true); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if !changed {
		writeAPIErr(w, &apiError{http.StatusConflict, "proposal_already_decided", "proposal was already decided"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "session.share.proposal.reject", Target: p.CatalogID, Detail: "proposer=" + p.ProposerMembershipID + " action=" + p.Action, HTTPStatus: http.StatusOK, At: nowTS()})
	writeJSON(w, 200, map[string]any{"status": "rejected"})
}

func (a sessionShareAPI) approve(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	p, ok, err := a.mgr.store.GetSessionShareProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || p.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{404, "not_found", "proposal not found"})
		return
	}
	res, e := a.ownerResolved(r.Context(), p.OwnerMembershipID)
	if e != nil {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	workspaceLock := a.mgr.startLockFor(res.ws.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{409, "owner_workspace_stopped", "owner workspace is stopped"})
		return
	}
	if p.Status == "processing" {
		state, status := lookupAgentShareOperation(res.rt, p.ID)
		if state == "applied" && status < http.StatusBadRequest {
			changed, err := a.mgr.store.TransitionSessionShareProposal(r.Context(), p.ID, "processing", "approved", ident.ID, nowTS(), true)
			if err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
			if changed {
				writeJSON(w, 200, map[string]any{"status": "approved", "reconciled": true})
				return
			}
		}
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation was claimed; its outcome cannot be retried safely"})
		return
	}
	if p.Status != "pending" {
		writeAPIErr(w, &apiError{409, "proposal_already_decided", "proposal was already decided"})
		return
	}
	if err := a.syncCatalog(r.Context(), res); err != nil {
		writeAPIErr(w, &apiError{502, "owner_workspace_unreachable", "owner workspace inventory is unavailable"})
		return
	}
	body, err := a.openProposal(r.Context(), p)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Do not bind the durable claim/Agent call to the browser connection. If the
	// response is lost, the transaction must still commit processing rather than
	// roll back and make a side effect retryable.
	applyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err := a.mgr.store.ClaimAndApplySessionShareProposal(applyCtx, p.ID, mv.MembershipID, ident.ID, nowTS(),
		func(claimed SessionShareProposal, catalog SharedSessionCatalog) error {
			path := "/sessions/" + url.PathEscape(catalog.Name) + "/" + proposalActions[claimed.Action]
			return agentShareOperation(applyCtx, res.rt, path, body, claimed.ID)
		})
	if err != nil {
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation result is unknown and will not be retried: " + err.Error()})
		return
	}
	switch state {
	case "approved":
	case "expired":
		writeAPIErr(w, &apiError{409, "share_changed", "proposal expired or RW share is no longer active"})
		return
	case "processing":
		writeAPIErr(w, &apiError{409, "proposal_outcome_unknown", "the operation was claimed; its outcome cannot be retried safely"})
		return
	default:
		writeAPIErr(w, &apiError{404, "not_found", "proposal or shared session not found"})
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{ID: newID(), TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID, Action: "session.share.proposal.approve", Target: p.CatalogID, Detail: "proposer=" + p.ProposerMembershipID + " action=" + p.Action, HTTPStatus: 200, At: nowTS()})
	writeJSON(w, 200, map[string]any{"status": "approved"})
}

func agentShareOperation(ctx context.Context, rt Runtime, path string, body []byte, operationID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.Endpoint()+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Fleet-Operation-ID", operationID)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusBadRequest {
		return &agentHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(payload))}
	}
	return nil
}

func lookupAgentShareOperation(rt Runtime, operationID string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rt.Endpoint()+"/share-operations/"+url.PathEscape(operationID), nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	var result struct {
		State      string `json:"state"`
		StatusCode int    `json:"statusCode"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil {
		return "", 0
	}
	return result.State, result.StatusCode
}

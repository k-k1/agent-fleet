package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Personal Access Tokens (docs/decisions/0006, P3-6). A user issues PATs in the
// Console; they authenticate the MCP endpoint. The token carries the issuer's
// identity+membership; role is resolved live at call time. scope is fixed at
// issuance and clamped to the issuer's ceiling.
//
// scope ladder (read < write < admin:dangerous): a token grants its level and
// every level below it. member/drive read tools need `read`, send_to_session
// needs `write`; admin mutating tools (P3-6 later phases) need higher levels.
const (
	scopeRead      = "read"
	scopeWrite     = "write"
	scopeDangerous = "admin:dangerous"
)

func scopeRank(s string) int {
	switch s {
	case scopeRead:
		return 1
	case scopeWrite:
		return 2
	case scopeDangerous:
		return 3
	}
	return 0
}

// ceilingScope is the highest scope a person may mint, from their deployment role.
func ceilingScope(role string) string {
	if role == "super_admin" {
		return scopeDangerous
	}
	return scopeWrite
}

// clampScope normalizes a requested scope and caps it at the ceiling.
func clampScope(requested, ceiling string) string {
	if scopeRank(requested) == 0 {
		requested = scopeRead
	}
	if scopeRank(requested) > scopeRank(ceiling) {
		return ceiling
	}
	return requested
}

// newPATToken mints a fresh token and its storage hash. The plaintext is shown
// to the user exactly once; only the hash is persisted.
func newPATToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = "af_pat_" + base64.RawURLEncoding.EncodeToString(b[:])
	return token, hashPAT(token)
}

func hashPAT(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// patAPI は PAT（docs/decisions/0006 P3-6）の機能ハンドラ集（docs/log/23 残③）。
// 解決は埋め込みの memberAuth（登録側で withIdentity に包む）、store は PATStore
// の narrow view だけを持つ。create のテナント選択だけは a.mgr.membershipsFor を
// 経由する（memberAuth が mgr を運ぶ）。
type patAPI struct {
	memberAuth
	store PATStore
}

func newPATAPI(m *manager) patAPI { return patAPI{memberAuth{m}, m.store} }

// maxActivePATs caps issuance per identity (未失効トークン数)。使い捨て発行の
// 積み上げでハッシュ照合対象が際限なく増えるのを防ぐ。
const maxActivePATs = 20

// auditPAT records a PAT lifecycle event to the audit ledger (best-effort).
func (a patAPI) auditPAT(ctx context.Context, tenantID, actorID, action, target, detail string) {
	_ = a.mgr.store.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
		Action: action, Target: target, Detail: detail, At: nowTS(),
	})
}

// list (GET /api/pat) — the caller's tokens (no secrets/hashes).
func (a patAPI) list(w http.ResponseWriter, r *http.Request, ident Identity) {
	pats, err := a.store.ListPATsByIdentity(r.Context(), ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(pats))
	for _, p := range pats {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "scope": p.Scope,
			"created_at": p.CreatedAt, "expires_at": p.ExpiresAt,
			"revoked_at": p.RevokedAt, "last_used_at": p.LastUsedAt,
			"revoked": p.RevokedAt != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out, "ceiling": ceilingScope(ident.Role)})
}

// create (POST /api/pat {name, scope, tenant, ttl_days}) mints a token.
// scope is clamped to the issuer's ceiling. ttl_days: omitted/0 = 90 days,
// negative = never expires, positive = that many days. Returns the secret once.
func (a patAPI) create(w http.ResponseWriter, r *http.Request, ident Identity) {
	var body struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Tenant  string `json:"tenant"`
		TTLDays *int   `json:"ttl_days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	ms, aerr := a.mgr.membershipsFor(r.Context(), ident)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	tenantSel := body.Tenant
	if tenantSel == "" {
		tenantSel = r.Header.Get("X-AF-Tenant") // Console injects the active tenant
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}

	// 発行数上限: 未失効トークンが上限に達していたら失効させてから、と促す。
	existing, err := a.store.ListPATsByIdentity(r.Context(), ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	active := 0
	for _, p := range existing {
		if p.RevokedAt == "" {
			active++
		}
	}
	if active >= maxActivePATs {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "too_many_tokens",
			"active token limit reached; revoke unused tokens first"})
		return
	}

	scope := clampScope(body.Scope, ceilingScope(ident.Role))
	expires := ""
	switch {
	case body.TTLDays == nil || *body.TTLDays == 0:
		expires = time.Now().UTC().AddDate(0, 0, 90).Format(time.RFC3339)
	case *body.TTLDays > 0:
		expires = time.Now().UTC().AddDate(0, 0, *body.TTLDays).Format(time.RFC3339)
		// negative => never expires (expires stays "")
	}

	token, hash := newPATToken()
	p := PAT{
		ID:           newID(),
		IdentityID:   ident.ID,
		MembershipID: mv.MembershipID,
		Scope:        scope,
		Name:         strings.TrimSpace(body.Name),
		CreatedAt:    nowTS(),
		ExpiresAt:    expires,
	}
	if err := a.store.CreatePAT(r.Context(), p, hash); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditPAT(r.Context(), mv.TenantID, ident.ID, "pat.create", p.ID,
		"scope="+scope+" name="+p.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, // shown once
		"id":    p.ID, "name": p.Name, "scope": scope,
		"tenant": mv.TenantSlug, "expires_at": expires,
	})
}

// revoke (DELETE /api/pat/{id}) revokes one of the caller's tokens.
func (a patAPI) revoke(w http.ResponseWriter, r *http.Request, ident Identity) {
	id := r.PathValue("id")
	if err := a.store.RevokePAT(r.Context(), id, ident.ID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditPAT(r.Context(), "", ident.ID, "pat.revoke", id, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// httpapi.go — 共有 JSON レスポンスヘルパ（writeJSON / writeAPIErr、docs/23 P2-W1）と、
// 機能別ハンドラ struct の共通土台 memberAuth（docs/23 残③）。ハンドラ冒頭にコピー
// されていた解決プリアンブル（membershipFor / resolvedFor / requireSuperAdmin）を
// 登録側で包むラッパーに畳む。各機能 API は memberAuth を埋め込み、store は
// 必要最小のサブインターフェース（store.go）だけを持つ — memo.go の memoAPI が実例。
package main

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	// 呼び出し側が JSON 系の専用 Content-Type（LFS の application/vnd.git-lfs+json 等）
	// を先に設定していたら尊重する。未設定時のみ既定を付ける。
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, map[string]any{
		"error": map[string]string{"code": e.code, "message": e.message},
	})
}

// memberAuth is the shared resolution base every feature API embeds. Tenant
// selection: header for REST; query param for WS/new-tab (browsers can't set
// custom headers there).
type memberAuth struct{ mgr *manager }

func tenantSel(r *http.Request) string {
	if t := r.Header.Get("X-AF-Tenant"); t != "" {
		return t
	}
	return r.URL.Query().Get("tenant")
}

// withMembership adapts a handler needing (identity, active membership) —
// lightweight per-member CRUD, no workspace build. 401/403/409 mirror withResolved.
func (a memberAuth) withMembership(h func(http.ResponseWriter, *http.Request, Identity, MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := a.mgr.resolveIdentity(r)
		if id.key == "" {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
			return
		}
		ident, mv, aerr := a.mgr.resolveMembership(r.Context(), id.key, id.email, tenantSel(r))
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, ident, mv)
	}
}

// withResolved adapts a handler needing the full per-request resolution
// (runtime + workspace record + identity + membership), creating the workspace
// on first use.
func (a memberAuth) withResolved(h func(http.ResponseWriter, *http.Request, *resolved)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := a.mgr.resolveIdentity(r)
		if id.key == "" {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
			return
		}
		res, aerr := a.mgr.resolveFull(r.Context(), id.key, id.email, tenantSel(r))
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, res)
	}
}

// withIdentity adapts a handler needing only the upserted caller identity
// (PAT CRUD, tenant picker).
func (a memberAuth) withIdentity(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, aerr := a.mgr.identityFor(r.Context(), r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, ident)
	}
}

// superAdminFor resolves the caller and requires identity.Role ==
// "super_admin" (writes 401/403 and reports ok=false). withSuperAdmin is the
// wrapper form for handlers gated at the top; this callable form serves gates
// taken mid-handler (adminAPI.tenantScope).
func (a memberAuth) superAdminFor(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	ident, aerr := a.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, false
	}
	if ident.Role != "super_admin" {
		writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "super_admin required"})
		return Identity{}, false
	}
	return ident, true
}

// withSuperAdmin gates a deployment-wide admin handler on identity.Role.
func (a memberAuth) withSuperAdmin(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := a.superAdminFor(w, r)
		if !ok {
			return
		}
		h(w, r, ident)
	}
}

// anyTenantAdminFor gates a DEPLOYMENT-WIDE READ that a tenant administrator
// legitimately needs in order to administer their own tenant — currently only the
// list of the deployment's sign-in methods (docs/61 §61.17.9 ①). It allows a
// super_admin, or anyone holding an active tenant_admin membership of ANY tenant.
//
// ★ It is a strictly weaker gate than withSuperAdmin, so it must never be put in
// front of a WRITE: which tenant the caller administers is not checked here (there
// is no slug to check it against), so the only thing it may authorize is reading
// something that is the same for every tenant. Anything scoped to one tenant keeps
// using tenantAdminFor, which does check.
//
// ★ Plain members are refused. The list is not secret — the ids and button labels
// are on the unauthenticated /login — but "not secret" is not a reason to widen a
// gate; the caller has to have a use for it (決定 19 の *編集* のゲートは別で、
// そちらは withSuperAdmin のまま).
func (a memberAuth) anyTenantAdminFor(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	ident, aerr := a.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, false
	}
	if ident.Role == "super_admin" {
		return ident, true
	}
	// ListMemberships returns ACTIVE rows only, so a tenant_admin who was taken off
	// the roster stops passing here as soon as the row is deactivated (docs/61
	// §61.10.7 の穴 2 と同じ性質)。
	ms, err := a.mgr.store.ListMemberships(r.Context(), ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return Identity{}, false
	}
	for _, mv := range ms {
		if mv.Role == "tenant_admin" {
			return ident, true
		}
	}
	writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "tenant admin required"})
	return Identity{}, false
}

// withAnyTenantAdmin is the wrapper form of anyTenantAdminFor.
func (a memberAuth) withAnyTenantAdmin(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := a.anyTenantAdminFor(w, r)
		if !ok {
			return
		}
		h(w, r, ident)
	}
}

// tenantAdminFor gates a per-tenant admin endpoint: allows a deployment
// super_admin (any tenant) or a tenant_admin of `slug`. Resolves and returns the
// caller's identity and the target tenant. Writes 401/403/404 and returns ok=false
// on failure. slug comes from the path on some routes and the body on others, so
// it is passed explicitly (hence a mid-handler call, not a with* wrapper).
func (a memberAuth) tenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (Identity, Tenant, bool) {
	ident, aerr := a.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, Tenant{}, false
	}
	t, ok, err := a.mgr.store.GetTenantBySlug(r.Context(), slug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return Identity{}, Tenant{}, false
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return Identity{}, Tenant{}, false
	}
	if !a.mgr.tenantAdminFor(r.Context(), ident, t.ID) {
		writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "tenant admin required"})
		return Identity{}, Tenant{}, false
	}
	return ident, t, true
}

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
	w.Header().Set("Content-Type", "application/json")
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

// withSuperAdmin gates a deployment-wide admin handler on identity.Role.
func (a memberAuth) withSuperAdmin(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, aerr := a.mgr.identityFor(r.Context(), r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		if ident.Role != "super_admin" {
			writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "super_admin required"})
			return
		}
		h(w, r, ident)
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Admin API for the tenant's git provider OAuth apps (docs/71 §71.4 + ADR0052).
//
// The whole surface belongs to the tenant_admin — read, write and delete. There is no
// super_admin step, and that is the decision, not an omission: registering an OAuth app
// for cloning repositories declares nothing about who anybody is (the thing tenant_idp's
// approval exists to guard), the redirect_uri is the CP's own so an app cannot redirect a
// grant anywhere else, and the resulting token is written into the member's own workspace
// and never returned to the administrator. A deployment run with AUTH=dev has no
// super_admin at all (docs/71 §71.6), so an approval step would also simply never clear
// there.
type tenantGitOAuthAPI struct{ memberAuth }

func newTenantGitOAuthAPI(m *manager) tenantGitOAuthAPI { return tenantGitOAuthAPI{memberAuth{m}} }

// gitOAuthBody is the wire shape. ClientSecret is write-only: it is never returned, and
// a save that leaves it empty keeps the stored value — the same contract tenant_idp and
// mcp_server use for their secrets.
type gitOAuthBody struct {
	Provider     string `json:"provider"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	// Read-only.
	HasSecret   bool   `json:"has_secret"`
	NeedsSecret bool   `json:"needs_secret"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	// RedirectURI is what the administrator has to paste into the provider's app
	// registration. It is derived from PUBLIC_BASE_URL and is empty for a provider whose
	// flow has no callback (GitHub's device flow). Returning it is the difference between
	// a form somebody can complete and one they have to guess at.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// list (GET /api/admin/tenants/{slug}/git-oauth) — one entry per KNOWN provider, whether
// or not a row exists. The screen is a fixed pair of cards, so an unregistered provider
// has to come back as an empty card rather than be absent.
func (a tenantGitOAuthAPI) list(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rows, err := a.mgr.store.ListTenantGitOAuth(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	byProvider := make(map[string]TenantGitOAuth, len(rows))
	for _, row := range rows {
		byProvider[row.Provider] = row
	}
	out := make([]gitOAuthBody, 0, len(gitOAuthProviders))
	for _, p := range gitOAuthProviders {
		row := byProvider[p]
		out = append(out, gitOAuthBody{
			Provider: p, ClientID: row.ClientID,
			HasSecret: row.SecretEnc != "", NeedsSecret: gitOAuthNeedsSecret(p),
			UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt,
			RedirectURI: a.mgr.gitOAuthRedirectURI(p),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "tenant": t.Slug})
}

// save (PUT /api/admin/tenants/{slug}/git-oauth/{provider}).
func (a tenantGitOAuthAPI) save(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if !validGitOAuthProvider(provider) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_provider", "unknown git provider: " + provider})
		return
	}
	var b gitOAuthBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	b.ClientID = strings.TrimSpace(b.ClientID)
	b.ClientSecret = strings.TrimSpace(b.ClientSecret)
	if b.ClientID == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "client_id is required"})
		return
	}
	prev, existed, err := a.mgr.store.GetTenantGitOAuth(r.Context(), t.ID, provider)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The secret is write-only, so an empty field means "keep what is stored" — the
	// editor never sees it and therefore cannot retype it. The one place that has to be
	// refused is a FIRST save of a provider that needs one: silently storing an empty
	// secret produces a row that looks configured and fails at the token exchange.
	secretEnc, keyRef := prev.SecretEnc, prev.KeyRef
	if b.ClientSecret != "" {
		if secretEnc, keyRef, err = a.mgr.sealTenantSecret(r.Context(), t.ID, b.ClientSecret); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	if gitOAuthNeedsSecret(provider) && secretEnc == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "secret_required",
			"client_secret is required for " + provider})
		return
	}
	// GitHub's device flow has no secret. Storing one anyway would put a credential in
	// the database that nothing ever reads and that nobody would think to rotate.
	if !gitOAuthNeedsSecret(provider) {
		secretEnc, keyRef = "", ""
	}
	now := nowTS()
	row := TenantGitOAuth{
		ID: prev.ID, TenantID: t.ID, Provider: provider, ClientID: b.ClientID,
		SecretEnc: secretEnc, KeyRef: keyRef, UpdatedBy: ident.ID,
		CreatedAt: prev.CreatedAt, UpdatedAt: now,
	}
	if !existed {
		row.ID, row.CreatedAt = newID(), now
	}
	if err := a.mgr.store.PutTenantGitOAuth(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The client_id is not a secret, so it is recorded: "which app was this tenant
	// pointed at on that day" is the question an audit of a leaked grant starts from.
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.git_oauth_save", Target: provider,
		Detail: "client_id=" + b.ClientID + " secret=" + boolWord(secretEnc != ""), At: now,
	})
	writeJSON(w, http.StatusOK, gitOAuthBody{
		Provider: provider, ClientID: row.ClientID, HasSecret: secretEnc != "",
		NeedsSecret: gitOAuthNeedsSecret(provider), UpdatedBy: ident.ID, UpdatedAt: now,
		RedirectURI: a.mgr.gitOAuthRedirectURI(provider),
	})
}

// remove (DELETE /api/admin/tenants/{slug}/git-oauth/{provider}) takes the OAuth option
// away from this tenant's members. Connections already made keep working: the token is
// in the member's workspace and Bitbucket's refresh credentials were copied there at
// connect time — this removes the way to make NEW ones.
func (a tenantGitOAuthAPI) remove(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if !validGitOAuthProvider(provider) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_provider", "unknown git provider: " + provider})
		return
	}
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if err := a.mgr.store.DeleteTenantGitOAuth(r.Context(), t.ID, provider); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.git_oauth_delete", Target: provider, At: nowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "provider": provider})
}

// availability (GET /api/git-oauth) is the MEMBER's half: which OAuth buttons this
// person's tenant can actually offer.
//
// It exists because the alternative is a button that reports not_configured only after
// it is pressed — and the member cannot fix that, since the setting is their tenant
// admin's. The screen needs to know before it draws the option.
//
// ★ Deliberately CP-native and not folded into GET /api/connections, which is proxied to
// the Agent: the answer lives in the CP's database, and a workspace that is stopped
// (502 from the proxy) is exactly when somebody is looking at this tab.
func (a tenantGitOAuthAPI) availability(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	out := map[string]any{}
	for _, p := range gitOAuthProviders {
		out[p] = map[string]any{"configured": a.mgr.gitOAuthConfigured(r.Context(), mv.TenantID, p)}
	}
	writeJSON(w, http.StatusOK, out)
}

// gitOAuthRedirectURI is the callback the tenant has to register with the provider, or
// "" when the flow has none. One definition, used by the admin form and by the flow
// itself (oauth_bitbucket.go), so the value shown can never drift from the value sent.
func (m *manager) gitOAuthRedirectURI(provider string) string {
	if provider != gitOAuthBitbucket || m.publicBaseURL == "" {
		return ""
	}
	return strings.TrimRight(m.publicBaseURL, "/") + "/api/oauth/bitbucket/callback"
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

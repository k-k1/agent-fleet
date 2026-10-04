package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Admin API for the tenant's git provider OAuth apps (docs/log/71 §71.4 + ADR0052).
//
// The whole surface belongs to the tenant_admin — read, write and delete. There is no
// super_admin step, and that is the decision, not an omission: registering an OAuth app
// for cloning repositories declares nothing about who anybody is (the thing tenant_idp's
// approval exists to guard), the redirect_uri is the CP's own so an app cannot redirect a
// grant anywhere else, and the resulting token is written into the member's own workspace
// and never returned to the administrator. A deployment run with AUTH=dev has no
// super_admin at all (docs/log/71 §71.6), so an approval step would also simply never clear
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

	// GitHub only (issue #1667). Source is writable: none / builtin_oauth / builtin_app /
	// custom, and a save without it means custom so a client written against the old
	// shape keeps working. InstallURL is writable for a custom GitHub App.
	Source     string `json:"source,omitempty"`
	InstallURL string `json:"install_url,omitempty"`
	// Read-only. IsDefault says Source is the default of a tenant with no row. AppType /
	// AppTypeBy are what the client_id turned out to be and how that was learnt.
	IsDefault bool               `json:"is_default,omitempty"`
	AppType   string             `json:"app_type,omitempty"`
	AppTypeBy string             `json:"app_type_by,omitempty"`
	Builtin   *githubBuiltinWire `json:"builtin,omitempty"`
}

// githubBuiltinWire tells the screen which built-in sources it may offer, and why one
// it may not is missing: a build without the apps and an operator who switched them off
// are fixed by different people.
type githubBuiltinWire struct {
	OAuthApp      bool `json:"oauth_app"`
	GitHubApp     bool `json:"github_app"`
	OffByOperator bool `json:"off_by_operator,omitempty"`
}

// githubCard fills the GitHub-only fields of a card from the tenant's effective app.
func (m *manager) githubCard(ctx context.Context, tenantID string, b *gitOAuthBody) error {
	app, _, err := m.githubOAuthApp(ctx, tenantID)
	if err != nil {
		return err
	}
	b.Source, b.IsDefault = app.Source, app.Default
	if app.Source == ghSourceCustom {
		b.AppType, b.AppTypeBy, b.InstallURL = app.Type, app.TypeBy, app.InstallURL
	} else {
		b.ClientID = ""
	}
	b.Builtin = &githubBuiltinWire{
		OAuthApp:      m.githubBuiltinAvailable(ghSourceBuiltinOAuth),
		GitHubApp:     m.githubBuiltinAvailable(ghSourceBuiltinApp),
		OffByOperator: m.githubBuiltinOff,
	}
	return nil
}

// list (GET /api/admin/tenants/{slug}/git-oauth) — one entry per KNOWN provider, whether
// or not a row exists. The screen shows one card per provider in gitOAuthProviders, so an
// unregistered provider has to come back as an empty card rather than be absent.
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
	byProvider := make(map[string]store.TenantGitOAuth, len(rows))
	for _, row := range rows {
		byProvider[row.Provider] = row
	}
	out := make([]gitOAuthBody, 0, len(gitOAuthProviders))
	for _, p := range gitOAuthProviders {
		row := byProvider[p]
		card := gitOAuthBody{
			Provider: p, ClientID: row.ClientID,
			HasSecret: row.SecretEnc != "", NeedsSecret: gitOAuthNeedsSecret(p),
			UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt,
			RedirectURI: a.mgr.gitOAuthRedirectURI(p),
		}
		if p == gitOAuthGitHub {
			if err := a.mgr.githubCard(r.Context(), t.ID, &card); err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
		}
		out = append(out, card)
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
	var gh githubSave
	if provider == gitOAuthGitHub {
		var aerr *apiError
		if gh, aerr = a.mgr.prepareGitHubSave(&b); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	} else if b.ClientID == "" {
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
	now := store.NowTS()
	row := store.TenantGitOAuth{
		ID: prev.ID, TenantID: t.ID, Provider: provider, ClientID: b.ClientID,
		SecretEnc: secretEnc, KeyRef: keyRef, UpdatedBy: ident.ID,
		Source: gh.source, AppType: gh.appType, AppTypeBy: gh.appTypeBy, InstallURL: gh.installURL,
		CreatedAt: prev.CreatedAt, UpdatedAt: now,
	}
	if !existed {
		row.ID, row.CreatedAt = store.NewID(), now
	}
	if err := a.mgr.store.PutTenantGitOAuth(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The client_id is not a secret, so it is recorded: "which app was this tenant
	// pointed at on that day" is the question an audit of a leaked grant starts from.
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.git_oauth_save", Target: provider,
		Detail: "client_id=" + b.ClientID + " secret=" + boolWord(secretEnc != "") + gh.auditDetail(), At: now,
	})
	out := gitOAuthBody{
		Provider: provider, ClientID: row.ClientID, HasSecret: secretEnc != "",
		NeedsSecret: gitOAuthNeedsSecret(provider), UpdatedBy: ident.ID, UpdatedAt: now,
		RedirectURI: a.mgr.gitOAuthRedirectURI(provider),
	}
	if provider == gitOAuthGitHub {
		if err := a.mgr.githubCard(r.Context(), t.ID, &out); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// githubSave is the GitHub-only part of a save, decided before anything is written.
type githubSave struct {
	source, appType, appTypeBy, installURL string
}

func (g githubSave) auditDetail() string {
	if g.source == "" {
		return ""
	}
	d := " source=" + g.source
	if g.appType != "" {
		d += " app_type=" + g.appType + "(" + g.appTypeBy + ")"
	}
	return d
}

// prepareGitHubSave validates a GitHub save and, for a custom client_id, asks GitHub
// what kind of app it is. A client_id GitHub does not know, or an app with the device
// flow switched off, is refused here: saved, it would look configured and fail only when
// a member presses the button.
func (m *manager) prepareGitHubSave(b *gitOAuthBody) (githubSave, *apiError) {
	src := strings.TrimSpace(b.Source)
	if src == "" {
		src = ghSourceCustom
	}
	switch src {
	case ghSourceNone:
		b.ClientID = ""
		return githubSave{source: src}, nil
	case ghSourceBuiltinOAuth, ghSourceBuiltinApp:
		if !m.githubBuiltinAvailable(src) {
			return githubSave{}, &apiError{http.StatusBadRequest, "builtin_unavailable",
				"this deployment does not offer the built-in GitHub app " + src}
		}
		// The client_id comes from the binary at use time, so a release can replace the
		// app without rewriting rows.
		b.ClientID = ""
		return githubSave{source: src}, nil
	case ghSourceCustom:
	default:
		return githubSave{}, &apiError{http.StatusBadRequest, "bad_source", "unknown source: " + src}
	}
	if b.ClientID == "" {
		return githubSave{}, &apiError{http.StatusBadRequest, "bad_request", "client_id is required"}
	}
	g := githubSave{source: src}
	if raw := strings.TrimSpace(b.InstallURL); raw != "" {
		page, ok := normalizeGitHubAppURL(raw)
		if !ok {
			return githubSave{}, &apiError{http.StatusBadRequest, "bad_install_url",
				"the install URL must be https://github.com/apps/<app name>"}
		}
		g.installURL = page
	}
	appType, by, perr := ghProbeAppType(b.ClientID)
	if perr != nil {
		return githubSave{}, &apiError{http.StatusBadRequest, perr.code, perr.msg}
	}
	g.appType, g.appTypeBy = appType, by
	if appType == ghTypeOAuthApp {
		// An OAuth App has nothing to install; a stale URL from an earlier app would
		// send members to the wrong place.
		g.installURL = ""
	}
	return g, nil
}

// remove (DELETE /api/admin/tenants/{slug}/git-oauth/{provider}) takes the OAuth option
// away from this tenant's members and removes the way to make NEW connections. Existing
// tokens stay in the members' workspaces, but from now on the CP's refresh bridge
// (git_oauth_bridge.go) answers not_configured for Bitbucket and Jira, so those
// connections can no longer renew through it. A Bitbucket store written before the
// bridge still holds key/secret and may refresh directly (gitx.RefreshBitbucket).
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
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.git_oauth_delete", Target: provider, At: store.NowTS(),
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
func (a tenantGitOAuthAPI) availability(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	out := map[string]any{}
	for _, p := range gitOAuthProviders {
		out[p] = map[string]any{"configured": a.mgr.gitOAuthConfigured(r.Context(), mv.TenantID, p)}
	}
	// A GitHub App reaches only the repositories it is installed on, so the member is
	// shown where to install it before connecting rather than after a clone fails.
	if app, ok, err := a.mgr.githubOAuthApp(r.Context(), mv.TenantID); err == nil && ok && app.Type == ghTypeGitHubApp {
		out[gitOAuthGitHub] = map[string]any{"configured": true, "app_type": app.Type, "install_url": app.InstallURL}
	}
	writeJSON(w, http.StatusOK, out)
}

// gitOAuthRedirectURI is the callback the tenant has to register with the provider, or
// "" when the flow has none. One definition, used by the admin form and by the flow
// itself (oauth_bitbucket.go), so the value shown can never drift from the value sent.
// (GitHub uses the device flow and has none; Bitbucket and Jira are code grants.)
func (m *manager) gitOAuthRedirectURI(provider string) string {
	if m.publicBaseURL == "" {
		return ""
	}
	switch provider {
	case gitOAuthBitbucket, gitOAuthJira:
		return strings.TrimRight(m.publicBaseURL, "/") + "/api/oauth/" + provider + "/callback"
	}
	return ""
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// dev_idp_client.go — what the development OIDC IdP knows about a workspace: its client
// registration, the claims it asserts for a member, and which redirect URIs it will send a
// browser to (ADR 0116 decisions 5 to 8).
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// devIdPPath is where the IdP lives on the Console origin: the issuer is PUBLIC_BASE_URL plus
// this. A distinctive path on purpose — a production configuration that trusts it is visible
// in a grep.
const devIdPPath = "/dev-idp"

// googleRealm is the issuer an IdentityLink carries for a Google sign-in.
const googleRealm = "https://accounts.google.com"

// devIdPIssuer is the issuer URL, or "" when the IdP is not served: the deployment switch
// is off (devIdPEnabled), or there is no Console origin to put it on. Every caller asks per
// request, so a switch flipped in the admin modal is followed without a restart.
func (m *manager) devIdPIssuer(ctx context.Context) string {
	base := strings.TrimRight(m.publicBaseURL, "/")
	if base == "" || !m.devIdPOfferedToMembers() || !m.devIdPEnabled(ctx) {
		return ""
	}
	return base + devIdPPath
}

// devIdPOfferedToMembers is false on a deployment without a preview domain. Without preview
// hosts the only redirect target is localhost, which only the workspace's browser pane can
// reach, and that pane would itself need a Console session to pass authGate. devIdPIssuer
// folds it in, so such a deployment serves nothing and injects nothing.
func (m *manager) devIdPOfferedToMembers() bool {
	return m.previewDomain != ""
}

func devIdPRandom(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("dev idp: crypto/rand: " + err.Error()) // no sane way to carry on without entropy
	}
	return devIdPB64.EncodeToString(b)
}

func devIdPHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// ensureDevIdPClient returns the workspace's registration, creating it on first use. The
// secret is unsealed here because the container gets it again at every start.
func (m *manager) ensureDevIdPClient(ctx context.Context, ws store.Workspace) (clientID, secret string, err error) {
	row, ok, err := m.store.GetDevIdPClient(ctx, ws.ID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		secret = "afdevsec-" + devIdPRandom(32)
		enc, ref, err := m.sealTenantSecret(ctx, ws.TenantID, secret)
		if err != nil {
			return "", "", fmt.Errorf("seal client secret: %w", err)
		}
		fresh := store.DevIdPClient{
			WorkspaceID: ws.ID, MembershipID: ws.MembershipID, ClientID: "afdev-" + strings.ToLower(devIdPRandom(12)),
			SecretEnc: enc, KeyRef: ref, CreatedAt: time.Now().Unix(),
		}
		created, err := m.store.CreateDevIdPClient(ctx, fresh)
		if err != nil {
			return "", "", err
		}
		if created {
			return fresh.ClientID, secret, nil
		}
		// Another request registered it first: use theirs.
		if row, ok, err = m.store.GetDevIdPClient(ctx, ws.ID); err != nil || !ok {
			return "", "", fmt.Errorf("client registration vanished: %v", err)
		}
	}
	secret, err = m.openTenantSecret(ctx, row.SecretEnc, row.KeyRef)
	if err != nil {
		return "", "", err
	}
	return row.ClientID, secret, nil
}

// devIdPEnv is the configuration an app switches its sign-in to, as container env. It is
// injected at every start while the deployment offers the IdP. There is no per-workspace
// switch: the env is fixed at start while a switch is not, so a switch could only break apps
// that follow the guide. Nothing is injected when the deployment does not offer it.
func (m *manager) devIdPEnv(ctx context.Context, ws store.Workspace) []string {
	issuer := m.devIdPIssuer(ctx)
	if issuer == "" {
		return nil
	}
	clientID, secret, err := m.ensureDevIdPClient(ctx, ws)
	if err != nil {
		log.Printf("dev idp: client for ws %s not injected: %v", ws.ID, err)
		return nil
	}
	return []string{
		"AF_DEV_IDP_ISSUER=" + issuer,
		"AF_DEV_IDP_WELL_KNOWN=" + issuer + "/.well-known/openid-configuration",
		"AF_DEV_IDP_CLIENT_ID=" + clientID,
		"AF_DEV_IDP_CLIENT_SECRET=" + secret,
	}
}

// devIdPProfile is the identity half of the claims, shaped like Google's. It is stored with the
// code and served again by userinfo, so what the ID token says and what userinfo says cannot
// drift apart.
type devIdPProfile struct {
	Sub           string `json:"sub"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Name          string `json:"name,omitempty"`
	Picture       string `json:"picture,omitempty"`
}

// devIdPClaims are the claims asserted for one member. With a linked Google identity the real
// `sub` and address come through, so the app's existing user rows match; otherwise the subject
// is a stable value derived from the person, which cannot equal a Google sub (they are numeric)
// or a future persona's.
func devIdPClaims(ctx context.Context, st store.IdentityStore, ident store.Identity) devIdPProfile {
	p := devIdPProfile{Email: ident.Email}
	if links, err := st.ListLinkedProviders(ctx, ident.ID); err == nil {
		for _, l := range links {
			if l.Realm == googleRealm && l.Subject != "" {
				p.Sub = l.Subject
				if l.Email != "" {
					p.Email = l.Email
				}
				break
			}
		}
	}
	if p.Sub == "" {
		sum := sha256.Sum256([]byte("af-dev-idp-sub\x00" + ident.ID))
		p.Sub = "af-" + hex.EncodeToString(sum[:16])
	}
	if p.Email == "" {
		p.Name = ident.UserKey
		return p
	}
	p.EmailVerified = true
	p.Name = p.Email[:strings.IndexByte(p.Email+"@", '@')]
	// No `hd`: it says the account belongs to a Google-hosted domain, which the address's
	// domain does not prove (a personal Google account can use any address), and the
	// IdentityLink does not keep the value Google verified. Omitted, like picture.
	return p
}

// devIdPScopedClaims keeps what the granted scopes entitle the app to (sub always).
func devIdPScopedClaims(all devIdPProfile, scope string) devIdPProfile {
	has := map[string]bool{}
	for _, s := range strings.Fields(scope) {
		has[s] = true
	}
	out := devIdPProfile{Sub: all.Sub}
	if has["email"] {
		out.Email, out.EmailVerified = all.Email, all.EmailVerified
	}
	if has["profile"] {
		out.Name, out.Picture = all.Name, all.Picture
	}
	return out
}

// devIdPRedirectAllowed is the ACL on redirect_uri: the one thing standing between the
// authorization code and an arbitrary URL. It admits exactly
//
//   - http://127.0.0.1:<port>/… and http://localhost:<port>/… (an app in the workspace, seen
//     from the browser pane or a forwarded port), and
//   - https://<slug>-<port>.<PreviewDomain>/… for THIS workspace's CURRENT slug and a listed
//     port, which is a host the member could open anyway,
//
// and only for a member who may open this workspace's preview at all (owner, or tenant share
// on). The match is on the canonical spelling: an upper-case host, a trailing dot, an explicit
// :443, userinfo or a fragment are refused rather than normalised, because every normalisation
// is a chance for this check and the browser to disagree.
func (m *manager) devIdPRedirectAllowed(ctx context.Context, ws store.Workspace, st wsSettings, membershipID, raw string) bool {
	u, ok := parseCanonicalRedirect(raw)
	if !ok {
		return false
	}
	if !previewViewerAllowed(ctx, m, ws, st, membershipID) {
		return false
	}
	switch u.Scheme {
	case "http":
		host, port := u.Hostname(), u.Port()
		if host != "127.0.0.1" && host != "localhost" {
			return false
		}
		n, err := strconv.Atoi(port)
		return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port && u.Host == host+":"+port
	case "https":
		ph, ok := parsePreviewHost(u.Host, m.previewDomain)
		if !ok || ws.PreviewSlug == "" || ph.slug != ws.PreviewSlug {
			return false
		}
		return u.Host == previewHostname(ph.slug, ph.port, m.previewDomain) && previewPortAllowed(st, ph.port)
	}
	return false
}

// parseCanonicalRedirect is the first cut every redirect_uri ACL makes (the dev IdP's and the
// /mcp authorization server's): backslash, fragment, userinfo, whitespace and opaque forms are
// refused outright. The callers then compare the host as written, never a normalised form.
func parseCanonicalRedirect(raw string) (*url.URL, bool) {
	if raw == "" || strings.ContainsAny(raw, "\\#\r\n\t ") {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.Fragment != "" {
		return nil, false
	}
	return u, true
}

func devIdPProfileJSON(p devIdPProfile) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func devIdPParseProfile(s string) devIdPProfile {
	var p devIdPProfile
	_ = json.Unmarshal([]byte(s), &p)
	return p
}

// devIdPRotated is the answer to a rotation. The secret is not in it, as in addDevIdP.
type devIdPRotated struct {
	ClientID        string `json:"devIdpClientId"`
	RestartRequired bool   `json:"restartRequired"`
}

// rotateDevIdPSecret (POST /api/env/ws-settings/dev-idp/rotate) issues the workspace a new
// client id and secret and invalidates the old ones at once. The container keeps the old values
// until its next start, which is why the Console says a restart is needed. It is audited before
// it is applied, and refused where the deployment does not offer the IdP.
func (a wsSettingsAPI) rotateDevIdPSecret(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	if !a.mgr.devIdPOfferedToMembers() {
		writeAPIErr(w, &apiError{http.StatusConflict, "no_preview_domain", "the development OIDC IdP needs preview subdomains, and this deployment has no preview domain"})
		return
	}
	if a.mgr.devIdPIssuer(ctx) == "" {
		writeAPIErr(w, &apiError{http.StatusConflict, "not_configured", "the development OIDC IdP is not enabled on this deployment"})
		return
	}
	if err := a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.dev_idp_rotate", Target: res.ws.ID, At: store.NowTS(),
	}); err != nil {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, errCodeAuditUnavailable, "nothing was changed: " + err.Error()})
		return
	}
	secret := "afdevsec-" + devIdPRandom(32)
	enc, ref, err := a.mgr.sealTenantSecret(ctx, res.ws.TenantID, secret)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	fresh := store.DevIdPClient{
		WorkspaceID: res.ws.ID, MembershipID: res.ws.MembershipID, ClientID: "afdev-" + strings.ToLower(devIdPRandom(12)),
		SecretEnc: enc, KeyRef: ref, CreatedAt: time.Now().Unix(),
	}
	if err := a.mgr.store.ReplaceDevIdPClient(ctx, fresh); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The cached runtime holds the env of the old registration.
	a.mgr.evictMembershipCache(res.ws.MembershipID)
	writeJSON(w, http.StatusOK, devIdPRotated{ClientID: fresh.ClientID, RestartRequired: true})
}

// addDevIdP appends the dev IdP block to the ws-settings answer. The secret is not in it: the
// container receives it as AF_DEV_IDP_CLIENT_SECRET, and a screen that displayed it would
// put it in one more place for no gain.
func (a wsSettingsAPI) addDevIdP(r *http.Request, res *resolved, out map[string]any) {
	issuer := a.mgr.devIdPIssuer(r.Context())
	out["devIdpAvailable"] = issuer != ""
	if issuer == "" {
		return
	}
	out["devIdpIssuer"] = issuer
	if c, ok, err := a.mgr.store.GetDevIdPClient(r.Context(), res.ws.ID); err == nil && ok {
		out["devIdpClientId"] = c.ClientID
	}
}

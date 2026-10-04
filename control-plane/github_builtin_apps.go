// github_builtin_apps.go — which GitHub app a tenant's "connect with OAuth" button
// talks to (issue #1667, ADR 0052 amendment).
//
// A tenant picks one of four sources: none, the built-in OAuth App, the built-in GitHub
// App, or its own client_id. The built-in apps exist so that a personal install has a
// working button without anyone registering an app first. They are safe to compile in
// because the device flow authenticates with the client_id alone: there is no secret to
// leak, and the token goes to the member's own workspace, never to the app's owner.
//
// The choice is a tenant row and is shown on the tenant settings screen, including the
// default a tenant without a row gets — so "which app am I sent to" is still answered in
// one place (ADR 0052 decision 2), it just has more than one possible answer.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The built-in apps' public identifiers. A build that leaves them empty (a fork, a dev
// build) offers no built-in source at all. They can be overridden at link time with
// -ldflags "-X main.builtinGitHubOAuthClientID=…" without editing this file.
var (
	// The project's OAuth App (Device flow on). Measured: a nonexistent scope answers
	// invalid_scope, so the probe classifies it as an OAuth App. The allow marker is there
	// because gitleaks' generic-api-key rule reads "Auth" in the name as a credential
	// keyword; a client_id is public.
	builtinGitHubOAuthClientID = "Ov23liJpLp15wcnFMDnV" // gitleaks:allow
	// The project's GitHub App (Device flow on, installable by any account). Measured: a
	// nonexistent scope still yields a device code, so the probe classifies it as a GitHub App.
	builtinGitHubAppClientID = "Iv23liEMe78j1i77oAme"
	// builtinGitHubAppSlug is the GitHub App's URL name. GitHub exposes no way to learn it
	// from a client_id, and the install link is built from it.
	builtinGitHubAppSlug = "agent-fleet-git"
)

// GitHub row sources (tenant_git_oauth.source).
const (
	ghSourceNone         = "none"
	ghSourceBuiltinOAuth = "builtin_oauth"
	ghSourceBuiltinApp   = "builtin_app"
	ghSourceCustom       = "custom"
)

// What kind of app a client_id belongs to, and how that was learnt.
const (
	ghTypeOAuthApp  = "oauth_app"
	ghTypeGitHubApp = "github_app"

	ghTypeByProbe  = "probe"  // GitHub answered at save time
	ghTypeByToken  = "token"  // a token minted through the app carried its prefix
	ghTypeByPrefix = "prefix" // guessed from the client_id; shown as an estimate
)

// ghProbeScope is a scope GitHub does not have. Asking for it is how the probe tells the
// two kinds apart: an OAuth App validates scopes and refuses, a GitHub App ignores them.
const ghProbeScope = "af_app_type_probe"

// ghAPIBase is GitHub's REST API root (a variable so tests can point it at a stub).
var ghAPIBase = "https://api.github.com"

// githubBuiltinOffFromEnv reads AF_GITHUB_BUILTIN_APPS. Only an explicit "off" disables
// the built-in apps; an unrecognised value leaves them on rather than silently taking
// members' buttons away.
func githubBuiltinOffFromEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "0", "no", "disabled":
		return true
	}
	return false
}

// githubBuiltinAvailable reports whether this binary carries the given built-in app and
// the operator has not switched the built-in apps off.
func (m *manager) githubBuiltinAvailable(source string) bool {
	if m.githubBuiltinOff {
		return false
	}
	switch source {
	case ghSourceBuiltinOAuth:
		return builtinGitHubOAuthClientID != ""
	case ghSourceBuiltinApp:
		return builtinGitHubAppClientID != "" && builtinGitHubAppSlug != ""
	}
	return false
}

// githubApp is the app a tenant's GitHub button resolves to.
type githubApp struct {
	Source string
	// Default is true when the tenant has no row and Source is the deployment default.
	Default    bool
	ClientID   string
	Type       string // ghTypeOAuthApp / ghTypeGitHubApp / "" when unknown
	TypeBy     string
	InstallURL string // GitHub App only: where a member installs it
}

// githubOAuthApp resolves a tenant's GitHub app. ok is false when there is nothing to
// connect through — no row and no default, "none", a custom row with no client_id, or a
// built-in source this binary or the operator does not offer — and app still carries the
// Source so the settings screen can say which of those it is.
//
// The default for a tenant with no row is the built-in OAuth App, for the deployment's
// default tenant only: that tenant is the whole deployment on native / compose, where the
// person installing is the person connecting. Any other tenant was created by somebody
// who can choose for it.
func (m *manager) githubOAuthApp(ctx context.Context, tenantID string) (githubApp, bool, error) {
	if tenantID == "" {
		return githubApp{}, false, nil
	}
	row, found, err := m.store.GetTenantGitOAuth(ctx, tenantID, gitOAuthGitHub)
	if err != nil {
		return githubApp{}, false, err
	}
	if !found {
		if tenantID != m.defaultTenantID {
			return githubApp{Source: ghSourceNone, Default: true}, false, nil
		}
		app := builtinGitHubApp(ghSourceBuiltinOAuth)
		app.Default = true
		return app, m.githubBuiltinAvailable(ghSourceBuiltinOAuth), nil
	}
	switch row.Source {
	case ghSourceNone:
		return githubApp{Source: ghSourceNone}, false, nil
	case ghSourceBuiltinOAuth, ghSourceBuiltinApp:
		return builtinGitHubApp(row.Source), m.githubBuiltinAvailable(row.Source), nil
	}
	// custom, and "" for a row written before the column existed.
	app := githubApp{
		Source: ghSourceCustom, ClientID: strings.TrimSpace(row.ClientID),
		Type: row.AppType, TypeBy: row.AppTypeBy, InstallURL: githubInstallURL(row.InstallURL),
	}
	return app, app.ClientID != "", nil
}

func builtinGitHubApp(source string) githubApp {
	if source == ghSourceBuiltinApp {
		return githubApp{
			Source: source, ClientID: builtinGitHubAppClientID, Type: ghTypeGitHubApp,
			InstallURL: githubInstallURL("https://github.com/apps/" + builtinGitHubAppSlug),
		}
	}
	return githubApp{Source: source, ClientID: builtinGitHubOAuthClientID, Type: ghTypeOAuthApp}
}

// githubAppPageRe is a GitHub App's public page, with or without the install suffix.
var githubAppPageRe = regexp.MustCompile(`^https://github\.com/apps/([A-Za-z0-9][A-Za-z0-9-]*)(/installations/new)?/?$`)

// normalizeGitHubAppURL accepts what an administrator copies from GitHub — the app's
// public page or its install page — and returns the app page, or ok=false for anything
// else. Only github.com/apps/<slug> is accepted because the Console renders it as a link
// members click.
func normalizeGitHubAppURL(raw string) (string, bool) {
	m := githubAppPageRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", false
	}
	return "https://github.com/apps/" + m[1], true
}

// githubInstallURL turns a stored app page into the page that installs it.
func githubInstallURL(appPage string) string {
	page, ok := normalizeGitHubAppURL(appPage)
	if !ok {
		return ""
	}
	return page + "/installations/new"
}

// ghAppTypeFromToken reads the documented token prefixes: gho_ is an OAuth App's access
// token, ghu_ a GitHub App's user-to-server token.
func ghAppTypeFromToken(token string) string {
	switch {
	case strings.HasPrefix(token, "gho_"):
		return ghTypeOAuthApp
	case strings.HasPrefix(token, "ghu_"):
		return ghTypeGitHubApp
	}
	return ""
}

var legacyOAuthClientIDRe = regexp.MustCompile(`^[0-9a-f]{20}$`)

// ghAppTypeFromClientID guesses from the client_id's shape. GitHub does not document
// these prefixes, so this is used only when the probe could not reach GitHub, and the
// result is marked as an estimate.
func ghAppTypeFromClientID(id string) string {
	switch {
	case strings.HasPrefix(id, "Iv1.") || strings.HasPrefix(id, "Iv2"):
		return ghTypeGitHubApp
	case strings.HasPrefix(id, "Ov2") || legacyOAuthClientIDRe.MatchString(id):
		return ghTypeOAuthApp
	}
	return ""
}

// ghProbeError is a probe answer the save has to refuse: the client_id is wrong, or the
// app cannot run the device flow at all.
type ghProbeError struct{ code, msg string }

// ghProbeAppType asks GitHub what kind of app clientID is, by starting a device flow
// with a scope that does not exist. An OAuth App answers invalid_scope without creating a
// device code; a GitHub App ignores scopes and hands one out, which then expires unused.
// A variable so tests do not reach github.com.
var ghProbeAppType = func(clientID string) (appType, by string, perr *ghProbeError) {
	var resp struct {
		DeviceCode string `json:"device_code"`
		Error      string `json:"error"`
	}
	err := ghDevicePostForm(ghDeviceCodeURL, url.Values{"client_id": {clientID}, "scope": {ghProbeScope}}, &resp)
	switch {
	case err != nil:
		// GitHub unreachable or an answer that is not JSON: an outbound-restricted CP must
		// still be able to save, so fall back to the guess.
		if t := ghAppTypeFromClientID(clientID); t != "" {
			return t, ghTypeByPrefix, nil
		}
		return "", "", nil
	case resp.Error == "invalid_scope":
		return ghTypeOAuthApp, ghTypeByProbe, nil
	case resp.DeviceCode != "":
		return ghTypeGitHubApp, ghTypeByProbe, nil
	case resp.Error == "Not Found" || resp.Error == "incorrect_client_credentials":
		return "", "", &ghProbeError{"github_client_not_found", "GitHub does not know this client_id"}
	case resp.Error == "device_flow_disabled":
		return "", "", &ghProbeError{"device_flow_disabled",
			"this app has Device flow disabled — enable it in the app's settings on GitHub"}
	}
	if t := ghAppTypeFromClientID(clientID); t != "" {
		return t, ghTypeByPrefix, nil
	}
	return "", "", nil
}

// ghHasInstallation reports whether a GitHub App user token can see at least one
// installation of its app. A GitHub App token reaches only repositories the app is
// installed on, so with none the connection looks fine and every clone fails.
// known=false means GitHub could not be asked; the caller then says nothing.
var ghHasInstallation = func(ctx context.Context, token string) (installed, known bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ghAPIBase+"/user/installations?per_page=1", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := ghDeviceHTTPClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var out struct {
		TotalCount int `json:"total_count"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return false, false
	}
	return out.TotalCount > 0, true
}

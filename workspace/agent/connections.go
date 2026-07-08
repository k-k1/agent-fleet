package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Connections hold the per-user provider credentials the Workspace consumes:
// git provider tokens (HTTPS) and the Claude OAuth token. They live in the
// user's home — the container's isolation boundary — inside the encrypted store
// (internal/secrets). The Control Plane delegates here and never holds secrets itself.
// See plan / docs/06 §6.7-6.8, docs/07 §7.3.

// gitHosts maps a supported provider host to its default git username. GitHub
// accepts any non-empty username with a token, so we use the conventional
// "x-access-token"; Bitbucket requires the user's Atlassian email, so there is
// no default and the caller must supply one.
var gitHosts = map[string]string{
	"github.com":    "x-access-token",
	"bitbucket.org": "",
}

// handleConnectionsGet reports connection status per provider, never secrets.
func handleConnectionsGet(w http.ResponseWriter, r *http.Request) {
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"claude":    claudeStatus(),
		"github":    gitConnStatus(s, "github.com"),
		"bitbucket": bitbucketStatus(s),
		"internal":  internalGitStatus(s),
		"opencode":  opencode.Status(s),
		"codex":     codexStatus(),
	})
}

// internalGitStatus reports the tenant's self-hosted git provider (docs/reference/
// internal-git-provider). It is CP-managed (the token is injected, not user-set),
// so it reports connected whenever the CP seeded a credential for the internal
// host. Absent AF_INTERNAL_GIT_HOST (internal git disabled) it is not connected.
func internalGitStatus(s *secrets.Data) map[string]any {
	host := internalGitHost()
	if host == "" {
		return map[string]any{"connected": false}
	}
	e, ok := s.Git[host]
	if !ok {
		return map[string]any{"connected": false, "host": host}
	}
	return map[string]any{"connected": true, "host": host, "username": firstNonEmpty(e.User, "x-access-token")}
}

// bitbucketStatus reports connected for either path: a pasted token (Git entry)
// or stored OAuth refresh creds (used via the cred helper). It surfaces the real
// Bitbucket account, resolved once from the API and cached in the store (so the
// polled endpoint doesn't re-fetch); on resolve failure it falls back to the
// stored email (token paste) or a placeholder (OAuth).
func bitbucketStatus(s *secrets.Data) map[string]any {
	if e, ok := s.Git["bitbucket.org"]; ok {
		m := map[string]any{"connected": true}
		if (e.Login == "" || e.Email == "") && e.Token != "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					e.Login, e.Email = h, email
					s.Git["bitbucket.org"] = e
					_ = s.Save()
				}
			}
		}
		if name := firstNonEmpty(e.Login, e.User); name != "" {
			m["username"] = name
		}
		if e.Email != "" {
			m["email"] = e.Email
		}
		id := s.GitIdentity["bitbucket.org"]
		m["commitName"] = firstNonEmpty(id.Name, e.Login)
		m["commitEmail"] = firstNonEmpty(id.Email, e.Email)
		return m
	}
	if s.Bitbucket != nil {
		m := map[string]any{"connected": true}
		if s.Bitbucket.Account == "" || s.Bitbucket.Email == "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					s.Bitbucket.Account, s.Bitbucket.Email = h, email
					_ = s.Save()
				}
			}
		}
		m["username"] = firstNonEmpty(s.Bitbucket.Account, "x-token-auth (oauth)")
		if s.Bitbucket.Email != "" {
			m["email"] = s.Bitbucket.Email
		}
		id := s.GitIdentity["bitbucket.org"]
		m["commitName"] = firstNonEmpty(id.Name, s.Bitbucket.Account)
		m["commitEmail"] = firstNonEmpty(id.Email, s.Bitbucket.Email)
		return m
	}
	return map[string]any{"connected": false}
}

// gitConnStatus reports a git provider's connection + the real account (handle +
// email), resolved once from the provider API and cached in the store
// (write-through), so the polled endpoint doesn't re-fetch. Falls back to the
// git-username placeholder; email is omitted when unavailable.
func gitConnStatus(s *secrets.Data, host string) map[string]any {
	e, ok := s.Git[host]
	m := map[string]any{"connected": ok}
	if !ok {
		return m
	}
	if host == "github.com" && (e.Login == "" || e.Email == "") && e.Token != "" {
		if login, email, err := githubAccount(e.Token); err == nil && login != "" {
			e.Login, e.Email = login, email
			s.Git[host] = e
			_ = s.Save()
		}
	}
	if name := firstNonEmpty(e.Login, e.User); name != "" {
		m["username"] = name
	}
	if e.Email != "" {
		m["email"] = e.Email
	}
	id := s.GitIdentity[host]
	m["commitName"] = firstNonEmpty(id.Name, e.Login)
	m["commitEmail"] = firstNonEmpty(id.Email, e.Email)
	return m
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type gitConnReq struct {
	Username string `json:"username"`
	Token    string `json:"token"`
	Name     string `json:"name"`  // optional git author name (for commits)
	Email    string `json:"email"` // optional git author email
}

// handlePutGitConn stores an HTTPS credential for a provider so git's `store`
// helper authenticates clone/fetch/push transparently. The token→git binding
// mirrors CodeLeaf (GitHub user "x-access-token"; Bitbucket user = email).
func handlePutGitConn(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	defUser, ok := gitHosts[host]
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	var req gitConnReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_token", "token is required")
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		user = defUser
	}
	if user == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_username", "username (Atlassian email) is required for "+host)
		return
	}

	if err := upsertGitCredential(host, user, token); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	// Commit identity is set separately (per provider) via /identity — not clobbered
	// into the global config at connect time.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": true, "host": host, "username": user})
}

func handleDeleteGitConn(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if _, ok := gitHosts[host]; !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	if err := removeGitCredential(host); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if host == "bitbucket.org" {
		removeBitbucketOAuth() // also clear OAuth tokens + the refresh helper
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"disconnected": host})
}

// upsertGitCredential stores an HTTPS credential for host in the encrypted store
// and ensures the cred helper is the active git credential source.
func upsertGitCredential(host, user, token string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	s.Git[host] = secrets.GitEntry{User: user, Token: token}
	if err := s.Save(); err != nil {
		return err
	}
	return ensureCredHelper()
}

func removeGitCredential(host string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	delete(s.Git, host)
	return s.Save()
}

func gitConfigGlobal(key, val string) error {
	if out, err := gitx.Combined("", "config", "--global", key, val); err != nil {
		return fmt.Errorf("git config %s: %v: %s", key, err, out)
	}
	return nil
}

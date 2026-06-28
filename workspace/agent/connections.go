package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// Connections hold the per-user provider credentials the Workspace consumes:
// git provider tokens (HTTPS) and the Claude OAuth token. They live in the
// user's home — the container's isolation boundary — inside the encrypted store
// (secrets.go). The Control Plane delegates here and never holds secrets itself.
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
	s, err := loadSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"claude":    claudeStatus(),
		"github":    gitConnStatus(s, "github.com"),
		"bitbucket": bitbucketStatus(s),
		"opencode":  opencodeStatus(s),
		"codex":     codexStatus(),
	})
}

// bitbucketStatus reports connected for either path: a pasted token (Git entry)
// or stored OAuth refresh creds (used via the cred helper). It surfaces the real
// Bitbucket account, resolved once from the API and cached in the store (so the
// polled endpoint doesn't re-fetch); on resolve failure it falls back to the
// stored email (token paste) or a placeholder (OAuth).
func bitbucketStatus(s *secretsData) map[string]any {
	if e, ok := s.Git["bitbucket.org"]; ok {
		m := map[string]any{"connected": true}
		if (e.Login == "" || e.Email == "") && e.Token != "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					e.Login, e.Email = h, email
					s.Git["bitbucket.org"] = e
					_ = s.save()
				}
			}
		}
		if name := firstNonEmpty(e.Login, e.User); name != "" {
			m["username"] = name
		}
		if e.Email != "" {
			m["email"] = e.Email
		}
		return m
	}
	if s.Bitbucket != nil {
		m := map[string]any{"connected": true}
		if s.Bitbucket.Account == "" || s.Bitbucket.Email == "" {
			if auth, err := bitbucketAuthHeader(s); err == nil {
				if h, email, err := bitbucketAccount(auth); err == nil && h != "" {
					s.Bitbucket.Account, s.Bitbucket.Email = h, email
					_ = s.save()
				}
			}
		}
		m["username"] = firstNonEmpty(s.Bitbucket.Account, "x-token-auth (oauth)")
		if s.Bitbucket.Email != "" {
			m["email"] = s.Bitbucket.Email
		}
		return m
	}
	return map[string]any{"connected": false}
}

// gitConnStatus reports a git provider's connection + the real account (handle +
// email), resolved once from the provider API and cached in the store
// (write-through), so the polled endpoint doesn't re-fetch. Falls back to the
// git-username placeholder; email is omitted when unavailable.
func gitConnStatus(s *secretsData, host string) map[string]any {
	e, ok := s.Git[host]
	m := map[string]any{"connected": ok}
	if !ok {
		return m
	}
	if host == "github.com" && (e.Login == "" || e.Email == "") && e.Token != "" {
		if login, email, err := githubAccount(e.Token); err == nil && login != "" {
			e.Login, e.Email = login, email
			s.Git[host] = e
			_ = s.save()
		}
	}
	if name := firstNonEmpty(e.Login, e.User); name != "" {
		m["username"] = name
	}
	if e.Email != "" {
		m["email"] = e.Email
	}
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
		writeErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	var req gitConnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeErr(w, http.StatusBadRequest, "bad_token", "token is required")
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		user = defUser
	}
	if user == "" {
		writeErr(w, http.StatusBadRequest, "bad_username", "username (Atlassian email) is required for "+host)
		return
	}

	if err := upsertGitCredential(host, user, token); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	// Optional commit identity, so anything claude commits is attributed sanely.
	if n := strings.TrimSpace(req.Name); n != "" {
		_ = gitConfigGlobal("user.name", n)
	}
	if e := strings.TrimSpace(req.Email); e != "" {
		_ = gitConfigGlobal("user.email", e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "host": host, "username": user})
}

func handleDeleteGitConn(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if _, ok := gitHosts[host]; !ok {
		writeErr(w, http.StatusBadRequest, "bad_host", "unsupported host: "+host)
		return
	}
	if err := removeGitCredential(host); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	if host == "bitbucket.org" {
		removeBitbucketOAuth() // also clear OAuth tokens + the refresh helper
	}
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": host})
}

// upsertGitCredential stores an HTTPS credential for host in the encrypted store
// and ensures the cred helper is the active git credential source.
func upsertGitCredential(host, user, token string) error {
	s, err := loadSecrets()
	if err != nil {
		return err
	}
	s.Git[host] = gitEntry{User: user, Token: token}
	if err := s.save(); err != nil {
		return err
	}
	return ensureCredHelper()
}

func removeGitCredential(host string) error {
	s, err := loadSecrets()
	if err != nil {
		return err
	}
	delete(s.Git, host)
	return s.save()
}

func gitConfigGlobal(key, val string) error {
	if out, err := exec.Command("git", "config", "--global", key, val).CombinedOutput(); err != nil {
		return fmt.Errorf("git config %s: %v: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

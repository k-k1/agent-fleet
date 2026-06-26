package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Connections hold the per-user provider credentials the Workspace consumes:
// git provider tokens (HTTPS) and the Claude OAuth token. They live in the
// user's home — the container's isolation boundary — at the same trust level as
// ~/.claude/.credentials.json. The Control Plane delegates here and never holds
// secrets itself. dev = single user; the store is simply the home. See plan /
// docs/06 §6.7-6.8, docs/07 §7.3.

func gitCredentialsPath() string { return filepath.Join(homeDir(), ".git-credentials") }

func claudeTokenPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "claude-oauth-token")
}

// gitHosts maps a supported provider host to its default git username. GitHub
// accepts any non-empty username with a token, so we use the conventional
// "x-access-token"; Bitbucket requires the user's Atlassian email, so there is
// no default and the caller must supply one.
var gitHosts = map[string]string{
	"github.com":    "x-access-token",
	"bitbucket.org": "",
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// handleConnectionsGet reports connection status per provider, never secrets.
func handleConnectionsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"claude":    map[string]any{"connected": fileExists(claudeTokenPath())},
		"github":    gitConnStatus("github.com"),
		"bitbucket": gitConnStatus("bitbucket.org"),
	})
}

func gitConnStatus(host string) map[string]any {
	user, ok := findGitCredential(host)
	m := map[string]any{"connected": ok}
	if ok && user != "" {
		m["username"] = user
	}
	return m
}

// findGitCredential returns the stored username for host (token withheld).
func findGitCredential(host string) (string, bool) {
	data, err := os.ReadFile(gitCredentialsPath())
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if u, err := url.Parse(line); err == nil && u.Host == host {
			return u.User.Username(), true
		}
	}
	return "", false
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
	if err := gitConfigGlobal("credential.helper", "store"); err != nil {
		writeErr(w, http.StatusInternalServerError, "config_failed", err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": host})
}

// upsertGitCredential rewrites ~/.git-credentials, replacing any line for host
// with a fresh `https://user:token@host` entry. url.UserPassword encodes the
// user/token so special characters survive.
func upsertGitCredential(host, user, token string) error {
	kept := keepCredentialsExcept(host)
	cred := url.URL{Scheme: "https", User: url.UserPassword(user, token), Host: host}
	kept = append(kept, cred.String())
	return writeCredentials(kept)
}

func removeGitCredential(host string) error {
	return writeCredentials(keepCredentialsExcept(host))
}

// keepCredentialsExcept returns existing credential lines minus those for host.
func keepCredentialsExcept(host string) []string {
	var kept []string
	data, err := os.ReadFile(gitCredentialsPath())
	if err != nil {
		return kept
	}
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if u, err := url.Parse(l); err == nil && u.Host == host {
			continue // drop the stale entry for this host
		}
		kept = append(kept, l)
	}
	return kept
}

func writeCredentials(lines []string) error {
	if len(lines) == 0 {
		// nothing left: remove the file rather than leave it empty
		if err := os.Remove(gitCredentialsPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(gitCredentialsPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func gitConfigGlobal(key, val string) error {
	if out, err := exec.Command("git", "config", "--global", key, val).CombinedOutput(); err != nil {
		return fmt.Errorf("git config %s: %v: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

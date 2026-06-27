// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	// Subcommand mode: git invokes this binary as its credential helper
	// (`workspace-agent cred get`), backed by the encrypted store. It prints
	// creds and exits without starting the server. `bitbucket-cred` is kept as
	// an alias for any git config left over from before the unified helper.
	if len(os.Args) > 1 && (os.Args[1] == "cred" || os.Args[1] == "bitbucket-cred") {
		runCredHelper(os.Args[2:])
		return
	}

	// Fold any pre-A3 plaintext credential files into the encrypted store.
	migrateLegacySecrets()

	addr := envOr("AGENT_ADDR", ":7700")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /sessions", handleListSessions)
	mux.HandleFunc("POST /sessions", handleCreateSession)
	mux.HandleFunc("POST /sessions/{name}/stop", handleStopSession)
	mux.HandleFunc("GET /ws/pty", handlePTY)

	// Repository management — git ops on working copies under ~/repos.
	mux.HandleFunc("GET /repos", handleListRepos)
	mux.HandleFunc("POST /repos", handleCloneRepo)
	mux.HandleFunc("DELETE /repos/{name}", handleDeleteRepo)
	mux.HandleFunc("GET /repos/{name}/status", handleRepoStatus)
	mux.HandleFunc("GET /repos/{name}/branches", handleRepoBranches)
	mux.HandleFunc("POST /repos/{name}/checkout", handleRepoCheckout)
	mux.HandleFunc("POST /repos/{name}/fetch", handleRepoFetch)
	// Source-control view + light edits (docs/17 P3-5).
	mux.HandleFunc("GET /repos/{name}/changes", handleRepoChanges)
	mux.HandleFunc("GET /repos/{name}/diff", handleRepoDiff)
	mux.HandleFunc("GET /repos/{name}/log", handleRepoLog)
	mux.HandleFunc("POST /repos/{name}/stage", handleRepoStage)
	mux.HandleFunc("POST /repos/{name}/unstage", handleRepoUnstage)
	mux.HandleFunc("POST /repos/{name}/discard", handleRepoDiscard)
	mux.HandleFunc("POST /repos/{name}/commit", handleRepoCommit)
	// Read-only file browser (docs/17 P3-5 段2).
	mux.HandleFunc("GET /fs/tree", handleFSTree)
	mux.HandleFunc("GET /fs/file", handleFSFile)

	// Claude settings (Remote Control / notifications / RTK hook) — Console toggles.
	mux.HandleFunc("GET /claude/settings", handleClaudeSettingsGet)
	mux.HandleFunc("PUT /claude/settings", handleClaudeSettingsPut)

	// Toolchain selection (node via nvm / java via pre-baked Temurin) — Console.
	mux.HandleFunc("GET /env/toolchains", handleToolchainsGet)
	mux.HandleFunc("PUT /env/toolchains", handleToolchainsPut)

	// Connections — per-user provider credentials (git tokens; Claude in Stage 3).
	mux.HandleFunc("GET /connections", handleConnectionsGet)
	mux.HandleFunc("GET /connections/git/{host}/repos", handleListRemoteRepos)
	mux.HandleFunc("GET /connections/git/{host}/branches", handleListRemoteBranches)
	mux.HandleFunc("PUT /connections/git/{host}", handlePutGitConn)
	mux.HandleFunc("DELETE /connections/git/{host}", handleDeleteGitConn)
	mux.HandleFunc("POST /connections/git/github/oauth/start", handleGithubOAuthStart)
	mux.HandleFunc("POST /connections/git/github/oauth/poll", handleGithubOAuthPoll)
	mux.HandleFunc("PUT /connections/git/bitbucket/oauth", handleBitbucketStore)
	mux.HandleFunc("POST /connections/claude/start", handleClaudeStart)
	mux.HandleFunc("POST /connections/claude/complete", handleClaudeComplete)
	mux.HandleFunc("DELETE /connections/claude", handleClaudeDisconnect)

	log.Printf("workspace-agent listening on %s", addr)
	if err := http.ListenAndServe(addr, logRequests(requireToken(mux))); err != nil {
		log.Fatal(err)
	}
}

// requireToken enforces the CP↔Agent shared token (docs/07 §7.5): the Control
// Plane injects AGENT_TOKEN at container start and presents it as a Bearer on
// every request. /healthz stays open (used for the startup readiness probe).
// If AGENT_TOKEN is unset the gate is disabled — a safety valve for manual
// debugging; the CP always injects one in normal operation.
func requireToken(next http.Handler) http.Handler {
	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		log.Printf("WARNING: AGENT_TOKEN unset — CP↔Agent auth disabled (dev only)")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid agent token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- small helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}

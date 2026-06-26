// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	// Subcommand mode: git invokes this binary as a credential helper for
	// bitbucket.org (`workspace-agent bitbucket-cred get`). It prints creds and
	// exits without starting the server.
	if len(os.Args) > 1 && os.Args[1] == "bitbucket-cred" {
		runBitbucketCredHelper(os.Args[2:])
		return
	}

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

	// Connections — per-user provider credentials (git tokens; Claude in Stage 3).
	mux.HandleFunc("GET /connections", handleConnectionsGet)
	mux.HandleFunc("PUT /connections/git/{host}", handlePutGitConn)
	mux.HandleFunc("DELETE /connections/git/{host}", handleDeleteGitConn)
	mux.HandleFunc("POST /connections/git/github/oauth/start", handleGithubOAuthStart)
	mux.HandleFunc("POST /connections/git/github/oauth/poll", handleGithubOAuthPoll)
	mux.HandleFunc("PUT /connections/git/bitbucket/oauth", handleBitbucketStore)
	mux.HandleFunc("POST /connections/claude/start", handleClaudeStart)
	mux.HandleFunc("POST /connections/claude/complete", handleClaudeComplete)
	mux.HandleFunc("DELETE /connections/claude", handleClaudeDisconnect)

	log.Printf("workspace-agent listening on %s", addr)
	if err := http.ListenAndServe(addr, logRequests(mux)); err != nil {
		log.Fatal(err)
	}
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

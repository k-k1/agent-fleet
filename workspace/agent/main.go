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
	// claude hook helper: records session working/idle/question state.
	if len(os.Args) > 1 && os.Args[1] == "session-status" {
		runSessionStatusHook(os.Args[2:])
		return
	}
	// Local stdio MCP server for the assistant chat (docs/19 Q1): read-only Agent
	// Fleet tools over JSON-RPC on stdio, spawned by claude's --mcp-config.
	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		runMCPStdio(os.Args[2:])
		return
	}

	// Fold any pre-A3 plaintext credential files into the encrypted store.
	migrateLegacySecrets()
	// Seed the CP-injected internal git token (docs/reference/internal-git-provider)
	// into the cred store so clone/push against the tenant's self-hosted repos auth
	// transparently. No-op when the CP didn't inject one.
	seedInternalGit()
	// Make claude emit working/idle/question via hooks into the status files.
	ensureStatusHooks()
	// Apply the durable codex/opencode rtk prefs to their artifacts (the entrypoint
	// reseeded the base AGENTS.md / status plugin just before us). claude's rtk is
	// handled separately via its settings.json hook.
	reconcileAgentRTK()

	addr := envOr("AGENT_ADDR", ":7700")

	mux := buildMux()

	// Translate the runtime's stop signal (SIGTERM from docker stop / ECS task
	// stop) into a graceful in-container shutdown before the SIGKILL deadline.
	watchShutdownSignals()

	// Keep origin refs fresh in the background so repo rows can badge
	// "origin advanced" without a manual fetch (fetch_loop.go).
	startAutoFetch()

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

// decodeJSON parses the request body into v; on failure it writes the shared
// 400 bad_request response and returns false — the caller just returns.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}

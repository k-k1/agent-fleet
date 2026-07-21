// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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
	// JDK provisioner: `workspace-agent install-jdk <major>` downloads the latest GA
	// Temurin for the container arch into the per-user home volume (temurin-<major>-
	// jdk-<arch>), the common JDK location the toolchain resolver + entrypoint search
	// alongside /usr/lib/jvm. Run by the entrypoint on demand (selected java missing)
	// and available to the agent directly. See jdk.go.
	if len(os.Args) > 1 && os.Args[1] == "install-jdk" {
		runInstallJDK(os.Args[2:])
		return
	}
	// claude hook helper: records session working/idle/question state.
	if len(os.Args) > 1 && os.Args[1] == "session-status" {
		runSessionStatusHook(os.Args[2:])
		return
	}
	// Pane exit recorder: `workspace-agent record-exit <name> <code>`, appended after
	// the agent CLI by startSessionTmux, records why a session terminated (crash / OOM).
	if len(os.Args) > 1 && os.Args[1] == "record-exit" {
		runRecordExit(os.Args[2:])
		return
	}
	// Bounded terminal-output sink, fed by tmux pipe-pane.
	if len(os.Args) > 1 && os.Args[1] == "record-terminal" {
		runRecordTerminal(os.Args[2:])
		return
	}
	// Local stdio MCP server for the assistant chat (docs/19 Q1): read-only Agent
	// Fleet tools over JSON-RPC on stdio, spawned by claude's --mcp-config.
	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		runMCPStdio(os.Args[2:])
		return
	}
	// Credential-injecting launcher for external ops MCP servers (docs/25): loads
	// the encrypted store, injects the provider key as env, and execs the real MCP
	// server (e.g. uvx pagerduty-mcp). Keeps API keys out of any MCP config file.
	if len(os.Args) > 1 && os.Args[1] == "mcp-run" {
		runMCPRun(os.Args[2:])
		return
	}
	// claude statusLine capture: claude pipes the session JSON (incl. rate_limits) on
	// stdin every render; we persist the 5h/weekly usage locally for the WsBar chip —
	// no network, so the rate-limited /api/oauth/usage endpoint is no longer used.
	if len(os.Args) > 1 && os.Args[1] == "statusline" {
		claude.RunStatusLine(os.Args[2:])
		return
	}
	// Image-only browser verification: exercise the production BrowserManager,
	// pipe CDP, sandbox, two simultaneous Pages and capture pacing without booting
	// the rest of the Agent subsystems. deploy/local/e2e-smoke.sh is the caller.
	if len(os.Args) > 1 && os.Args[1] == "browser-smoke" {
		if err := runBrowserImageSmoke(); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Fold any pre-A3 plaintext credential files into the encrypted store.
	migrateLegacySecrets()
	// Seed the CP-injected internal git token (docs/reference/internal-git-provider)
	// into the cred store so clone/push against the tenant's self-hosted repos auth
	// transparently. No-op when the CP didn't inject one.
	seedInternalGit()
	// Make claude emit working/idle/question via hooks into the status files.
	claude.EnsureStatusHooks()
	// Wire claude's statusLine to us so we capture its rate_limits (5h/weekly usage)
	// locally for the WsBar chip. Wraps a user's own statusLine rather than clobbering.
	claude.EnsureStatusLine()
	// Apply the durable codex/opencode rtk prefs to their artifacts (the entrypoint
	// reseeded the base AGENTS.md / status plugin just before us). claude's rtk is
	// handled separately via its settings.json hook.
	reconcileAgentRTK()
	startTerminalHistoryJanitor()
	// managed driver（hook を持たない）の turn 完了を、hook 経路と同じ通知/報告
	// （応答あり notice ＋ docs/30 のオペレーター報告）へ流す。driver は
	// internal/agents 配下で package main を import できないため、判定の 1 実装を
	// ここで seam に登録する。app-server 起動と reconcile より前に張ること。
	agents.SetStateNotifier(recordSessionNotification)
	// Codex sessions use a shared local app-server when available（P3 からは
	// codex.Serve() の RuntimeSupervisor が daemon を所有する）。AF attaches
	// a read-only observer per loaded thread: compaction state, rate limits, and
	// the model-switch observation log (docs/27 P1).
	startCodexAppServer()
	// managed セッション（docs/27 P2: opencode / P3: codex）を再接続する — Agent
	// 再起動を挟んでも tmux の tui セッションが生き残るのと同じ体感にする（§6 の
	// reconciliation）。runtime が必要なら Ensure が起こす。managed メタが無ければ
	// 即 no-op。
	go opencode.ReconcileManaged("agent boot")
	go codex.ReconcileManaged("agent boot")
	go copilot.ReconcileManaged("agent boot")

	addr := envOr("AGENT_ADDR", ":7700")

	mux := buildMux()

	// Translate the runtime's stop signal (SIGTERM from docker stop / ECS task
	// stop) into a graceful in-container shutdown before the SIGKILL deadline.
	watchShutdownSignals()

	// Keep origin refs fresh in the background so repo rows can badge
	// "origin advanced" without a manual fetch (fetch_loop.go).
	startAutoFetch()

	log.Printf("workspace-agent listening on %s", addr)
	if err := http.ListenAndServe(addr, httpx.LogRequests(httpx.RequireToken(mux))); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- small helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

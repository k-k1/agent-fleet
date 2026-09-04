// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"net/http"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// buildVersion is stamped by the release pipeline via
// `-ldflags "-X main.buildVersion=<v>"` (docs/log/35 §35.6.1); dev builds stay "dev".
var buildVersion = "dev"

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
	// Arch self-repair's Console face: turn what af-arch-repair could NOT put back into
	// a notification the member can actually see (the repair script only reaches the
	// container's stdout). See arch_residue.go for why it is keyed on content.
	if len(os.Args) > 1 && os.Args[1] == "notify-arch-residue" {
		runNotifyArchResidue(os.Args[2:])
		return
	}
	// On-demand pinned installers (docs/log/35 §35.7.2): chromium+CJK font for the
	// browser pane, node (docs/decisions/0068), the Go toolchain, and AWS CLI +
	// Session Manager plugin for ssm sessions. Lean rootfs deployments install these
	// into the home on first use; versions come from the versions.json pins (node
	// resolves the newest patch of the selected major). See install_tools.go.
	if len(os.Args) > 1 && os.Args[1] == "install-chromium" {
		runInstallChromium(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-node" {
		runInstallNode(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-go" {
		runInstallGo(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-awscli" {
		runInstallAWSCLI(os.Args[2:])
		return
	}
	// On-demand Kiro CLI installer (kind="kiro", docs/log/43 Track B): kiro is ~855MB
	// extracted, so unlike the other agent CLIs it is NOT baked/boot-installed for
	// everyone — it lands in the per-user home only when that user actually uses it
	// (the kiro launch program runs this with --if-needed on every launch, so a
	// versions.json pin bump also reaches the already-installed home copy; the
	// connection card install button does too). See install_kiro.go.
	if len(os.Args) > 1 && os.Args[1] == "install-kiro" {
		runInstallKiro(os.Args[2:])
		return
	}
	// claude hook helper: records session working/idle/question state.
	if len(os.Args) > 1 && os.Args[1] == "session-status" {
		sessionx.RunSessionStatusHook(os.Args[2:])
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
	// Local stdio MCP server: assistant chat tools (docs/log/19 Q1) or the narrowly scoped
	// interactive-session builtin (docs/log/51 Phase 3 + docs/log/53 §53.8), selected by args.
	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		mcpx.RunStdio(os.Args[2:])
		return
	}
	// Credential-injecting launcher for external ops MCP servers (docs/log/25): loads
	// the encrypted store, injects the provider key as env, and execs the real MCP
	// server (e.g. uvx pagerduty-mcp). Keeps API keys out of any MCP config file.
	if len(os.Args) > 1 && os.Args[1] == "mcp-run" {
		mcpx.RunSubcommand(os.Args[2:])
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
		if err := browserx.RunBrowserImageSmoke(); err != nil {
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
	// Record where the git OAuth refresh bridge lives (docs/log/71 §71.8) so the separate
	// `workspace-agent cred` process can reach it without depending on its own env.
	seedGitOAuthBridge()
	// Make claude emit working/idle/question via hooks into the status files.
	claude.EnsureStatusHooks()
	// Drop expired carried interactions (docs/log/75). This is the only place they are
	// ever deleted — nothing else sweeps a carried interaction whose session is gone, and
	// the lifetime-less pending-* files really did pile up five to six weeks deep.
	if n := status.SweepCarried(); n > 0 {
		log.Printf("carried-interaction: dropped %d expired entr(y|ies)", n)
	}
	// Wire claude's statusLine to us so we capture its rate_limits (5h/weekly usage)
	// locally for the WsBar chip. Wraps a user's own statusLine rather than clobbering.
	claude.EnsureStatusLine()
	// Compose the instruction files every session reads: the baked fleet guide, the
	// user's own instructions (docs/log/60) and the rtk block — in that order, through one
	// writer. This replaces the entrypoint's old `cp -f` of workspace-notes.md, which
	// destroyed anything the user had added to those files on every container start.
	// Sessions are started by us, so nothing can read a half-composed file.
	reconcileAgentInstructions()
	// Mint this boot's name for af's own MCP server, BEFORE anything materializes a
	// config. A repository's project-scoped MCP config beats af's user-scope one on
	// every kind but claude (docs/log/48 §8.4), so a repo that happens to define a server
	// called "af" would silently take over self-report, the handoff proposal and
	// Chromium attach; a random suffix makes that collision go away, and rotating it
	// per boot means even a deliberate one is shaken off by a restart.
	log.Printf("mcp: af server name for this boot = %s", mcpreg.RotateAFServerName())
	// Write the MCP registry into each CLI's own config (docs/log/48 P3) so the servers a
	// user registered are live from container start — including for a CLI they launch
	// by hand in a terminal, which never passes through the session launch hook.
	mcpx.MaterializeAll()
	// Pull the tenant-distributed MCP set from the CP and keep it fresh (docs/log/48 P4).
	// Backgrounded and fail-open: boot must not wait on the CP, and an unreachable CP
	// keeps the cached set rather than stripping everyone's servers.
	mcpx.StartTenantSync()
	// Pull the role-scoped docs when the runtime mounted none (ECS — docs/build/04 §4.9).
	// Backgrounded: it is a few hundred KB over the network and nothing at boot waits on
	// it, but the Console's user guide and every agent's environment answers need it.
	go syncWorkspaceDocs("agent boot")
	startTerminalHistoryJanitor()
	// Route a managed driver's turn completion (it has no hooks) into the same
	// notification/report path the hook route uses (the "answered" notice plus the
	// docs/log/30 operator report). Drivers live under internal/agents and cannot import
	// package main, so the single implementation of that decision is registered on the
	// seam here. Must be installed before the app-server start and the reconcilers below.
	agents.SetStateNotifier(sessionx.RecordSessionNotification)
	// The decision that an instruction's report has been consumed (docs/log/51 Phase 1 /
	// ADR 0035). The hooks, the notify seam and record-exit's kick are wake-up hints only;
	// whether an instruction is complete is decided by this reconciler's tick alone. A
	// dead hint costs nothing because the next tick reads the same state by level, so a
	// miss degrades into a late report rather than a lost one.
	// docs/log/51 Phase 2: convert instructions still waiting on the old 1-bit arm into
	// ledger rows first. Run before the reconciler — a tick before the conversion sees
	// "no unreported instructions".
	chatx.MigrateReportArms()
	chatx.StartReportReconciler()
	// Delivery ledger for browser attach handoffs (docs/log/53, completion-notice section):
	// pick up the ones where resolveBrowserHandoff finished last boot but
	// deliverBrowserHandoff did not. It has no busy/idle settle decision, so unlike the
	// reconciler above a single pass is enough.
	browserx.SweepUndeliveredBrowserHandoffs()
	// Repo import jobs (docs/log/78): a clone / checkout dies with the Agent (task
	// replacement, idle-stop). Unless a surviving marker is restored as "interrupted", a
	// half-made working copy comes back into the list looking like an ordinary repository.
	sweepRepoJobMarkers()
	// Codex sessions use a shared local app-server when available (from P3 on, the
	// RuntimeSupervisor in codex.Serve() owns the daemon). AF attaches a read-only
	// observer per loaded thread: compaction state, rate limits, and the model-switch
	// observation log (docs/log/27 P1).
	// This does NOT wake the daemon (docs/log/27 §7 addendum): demand — a managed Resume
	// or a TUI launch — wakes it, and it folds away once demand stays at zero. All that is
	// installed here is the seam and the observer.
	startCodexAppServer()
	// Reconnect managed sessions (docs/log/27 P2: opencode / P3: codex) so an Agent
	// restart feels like the tmux tui sessions surviving one (§6, reconciliation). Ensure
	// starts a runtime if one is needed; with no managed metadata this is an immediate
	// no-op.
	go opencode.ReconcileManaged("agent boot")
	go codex.ReconcileManaged("agent boot")
	go copilot.ReconcileManaged("agent boot")
	go cursor.ReconcileManaged("agent boot")
	go kiro.ReconcileManaged("agent boot")
	// Assistant-conversation slugs (docs/log/38 assistant triggering): stamp "a…" slugs onto
	// conversations created before the field existed, so schedules/operator tools can
	// address every conversation. One-time per store state; cheap when nothing to do.
	go chatx.BackfillConvSlugs()

	addr := envOr("AGENT_ADDR", ":7700")

	mux := buildMux()

	// Translate the runtime's stop signal (SIGTERM from docker stop / ECS task
	// stop) into a graceful in-container shutdown before the SIGKILL deadline.
	watchShutdownSignals()

	// Keep origin refs fresh in the background so repo rows can badge
	// "origin advanced" without a manual fetch (fetch_loop.go).
	startAutoFetch()

	// Automatic agent-memory snapshots (docs/log/39 / ADR 0022 P1): store claude's and
	// codex's memory markdown into a bare repo as diffs. Driven by polling
	// (memory_trigger.go), committing only when the changes have gone quiet and the
	// session in question is not running. AF_MEMORY_SNAPSHOT=off disables it.
	memoryx.StartMemorySnapshotLoop()

	// Automatic recovery of a claude session stopped by a rate limit (docs/log/47 §4-4):
	// dismiss the menu on its default ("wait for the reset") and hand the CP a one-shot
	// schedule that sends "continue" when the limit lifts. It has to work while nobody is
	// watching the screen, so it runs on its own loop rather than off the list polling.
	sessionx.StartRateLimitWatch()

	// Automatic resume from an abort that a retry fixes — a dropped connection, a
	// transient rate limit, the stream watchdog (docs/log/47 §4-6): the Agent itself sends
	// "continue" to a claude session whose transcript ends in a retryable abort. It works
	// for sessions with no assistant conversation too, and only reaches the assistant or
	// the user as a report when it gives up.
	sessionx.StartAbortResumeWatch()

	// Chat-bridge delivery loop (docs/log/37 P1): drains the on-disk queue that
	// notice.Put / record-exit enqueue into (possibly from hook subprocesses)
	// and pushes to the configured chat providers (Discord first).
	bridge.StartSender()

	// Chat-bridge receive (docs/log/37 P2a): the Discord Gateway supervisor that routes the
	// bound user's thread replies back into sessions. No-op until a user opts into receive
	// (Discord.Receive) — bounds the WSS connection to opted-in users only.
	sessionx.StartBridgeReceiver()

	log.Printf("workspace-agent %s listening on %s", buildVersion, addr)
	if err := http.ListenAndServe(addr, httpx.LogRequests(httpx.Gzip(httpx.RequireToken(mux)))); err != nil {
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

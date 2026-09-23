// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/afdb"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/statemig"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// buildVersion is stamped by the release pipeline via
// `-ldflags "-X main.buildVersion=<v>"` (docs/log/35 §35.6.1); dev builds stay "dev".
var buildVersion = "dev"

func main() {
	// Every argument this binary accepts is one row of the table in cli.go, and anything it
	// does not name is rejected there with usage and exit 2. Only "no arguments" (the
	// container's CMD, the native runtime's exec) and the explicit `serve` reach the boot
	// below — inspecting the binary must never start an Agent.
	if code, handled := dispatchCLI(os.Args[1:], os.Stdout, os.Stderr); handled {
		if code != 0 {
			os.Exit(code)
		}
		return
	}
	serve()
}

// runAFDB is the af-db subcommand, with the state migration in front of it. That order is
// the point: this is the one subcommand a USER runs from a terminal, with no Agent involved
// (ADR 0087 decision 4), and af-db reads a registry it cannot find as an EMPTY one and writes
// that back. Run before the Agent has migrated, it would leave a `{"instances":{}}` at the
// destination that "the destination is the truth" then keeps — discarding the real registry
// and orphaning a running postmaster. Cheap once done: the marker plus one failed stat per
// entry.
//
// The Result is dropped on purpose: anything skipped or failed leaves its entry unfinished,
// so the next Agent boot runs into it again and logs it there, where a reader is looking for
// boot diagnostics rather than a database command's output.
func runAFDB(args []string) {
	statemig.RunQuiet()
	afdb.RunAFDB(args)
}

func serve() {
	addr := envOr("AGENT_ADDR", ":7700")

	// Take the listening socket BEFORE any of the boot work below, and die if it is busy.
	// Every step from here to Serve mutates container-wide state — the credential store, the
	// instruction files, and above all af's MCP server name, whose rotation rewrites every
	// CLI's config. A second Agent in this container used to do all of that on its way to
	// failing on bind, because the listen was last. Losing the race has to cost nothing.
	// (Nothing is served until http.Serve runs, so a health probe in this window queues
	// rather than being refused — a boot takes a second or two.)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v (is an Agent already running in this container?)", addr, err)
	}

	// Move the mutable state off ~/.config/agent-fleet (an EFS mount on ecs-ec2) into the
	// home volume, once (ADR 0087 decision 4). FIRST, before anything below reads a store:
	// every one of them resolves through paths.AgentStateDir now, and a read that lands there
	// before the migration reads an empty store — the session ledger included, which is the
	// Console's whole session list.
	//
	// After the listen above, deliberately: that bind is what stops a second Agent in this
	// container, so the migration cannot be running twice here. What it does not stop is the
	// af-db subcommand, which a user runs by hand — runAFDB migrates for itself.
	if r := statemig.Run(); r.Files > 0 || r.Skipped > 0 || len(r.Errs) > 0 {
		log.Printf("state: migrated %d entr(y|ies), %d file(s), %.1f MB in %s",
			r.Entries, r.Files, float64(r.Bytes)/(1<<20), r.Took.Round(time.Millisecond))
		for _, p := range r.SkippedPaths {
			// Named, not counted: a live socket left behind costs nothing, while a
			// credential left behind is a token the next chat turn will NOT fold back into
			// the shared file — that CLI may ask for a fresh sign-in. The reader of a
			// container log has no source tree to look any of this up in.
			log.Printf("state: left in place (not migrated): %s", p)
		}
		for _, err := range r.Errs {
			log.Printf("state: migration: %v", err)
		}
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
	// Fleet session graph (ADR 0096): resync marks the observation boundary before the
	// server accepts its first request, and the Meta backfill (once per AgentStateDir)
	// writes lineage for sessions that predate the ledger.
	sessionx.FleetGraphResync()
	sessionx.FleetGraphBackfillFromMeta()
	go fleetgraph.StartActivityPruner()
	// Delivery ledger for browser attach handoffs (docs/log/53, completion-notice section):
	// pick up the ones where resolveBrowserHandoff finished last boot but
	// deliverBrowserHandoff did not. It has no busy/idle settle decision, so unlike the
	// reconciler above a single pass is enough.
	browserx.SweepUndeliveredBrowserHandoffs()
	// Repo import jobs (docs/log/78): a clone / checkout dies with the Agent (task
	// replacement, idle-stop). Unless a surviving marker is restored as "interrupted", a
	// half-made working copy comes back into the list looking like an ordinary repository.
	sweepRepoJobMarkers()
	// Image-generation input sets (ADR 0100 decision 4) belong to jobs in a queue that lives in
	// memory, so none of them can still be needed after a restart.
	imagegen.ClearInputSets()
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
	go lcpp.ReconcileManaged("agent boot")
	go muse.ReconcileManaged("agent boot")
	// Assistant-conversation slugs (docs/log/38 assistant triggering): stamp "a…" slugs onto
	// conversations created before the field existed, so schedules/operator tools can
	// address every conversation. One-time per store state; cheap when nothing to do.
	go chatx.BackfillConvSlugs()
	// Databases the member asked to have running (ADR 0086). Only starts what is
	// already installed and marked, so this is a no-op on a workspace that has
	// never used one.
	go afdb.Autostart("agent boot")

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

	// Self-hosted inference engines (ADR 0071 P0): ask the CP which engines this deployment
	// runs and declare them in opencode's config, so `llamacpp/<model>` is in the launch
	// menu. In the background because it is a call out over the public hairpin and nothing
	// else waits on it; a deployment with no engines answers 404 and this is a no-op.
	go syncEngineProviders()

	// Chat-bridge delivery loop (docs/log/37 P1): drains the on-disk queue that
	// notice.Put / record-exit enqueue into (possibly from hook subprocesses)
	// and pushes to the configured chat providers (Discord first).
	bridge.StartSender()

	// Chat-bridge receive (docs/log/37 P2a): the Discord Gateway supervisor that routes the
	// bound user's thread replies back into sessions. No-op until a user opts into receive
	// (Discord.Receive) — bounds the WSS connection to opted-in users only.
	sessionx.StartBridgeReceiver()

	// af-db idle-stop: polls every 60 s and stops servers with no client backends for 30 min.
	afdb.StartIdleLoop()

	log.Printf("workspace-agent %s listening on %s", buildVersion, addr)
	if err := http.Serve(ln, httpx.LogRequests(httpx.Gzip(httpx.RequireToken(mux)))); err != nil {
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

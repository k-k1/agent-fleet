package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Graceful workspace stop (docs/log/p3-7-aws-adapter.md §20b.7.8 stop revision).
// The runtime stops a workspace by delivering SIGTERM to the container — `docker
// stop -t` locally, ECS task stop with stopTimeout on AWS — and SIGKILLs whatever
// survives the grace. Before this handler existed the agent died with Go's
// default disposition (and, as PID 1 on ECS where the kernel suppresses
// default-action signals, ignored SIGTERM outright), so everything inside —
// claude mid-turn, git mid-write, builds — was eventually SIGKILLed cold. Now we
// translate the runtime's SIGTERM into what a person at the keyboard would do:
// Ctrl-C (SIGINT) each pane we own, wait for agent sessions to finish aborting
// their turn, then kill-session each owned session and exit cleanly.

// watchShutdownSignals installs the SIGTERM/SIGINT shutdown handler. The budget
// comes from AGENT_STOP_GRACE_SEC, injected by the CP as the runtime's stop grace
// minus a safety margin so we always finish before the runtime's SIGKILL lands.
func watchShutdownSignals() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-ch
		budget := stopGraceBudget()
		log.Printf("%s received: graceful shutdown (budget %s)", sig, budget)
		gracefulShutdown(budget)
		os.Exit(0)
	}()
}

// stopGraceBudget reads AGENT_STOP_GRACE_SEC (default 25s — the CP's default
// 30s runtime grace minus the safety margin).
func stopGraceBudget() time.Duration {
	if v := os.Getenv("AGENT_STOP_GRACE_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 25 * time.Second
}

// gracefulShutdown Ctrl-C's every pane THIS instance owns, waits (bounded by
// budget) for the status hooks to report no session still working, then
// kill-session's those owned sessions — never `tmux kill-server`. Sessions stay
// resumable — meta + transcripts live in the home volume and the CP serves its
// DB mirror while the workspace is down. This is the same end state as the halt
// endpoint's kill-session, minus a SIGKILL landing mid-write.
//
// Owned = live tmux session ∩ own meta (AF_SESSIONS_DIR). kill-server was the
// original design — one certain command, valid while "the tmux server is mine
// alone" held — but a second agent instance on the same default socket (dev /
// in-container E2E, docs/log/32) shares that server, and its shutdown nuked every
// real session in the workspace, four times. An instance can only know its OWN
// sessions (its metas); anything else on the server — another instance's
// sessions, or an orphan that lost its meta — is indistinguishable from someone
// else's work and must not be touched. In production (one instance, all
// sessions have metas) killing the owned set empties the server, which then
// exits by itself (exit-empty on); orphans merely lose the courtesy C-c and die
// with the container's SIGKILL like any stray process.
func gracefulShutdown(budget time.Duration) {
	deadline := time.Now().Add(budget)
	// Chromium owns a temporary profile and a process group. Close its pipe first
	// so browser pages cannot outlive the Workspace Agent during Stop/recreate.
	browserx.WorkspaceBrowserManager.Close()
	// Attachments own neither Page nor Chromium. Closing them detaches AF's target
	// sessions and WebSockets only; the external owner remains responsible for exit.
	browserx.WorkspaceBrowserAttachmentManager.Close()
	// Carry pending interactions over (docs/log/75 §75.6.3, trigger 2). It must run
	// BEFORE the aborts and the kills, because for everything but claude this is the
	// last chance: claude's pending state stays in the home volume as pending-*, so a
	// later trigger (the listing, the boot hook) can still pick it up, but kiro's
	// approval panel is text in a pane and the three ACP kinds' permission requests
	// live only in the runtime handle's memory, so both are gone the moment
	// AbortManaged / kill-session below runs.
	//
	// Tier 2 (workspace stop) is the only healthy path through here, so without this
	// "stopping to save money" would also throw away a decision a person was in the
	// middle of making (ADR 0055 decision 2).
	promoteCarriedOnShutdown()
	owned := ownedLiveSessions()
	// Managed sessions (docs/log/27 §10.2-8): deliver the abort that corresponds to a
	// pane's C-c. The turn goroutine records cancelled and the status store returns to
	// idle, so the anySessionWorking wait below clears on the same condition as tui.
	opencode.AbortManaged()
	codex.AbortManaged()
	copilot.AbortManaged()
	cursor.AbortManaged()
	kiro.AbortManaged()
	if len(owned) == 0 {
		opencode.Serve().Shutdown()
		codex.Serve().Shutdown()
		copilot.Shutdown()
		cursor.Shutdown()
		kiro.Shutdown()
		return
	}
	for _, tn := range owned {
		// send-keys needs the %N pane id (the "=" exact-session form is a pane
		// syntax error, see tmuxx.SessionPaneID). C-c goes to the pane's foreground
		// process group — exactly a user Ctrl-C: claude aborts the in-flight turn
		// and finalizes its jsonl; a shell interrupts its running job.
		if pane := tmuxx.SessionPaneID(tn); pane != "" {
			_ = tmuxx.Cmd("send-keys", "-t", pane, "C-c").Run()
		}
	}
	// Wait for the interrupts to land: agent sessions flip working→idle via the
	// status hooks once the turn abort has flushed the transcript.
	for time.Now().Before(deadline) {
		if !anySessionWorking() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Short settle for panes without status hooks (shells, builds) so their
	// foreground processes can run SIGINT handlers before their session goes away.
	time.Sleep(time.Second)
	// The aborts are done by now, so the shared daemons' drain returns immediately
	// (they exit on SIGTERM).
	opencode.Serve().Shutdown()
	codex.Serve().Shutdown()
	copilot.Shutdown()
	cursor.Shutdown()
	kiro.Shutdown()
	for _, tn := range owned {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
}

// promoteCarriedOnShutdown snapshots every LIVE session's pending modal into the
// carried store before the shutdown tears the panes and runtime handles down.
//
// Only live sessions are inspected: a stopped one has nothing to look into, and
// looking anyway would make a managed session Resume — starting the very thread we
// are trying to fold away. (promoteCarriedOther does not do that, but the liveness
// test is repeated here to state the intent.) Promotion is idempotent and never
// overwrites, so anything halt already promoted stays as it is.
func promoteCarriedOnShutdown() {
	for _, m := range session.ListMetas() {
		if m.Archived || !sessionx.SessionAlive(m) {
			continue
		}
		sessionx.PromoteCarriedFor(m)
	}
}

// ownedLiveSessions returns the tmux names of the live sessions this instance
// manages — the intersection of the live claude_* sessions on our socket with
// our own session metas. Metas without a live pane (stopped, managed driver)
// and live sessions without our meta (another instance's, or meta-less
// orphans) both drop out.
func ownedLiveSessions() []string {
	live := tmuxx.LiveSessionNames()
	if len(live) == 0 {
		return nil
	}
	var owned []string
	for _, m := range session.ListMetas() {
		if live[m.Name] {
			owned = append(owned, session.TmuxName(m.Name))
		}
	}
	return owned
}

// anySessionWorking reports whether any LIVE session's status hook still says
// "working". Shell panes have no status file and are covered by the settle sleep
// instead; a stale status without a live tmux session is ignored.
func anySessionWorking() bool {
	live := tmuxx.LiveSessionNames()
	if len(live) == 0 {
		return false
	}
	for _, m := range session.ListMetas() {
		if !live[m.Name] {
			continue
		}
		if s, ok := status.Read(session.UUID(m.Dir, m.Name)); ok && s.State == "working" {
			return true
		}
	}
	return false
}

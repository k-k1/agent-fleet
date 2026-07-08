package main

import (
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Graceful workspace stop (docs/history/p3-7-aws-adapter.md §20b.7.8 停止改訂).
// The runtime stops a workspace by delivering SIGTERM to the container — `docker
// stop -t` locally, ECS task stop with stopTimeout on AWS — and SIGKILLs whatever
// survives the grace. Before this handler existed the agent died with Go's
// default disposition (and, as PID 1 on ECS where the kernel suppresses
// default-action signals, ignored SIGTERM outright), so everything inside —
// claude mid-turn, git mid-write, builds — was eventually SIGKILLed cold. Now we
// translate the runtime's SIGTERM into what a person at the keyboard would do:
// Ctrl-C (SIGINT) each live pane, wait for agent sessions to finish aborting
// their turn, then tear the tmux server down and exit cleanly.

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

// gracefulShutdown Ctrl-C's every live pane, waits (bounded by budget) for the
// status hooks to report no session still working, then kills the tmux server.
// Sessions stay resumable — meta + transcripts live in the home volume and the CP
// serves its DB mirror while the workspace is down. This is the same end state as
// the halt endpoint's kill-session, minus a SIGKILL landing mid-write.
func gracefulShutdown(budget time.Duration) {
	deadline := time.Now().Add(budget)
	live := tmuxx.LiveSessionNames()
	if len(live) == 0 {
		_ = exec.Command("tmux", "kill-server").Run()
		return
	}
	for name := range live {
		// send-keys needs the %N pane id (the "=" exact-session form is a pane
		// syntax error, see tmuxx.SessionPaneID). C-c goes to the pane's foreground
		// process group — exactly a user Ctrl-C: claude aborts the in-flight turn
		// and finalizes its jsonl; a shell interrupts its running job.
		if pane := tmuxx.SessionPaneID(session.TmuxName(name)); pane != "" {
			_ = exec.Command("tmux", "send-keys", "-t", pane, "C-c").Run()
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
	// foreground processes can run SIGINT handlers before the server goes away.
	time.Sleep(time.Second)
	_ = exec.Command("tmux", "kill-server").Run()
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

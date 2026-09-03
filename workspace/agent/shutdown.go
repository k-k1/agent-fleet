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

// Graceful workspace stop (docs/log/p3-7-aws-adapter.md §20b.7.8 停止改訂).
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
	// ★保留中の対話を持ち越す（docs/log/75 §75.6.3 の契機 2）。**abort と kill より前**に
	// 置くのが要点で、ここが claude 以外にとっては最後の機会になる: claude の保留は
	// pending-* としてホームに残るので後の契機（一覧・boot フック）が拾えるが、kiro の
	// 承認パネルはペインの文字列、ACP 3 種の許可要求は runtime handle のメモリにしか
	// 無く、下の AbortManaged / kill-session を通った時点で消える。
	//
	// tier2（Workspace 停止）はここを通る唯一の正常系なので、これが無いと「費用のために
	// 止めたら、止めたことで人の判断が消えた」になる（ADR 0055 決定 2）。
	promoteCarriedOnShutdown()
	owned := ownedLiveSessions()
	// managed セッション（docs/log/27 §10.2-8）: pane の C-c に相当する abort を配る。
	// turn goroutine が cancelled を刻み status ストアが idle へ戻るので、下の
	// anySessionWorking 待ちが tui と同じ条件で解ける。
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
	// 共有 daemon は abort が済んだ後なので drain は即座に抜ける（SIGTERM で終了）。
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
// 生きているものだけを見る: 停止中のセッションには覗く先が無く、覗こうとすると
// managed では Resume が走って**畳もうとしている thread を立ち上げてしまう**
// （promoteCarriedOther はそれをしないが、生存判定をここでも先に置いて意図を明示する）。
// 昇格は冪等・非上書きなので、halt で既に昇格済みのものはそのまま残る。
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

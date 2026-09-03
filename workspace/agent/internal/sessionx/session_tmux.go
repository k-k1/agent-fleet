package sessionx

// tmux まわりのセッションオーケストレーション: 起動/再生成、ディレクトリ配下の
// セッション判定。純粋な tmux プロービング（存在確認・pane 解決/キャプチャ・
// 生存一覧・pane 種別）は internal/tmuxx へ移設（docs/log/23 残① Wave A）。

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// startSessionTmux launches the detached tmux session for m. For claude it builds
// the resume/new program (claude 縦割りの buildProgram が jsonl の有無で --resume を
// 選ぶ); for shell it runs a login bash.
func startSessionTmux(m session.Meta, ssmForce bool) error {
	// Write the MCP registry into this kind's own config before the CLI reads it
	// (docs/log/48 §8.3). Doing it here rather than in BuildLaunch keeps it out of the
	// per-kind agents, and covers every tui launch — create, start, recreate, handoff.
	mcpx.Materialize(m.Kind)
	// Same timing for claude's own settings.json wiring (hooks + statusLine): it is
	// shared state that another build of the agent can leave pointing at a binary that
	// no longer exists.
	EnsureClaudeSettingsWiring(m.Kind)
	// The kind decides the pane program and launch dir; the agent builds both.
	plan, err := AgentOf(m.Kind).BuildLaunch(m, agents.LaunchOpts{SSMForce: ssmForce})
	if err != nil {
		return err
	}
	// Session-side MCP tools need a provider-neutral owner identity. Native IDs differ
	// across CLIs, while this slug is stable for every Agent Fleet session.
	plan.Env = append(plan.Env, "AF_SESSION_NAME="+m.Name)
	// Inject the current toolchain selection (JAVA_HOME / node / TZ) so a Console
	// change applies to this freshly-launched session without a Stop→Start. tmux
	// runs the pane command via /bin/sh -c, so the export prefix takes effect.
	program := toolchainShellPrefix() + plan.Program
	// Append an exit recorder so we capture WHY a session ends (normal / crash / OOM).
	// The pane's shell outlives the agent CLI, so $? here is the CLI's wait status
	// (128+signal on a kill → OOM SIGKILL shows as 137). A deliberate `tmux
	// kill-session` kills this shell too, so it records nothing — intentional stops are
	// never mislabeled as crashes (see record_exit.go). m.Name is a validated slug, so
	// it needs no shell escaping beyond the single quotes.
	program += "; __af_ec=$?; workspace-agent record-exit '" + m.Name + "' \"$__af_ec\""
	args := []string{"new-session", "-d", "-s", session.TmuxName(m.Name), "-c", plan.Cwd}
	// Secrets ride -e (pane process env), never the command string (plan.Env contract).
	for _, kv := range plan.Env {
		args = append(args, "-e", kv)
	}
	args = append(args, program)
	if out, err := tmuxx.Cmd(args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	// Record pane output even when no browser is attached. The helper owns the
	// size cap; failure here must not prevent the actual session from starting.
	if pane := tmuxx.SessionPaneID(session.TmuxName(m.Name)); pane != "" {
		_ = tmuxx.Cmd("pipe-pane", "-o", "-t", pane,
			"workspace-agent record-terminal '"+m.Name+"'").Run()
	}
	// Baseline the container's oom_kill counter so a later crash is attributed to an OOM
	// only when the counter advanced during THIS session. Writing it also clears any
	// prior death record for this name, so a resumed session starts clean.
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return nil
}

// ensureSessionTmux (re)creates the tmux session from its recorded meta when it is
// not currently alive — used on attach so a clicked-but-exited session relaunches
// claude rather than the default shell. nil means the session is now running.
// managed セッション（docs/log/27 P2）は tmux でなく driver.Resume（runtime handle の
// 再接続＝§6 の reconciliation）で「起動」する — /start の意味論は両ドライバで同じ。
//
// Every failure is REPORTED, never swallowed: /start answering {"ok":true} for a
// launch that never happened is worse than a plain error, because the Console then
// waits for a session that is not coming and offers the user no way to retry.
func ensureSessionTmux(name string, ssmForce bool) error {
	m, ok := session.ReadMeta(name)
	if ok && m.DriverKind() == session.DriverManaged {
		if ManagedAlive(m) {
			return nil
		}
		d, dok := driverOf(m)
		if !dok {
			return fmt.Errorf("no driver for session %s (kind %q)", name, m.Kind)
		}
		if _, err := mcpx.StartManagedSession(d, m); err != nil {
			log.Printf("managed resume %s: %v", name, err)
			return err
		}
		// 再開＝停止扱いの解除。次の一覧ポーリングでも clear されるが、/start 応答の
		// 直後に halt 直前の StoppedAt が残っていると紛らわしいのでここで消す。
		if m.StoppedAt != "" {
			m.StoppedAt = ""
			session.WriteMeta(m)
		}
		return nil
	}
	if tmuxx.HasSession(session.TmuxName(name)) {
		return nil
	}
	if !ok {
		return fmt.Errorf("no meta for session %s", name)
	}
	if err := startSessionTmux(m, ssmForce); err != nil {
		log.Printf("resume %s: %v", name, err)
		return err
	}
	return nil
}

// LiveSessionsInDir returns the display names of running sessions whose cwd is at
// or under dir. Switching branches in dir would swap the working tree beneath
// these processes mid-flight (vanished/rewritten files, stale diffs, edits landing
// on the wrong branch) — the "大惨事" this guards against — so callers refuse the
// operation while this is non-empty. Only LIVE sessions count: a stopped session
// has no process to corrupt (branch drift for those is handled elsewhere). Archived
// sessions are ignored. A subdir cwd still counts because checkout rewrites the
// whole working tree, not just the repo root.
func LiveSessionsInDir(dir string) []string {
	return sessionsInDir(session.ListMetas(), tmuxx.LiveSessionNames(), dir)
}

// sessionsInDir is the pure core of LiveSessionsInDir (tmux/fs kept out so it is
// testable): from metas + the live set, the display names of running, non-archived
// sessions whose cwd equals dir or sits strictly beneath it. The trailing
// PathSeparator on the prefix test is load-bearing — it keeps "/r/foo" from matching
// a sibling "/r/foobar".
func sessionsInDir(metas []session.Meta, live map[string]bool, dir string) []string {
	var names []string
	for _, m := range metas {
		if m.Archived || !live[m.Name] {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			names = append(names, session.Display(m))
		}
	}
	sort.Strings(names)
	return names
}

// LockedSessionsInDir returns the display names of DELETE-LOCKED sessions (docs/log/45)
// whose cwd is dir or sits beneath it — running or not, archived included. Removing
// the working copy would strand them (their dir vanishes, resume is gone), which is
// exactly what the lock exists to prevent, so a working-copy delete refuses while any
// of these live there. Pure, for the same reason as sessionsInDir.
func LockedSessionsInDir(metas []session.Meta, dir string) []string {
	var names []string
	for _, m := range metas {
		if !m.Locked {
			continue
		}
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			names = append(names, session.Display(m))
		}
	}
	sort.Strings(names)
	return names
}

// WorktreeHasSessions reports whether ANY session meta (live, stopped, or archived)
// still has its cwd at or under dir. Auto-pruning a worktree checks this first so a
// working copy that a stopped/archived session could still resume or restore into is
// never removed out from under it.
func WorktreeHasSessions(dir string) bool {
	for _, m := range session.ListMetas() {
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

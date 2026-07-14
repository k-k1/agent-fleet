package main

// tmux まわりのセッションオーケストレーション: 起動/再生成、ディレクトリ配下の
// セッション判定。純粋な tmux プロービング（存在確認・pane 解決/キャプチャ・
// 生存一覧・pane 種別）は internal/tmuxx へ移設（docs/23 残① Wave A）。

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// startSessionTmux launches the detached tmux session for m. For claude it builds
// the resume/new program (claude 縦割りの buildProgram が jsonl の有無で --resume を
// 選ぶ); for shell it runs a login bash.
func startSessionTmux(m session.Meta, ssmForce bool) error {
	// The kind decides the pane program and launch dir; the agent builds both.
	plan, err := agentOf(m.Kind).BuildLaunch(m, agents.LaunchOpts{SSMForce: ssmForce})
	if err != nil {
		return err
	}
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
	args := []string{"new-session", "-d", "-s", session.TmuxName(m.Name), "-c", plan.Cwd, program}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	// Record pane output even when no browser is attached. The helper owns the
	// size cap; failure here must not prevent the actual session from starting.
	if pane := tmuxx.SessionPaneID(session.TmuxName(m.Name)); pane != "" {
		_ = exec.Command("tmux", "pipe-pane", "-o", "-t", pane,
			"workspace-agent record-terminal '"+m.Name+"'").Run()
	}
	// Baseline the container's oom_kill counter so a later crash is attributed to an OOM
	// only when the counter advanced during THIS session. Writing it also clears any
	// prior death record for this name, so a resumed session starts clean.
	base, _ := containerOOMKill()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return nil
}

// ensureSessionTmux (re)creates the tmux session from its recorded meta when it is
// not currently alive — used on attach so a clicked-but-exited session relaunches
// claude rather than the default shell. Reports whether a meta was found.
func ensureSessionTmux(name string, ssmForce bool) bool {
	if tmuxx.HasSession(session.TmuxName(name)) {
		return true
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return false
	}
	_ = startSessionTmux(m, ssmForce)
	return true
}

// liveSessionsInDir returns the display names of running sessions whose cwd is at
// or under dir. Switching branches in dir would swap the working tree beneath
// these processes mid-flight (vanished/rewritten files, stale diffs, edits landing
// on the wrong branch) — the "大惨事" this guards against — so callers refuse the
// operation while this is non-empty. Only LIVE sessions count: a stopped session
// has no process to corrupt (branch drift for those is handled elsewhere). Archived
// sessions are ignored. A subdir cwd still counts because checkout rewrites the
// whole working tree, not just the repo root.
func liveSessionsInDir(dir string) []string {
	return sessionsInDir(session.ListMetas(), tmuxx.LiveSessionNames(), dir)
}

// sessionsInDir is the pure core of liveSessionsInDir (tmux/fs kept out so it is
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

// worktreeHasSessions reports whether ANY session meta (live, stopped, or archived)
// still has its cwd at or under dir. Auto-pruning a worktree checks this first so a
// working copy that a stopped/archived session could still resume or restore into is
// never removed out from under it.
func worktreeHasSessions(dir string) bool {
	for _, m := range session.ListMetas() {
		if m.Dir == dir || strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

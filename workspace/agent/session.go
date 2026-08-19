package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// セッションのワイヤ変換とタイトル/ラベル導出。モデル・メタ永続化・UUID は
// internal/session（docs/23 残① Wave A）/ tmux= session_tmux.go /
// HTTPハンドラ= session_handlers.go / claude の CLI 起動コマンドは
// internal/agents/claude の program.go（docs/23 残① Wave F）

// wireSession builds the API representation from a meta and liveness.
func wireSession(m session.Meta, alive bool) session.Session {
	started := ""
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			started = t.Local().Format("01/02 15:04")
		}
	}
	// The live-dependent fields (state / remote URL / context / resumable / bg-busy)
	// diverge by kind — the agent computes them (see WireLive per implementation).
	li := agentOf(m.Kind).WireLive(m, alive)
	s := session.Session{
		Name: m.Name, Tmux: session.TmuxName(m.Name), Dir: m.Dir, Subdir: m.Subdir, Kind: m.Kind, Driver: m.Driver,
		Repo: m.Repo, WorkingCopyID: workingCopyID(m.Dir), Title: m.Title, Display: session.Display(m), Color: m.Color, Label: m.Label,
		Started: started, CreatedAt: m.CreatedAt, Branch: m.Branch,
		RemoteUrl: li.RemoteURL, State: li.State, Alive: alive, Resumable: li.Resumable,
		BackgroundBusy: li.BackgroundBusy, Context: li.Context, Locked: m.Locked, Archived: m.Archived,
	}
	// 上限で切れたターンの後始末が済んだ claude（メニューは自動解除済み／モデル別上限は
	// そもそもメニューを出さない）はペインが待機プロンプトに戻るので、ここまでの状態は
	// idle＝入力待ちになる。リセットを待っているだけなのに正常終了と見分けが付かない
	// ので、開いているエピソードがある間は 制限解除待ち として名乗る（docs/47 §4-9）。
	// WireLive ではなくここで見るのはエピソードの持ち主が package main だからで、
	// driveState（チャット／ミラーのチップ）にも同じ読み替えが要る。
	if alive && s.State == "idle" && normalizeKind(m.Kind) == session.KindClaude {
		if at, waiting := rateLimitWaiting(m, time.Now()); waiting {
			s.State = agents.StateLimited
			s.RateLimitResumeAt = at
		}
	}
	// For a stopped session, surface WHY it ended (crash / OOM) if the pane recorder
	// captured a cause. A clean quit (exited) or a deliberate stop (empty reason) leaves
	// the fields unset so the row shows the plain 停止中 chip.
	if !alive {
		if e, ok := status.ReadExit(m.Name); ok && e.Reason != "" && e.Reason != "exited" && e.Reason != "stopped" {
			s.ExitReason = e.Reason
			s.ExitCode = e.Code
			s.ExitSignal = e.Signal
		}
	}
	return s
}

// remoteSessionURL（claude.ai Remote Control URL の導出）は internal/agents/claude
// の claude.RemoteSessionURL へ移設（docs/23 残① Wave F）。

// dirInfo is a working copy's current branch + worktree flag, cached per dir.
type dirInfo struct {
	branch   string
	worktree bool
}

// annotateSessions enriches each session from its working copy: Worktree (is it a
// linked worktree) and BranchDrift/CurrentBranch (was it switched off its start
// branch). info resolves a dir once and is cached, so N sessions sharing a working
// copy cost a single git call. Drift needs a recorded start branch; worktree does not.
// Split from the git/tmux plumbing so it is unit-testable.
func annotateSessions(sessions []session.Session, info func(string) dirInfo) {
	cache := map[string]dirInfo{}
	for i := range sessions {
		s := &sessions[i]
		if s.Dir == "" {
			continue
		}
		v, ok := cache[s.Dir]
		if !ok {
			v = info(s.Dir)
			cache[s.Dir] = v
		}
		s.Worktree = v.worktree
		if s.Branch != "" && v.branch != "" && v.branch != s.Branch {
			s.BranchDrift = true
			s.CurrentBranch = v.branch
		}
	}
}

// sessionLabelFor builds the claude --name for a session. When the user supplied a
// title it's "[AF] {title}"; otherwise it falls back to the auto default
// "[AF] {repo} @MMDD-HHMM" where {repo} is the working dir's basename and the time is
// the workspace's local time (the entrypoint exports TZ from the per-user timezone
// setting, default JST). The "[AF] " tag identifies Agent-Fleet-launched sessions in
// the claude.ai Remote Control picker. Computed at create/recreate and stored in the
// meta so relaunch keeps the same name.
func sessionLabelFor(dir, title string) string {
	if title != "" {
		return "[AF] " + title
	}
	return fmt.Sprintf("[AF] %s @%s", filepath.Base(dir), time.Now().Format("0102-1504"))
}

// cleanTitle trims and validates a user-supplied display title. It rejects control
// characters (which would corrupt the tmux title / claude --name) and caps the length.
// An empty title is valid (the session uses the auto default label). Returns ok=false
// only for an over-long or control-laden title.
func cleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// forkTitle derives a fork's display title from its source: the source's own title
// when set, else its stripped label (the auto "{repo} @time"), suffixed " (fork)".
func forkTitle(src session.Meta) string {
	base := src.Title
	if base == "" {
		base = strings.TrimPrefix(src.Label, "[AF] ")
	}
	return strings.TrimSpace(base + " (fork)")
}

// shellQuote は internal/session の session.ShellQuote へ移設（docs/23 残① Wave D）。

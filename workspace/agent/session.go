package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// セッションのワイヤ変換とタイトル/ラベル導出。モデル・メタ永続化・UUID は
// internal/session（docs/23 残① Wave A）/ tmux= session_tmux.go /
// HTTPハンドラ= session_handlers.go / CLI起動コマンド= session_program.go（docs/23 P1-W4）

// wireSession builds the API representation from a meta and liveness.
func wireSession(m session.Meta, alive bool) session.Session {
	started := ""
	if m.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			started = t.Local().Format("01/02 15:04")
		}
	}
	// The live-dependent fields (state / remote URL / context / resumable / bg-busy)
	// diverge by kind — the agent computes them (see wireLive per implementation).
	li := agentOf(m.Kind).wireLive(m, alive)
	return session.Session{
		Name: m.Name, Tmux: session.TmuxName(m.Name), Dir: m.Dir, Kind: m.Kind,
		Repo: m.Repo, Title: m.Title, Display: session.Display(m), Color: m.Color, Label: m.Label,
		Started: started, CreatedAt: m.CreatedAt, Branch: m.Branch,
		RemoteUrl: li.remoteURL, State: li.state, Alive: alive, Resumable: li.resumable,
		BackgroundBusy: li.backgroundBusy, Context: li.context,
	}
}

// remoteSessionURL derives the claude.ai Remote Control page for sid from its
// jsonl "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". We read only the
// head of the log (the bridge line is written at session start) to stay cheap on
// the polled list. Returns "" when there is no bridge (RC off / not yet connected).
func remoteSessionURL(sid string) string {
	for _, p := range jsonlPaths(sid) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		buf := make([]byte, 64*1024)
		n, _ := f.Read(buf)
		f.Close()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !strings.Contains(line, `"type":"bridge-session"`) {
				continue
			}
			var b struct {
				BridgeSessionID string `json:"bridgeSessionId"`
			}
			if json.Unmarshal([]byte(line), &b) == nil && b.BridgeSessionID != "" {
				return "https://claude.ai/code/session_" + strings.TrimPrefix(b.BridgeSessionID, "cse_")
			}
		}
	}
	return ""
}

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

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

package sessionx

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Wire conversion for sessions, plus title/label derivation. The model, meta persistence
// and UUIDs live in internal/session (docs/log/23 remaining item 1 Wave A); tmux in
// session_tmux.go; the HTTP handlers in session_handlers.go; claude's CLI launch command
// in internal/agents/claude/program.go (docs/log/23 remaining item 1 Wave F).

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
	li := AgentOf(m.Kind).WireLive(m, alive)
	s := session.Session{
		Name: m.Name, Tmux: session.TmuxName(m.Name), Dir: m.Dir, Subdir: m.Subdir, Kind: m.Kind, Driver: m.Driver,
		Repo: m.Repo, WorkingCopyID: gitx.WorkingCopyID(m.Dir), Title: m.Title, Display: session.Display(m), Color: m.Color, Label: m.Label,
		Started: started, CreatedAt: m.CreatedAt, Branch: m.Branch,
		RemoteUrl: li.RemoteURL, State: li.State, Alive: alive, Resumable: li.Resumable,
		BackgroundBusy: li.BackgroundBusy, BackgroundBusyReason: li.BackgroundBusyReason,
		Context: li.Context, Locked: m.Locked, Archived: m.Archived,
		KeepAwakeUntil: m.KeepAwakeUntil,
	}
	// A claude whose limit-aborted turn has been cleaned up (the menu dismisses itself, and a
	// per-model limit never raises one) leaves its pane back at the waiting prompt, so
	// everything up to here reads idle = waiting for input. That is indistinguishable from a
	// normal finish even though the session is only waiting for the reset, so while an episode
	// is open it names itself as waiting for the limit to lift (docs/log/47 §4-9). The check
	// sits here rather than in WireLive because package main owns the episode; DriveState (the
	// chat / mirror chip) needs the same reinterpretation.
	if alive && s.State == "idle" && NormalizeKind(m.Kind) == session.KindClaude {
		if state, at, waiting := rateLimitWaiting(m, time.Now()); waiting {
			s.State = state
			s.RateLimitResumeAt = at
		}
	}
	// For a stopped session, surface WHY it ended (crash / OOM) if the pane recorder
	// captured a cause. A clean quit (exited) or a deliberate stop (empty reason) leaves
	// the fields unset so the row shows the plain stopped chip.
	if !alive {
		if e, ok := status.ReadExit(m.Name); ok && e.Reason != "" && e.Reason != "exited" && e.Reason != "stopped" {
			s.ExitReason = e.Reason
			s.ExitCode = e.Code
			s.ExitSignal = e.Signal
		}
		// The dialog that was still waiting for an answer when the session was folded away
		// (docs/log/75). Never shown on a live row: there State (question / plan / permission)
		// already describes the modal that is up right now, and showing the carried one
		// alongside it reads as "something I already answered is still pending".
		if c, ok := status.ReadCarried(session.UUID(m.Dir, m.Name)); ok {
			s.Carried = c.Kind
		}
	}
	return s
}

// remoteSessionURL (deriving the claude.ai Remote Control URL) lives in
// internal/agents/claude as claude.RemoteSessionURL.

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
// title it's "[AF:{name}] {title}"; otherwise it falls back to the auto default
// "[AF:{name}] {repo} @MMDD-HHMM" where {repo} is the working dir's basename and the
// time is the workspace's local time (the entrypoint exports TZ from the per-user
// timezone setting, default JST). The tag identifies Agent-Fleet-launched sessions in
// the claude.ai Remote Control picker; the session name inside it is what keeps two
// sessions with the SAME title apart — see internal/session/label.go for why that
// matters (claude's own cross-session channel addresses sessions by this string, and
// a duplicate title silently misdelivers). Computed at create/recreate and stored in
// the meta so relaunch keeps the same name.
func sessionLabelFor(dir, title, name string) string {
	tag := session.LabelPrefix(name)
	if title != "" {
		return tag + title
	}
	return fmt.Sprintf("%s%s @%s", tag, filepath.Base(dir), time.Now().Format("0102-1504"))
}

// SessionTitleMaxRunes is THE limit for a session's display title, in runes. Every
// layer that can end up as a session title must use this one — a proposal stored under
// a laxer cap (the handoff card once allowed 512 bytes) is accepted, shown and edited
// fine, and only fails at the launch that finally hands it to POST /sessions. The
// Console mirrors this value in console/src/lib/sessionTitle.ts.
const SessionTitleMaxRunes = 80

// CleanTitle trims and validates a user-supplied display title. It rejects control
// characters (which would corrupt the tmux title / claude --name) and caps the length.
// An empty title is valid (the session uses the auto default label). Returns ok=false
// only for an over-long or control-laden title.
func CleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > SessionTitleMaxRunes {
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
		base = session.StripLabel(src.Label)
	}
	return strings.TrimSpace(base + " (fork)")
}

// shellQuote lives in internal/session as session.ShellQuote.

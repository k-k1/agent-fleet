package claude

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Optional agents capabilities of the claude kind, each delegating to this package's own
// functions.

// ListModels is agents.ModelLister: the fixed tier aliases (Models) followed by the member's
// registered models the aliases do not already name, compared case-insensitively.
func (agentImpl) ListModels(q agents.ModelQuery) agents.ModelListing {
	list := Models()
	seen := make(map[string]bool, len(list))
	for _, model := range list {
		seen[strings.ToLower(model.ID)] = true
	}
	for _, id := range q.CustomModels {
		if key := strings.ToLower(id); !seen[key] {
			list = append(list, agents.ModelChoice{ID: id, Label: id, Efforts: EffortsFor(id)})
			seen[key] = true
		}
	}
	return agents.ModelListing{Models: list, Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: false, a Terminal send is typed without
// waiting for the composer.
func (agentImpl) SendWaitsForComposer() bool { return false }

// SkillsLayout is agents.SkillsLayouter. claude reads <d>/.claude/skills and <d>/.claude/commands
// for the CWD and every ancestor below $HOME, then the personal root (CLAUDE_CONFIG_DIR, where
// AF redirects $HOME/.claude), skills before commands, invoked as "/name"; the CLI's bundled
// skills come from its init frame (measured 2026-07-28 and 2026-09-12, docs/log/50 §7, §9, §10).
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{
		Native:      agents.SkillsScan,
		Invoke:      "/",
		Project:     []agents.SkillRoot{{Rel: ".claude/skills", Form: "skills"}, {Rel: ".claude/commands", Form: "commands"}},
		ProjectDirs: agents.SkillDirsCWDToHome,
		User: []agents.UserSkillRoot{
			{Dir: func() string { return filepath.Join(ConfigDir(), "skills") }, Source: "user", Form: "skills"},
			{Dir: func() string { return filepath.Join(ConfigDir(), "commands") }, Source: "user", Form: "commands"},
		},
		Bundled:     true,
		NativeConvs: []string{".claude/skills"},
		Shared:      &agents.SharedSkillRoot{Origin: ".claude", Dir: func() string { return filepath.Join(ConfigDir(), "skills") }},
	}
}

// TerminalTraits is agents.TerminalTraiter: claude's TUI traps and live-state verdicts, each
// delegating to this package or to tmuxx's claude pane probes.
func (agentImpl) TerminalTraits() agents.TerminalTraits {
	return agents.TerminalTraits{
		AuthExpired:      AuthExpired,
		UsageLimitMenu:   tmuxx.AtRateLimitModal,
		AgentsViewActive: tmuxx.AgentsViewActive,
		LeaveAgentsView:  tmuxx.LeaveAgentsView,
		ModalActive:      tmuxx.ModalActive,
		ShapePrompt:      QuoteImagePaths,
		PasteLF:          true,
		SubmitDelay:      20 * time.Millisecond,
		RemoteControlURL: RemoteSessionURL,
		HookPending:      true,
		TranscriptLines:  true,
		TerminalScreen:   func(pane string) string { st, _ := ClassifyPane(pane); return st },
		HealIdle:         HealIdle,
	}
}

// ConnectionStatus is agents.ConnectionStatuser: this package's Status().
func (agentImpl) ConnectionStatus(agents.StatusInputs) map[string]any { return Status() }

// UsageLimitTraits is agents.UsageLimiter: the pane's /rate-limit-options menu, the transcript
// tail's usage-limit abort (UsageLimitAbort) and its reset instant (ResetAt).
func (agentImpl) UsageLimitTraits() agents.UsageLimitTraits {
	return agents.UsageLimitTraits{
		PaneMenu: func(name string) bool { return tmuxx.ReadPane(name).RateLimitMenu },
		AtLimit: func(m session.Meta) (agents.LimitKind, bool) {
			_, kind, atLimit := UsageLimitAbort(session.UUID(m.Dir, m.Name))
			return kind, atLimit
		},
		ResetAt: func(m session.Meta, menu bool, now time.Time) (time.Time, string, bool) {
			at, source, ok := ResetAt(session.UUID(m.Dir, m.Name), now)
			return at, source, bannerGated(source, ok, menu)
		},
	}
}

// TUIInterrupt is agents.TUIInterrupter: Esc interrupts a running turn (docs/log/133 §1).
func (agentImpl) TUIInterrupt(func() bool) agents.TUIInterruptTraits {
	return agents.TUIInterruptEscape()
}

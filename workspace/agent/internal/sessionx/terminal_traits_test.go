package sessionx

import (
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

func funcPtr(f any) uintptr {
	if f == nil || reflect.ValueOf(f).IsNil() {
		return 0
	}
	return reflect.ValueOf(f).Pointer()
}

// The claude and codex specials of session_io.go and DriveState are agents.TerminalTraits
// (#2870). Each trait must belong to exactly the kinds the kind condition it replaced selected
// (kept verbatim below, NormalizeKind's claude fallback included), and call the very function
// that condition's site called; a trait that drifts to another kind or function goes red here.
func TestTerminalTraitsBelongToTheirKinds(t *testing.T) {
	type site struct {
		name  string
		old   func(kind string) bool
		trait func(kind string) any // the func the site will call, or a bool
		want  any                   // the function the old site called (nil for a bool trait)
	}
	norm := normalizedTerminalTraits
	exact := terminalTraits
	sites := []site{
		{"promptBlocker / initial prompt / DriveState auth", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).AuthExpired }, claude.AuthExpired},
		{"rate-limit modal / DriveState menu", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).UsageLimitMenu }, tmuxx.AtRateLimitModal},
		{"agents view", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).AgentsViewActive }, tmuxx.AgentsViewActive},
		{"agents view leave", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).LeaveAgentsView }, tmuxx.LeaveAgentsView},
		{"view-nav key", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).ModalActive }, tmuxx.ModalActive},
		{"DriveState heal", func(k string) bool { return NormalizeKind(k) == session.KindClaude },
			func(k string) any { return norm(k).HealIdle }, claude.HealIdle},
		{"typePromptText quote", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).ShapePrompt }, claude.QuoteImagePaths},
		{"disconnectRemoteControl", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).RemoteControlURL }, claude.RemoteSessionURL},
		{"DriveState missed turn end", func(k string) bool { return k == session.KindCodex },
			func(k string) any { return exact(k).MissedTurnEnd }, codex.MissedTurnEnd},
		{"DriveState terminal question", func(k string) bool { return k == session.KindCodex },
			func(k string) any { return exact(k).TerminalQuestion }, codex.TerminalModal},
		{"typePromptText LF", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).PasteLF }, nil},
		{"inputSubmitDelay", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).SubmitDelay == 20*time.Millisecond }, nil},
		{"/status pending payloads", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).HookPending }, nil},
		{"/output transcript lines", func(k string) bool { return k == session.KindClaude },
			func(k string) any { return exact(k).TranscriptLines }, nil},
		{"rememberCodexTUIMode", func(k string) bool { return k == session.KindCodex },
			func(k string) any { return exact(k).RemembersMode }, nil},
		{"sessionInputReady screens", func(k string) bool { return k == session.KindClaude || k == session.KindCodex },
			func(k string) any { return exact(k).TerminalScreen }, nil},
	}
	kinds := append(RegisteredKinds(), "", "Claude", " codex", "pi")
	for _, s := range sites {
		selected := 0
		for _, k := range kinds {
			v := s.trait(k)
			var has bool
			if b, isBool := v.(bool); isBool {
				has = b
			} else {
				has = funcPtr(v) != 0
				if has && s.want != nil && funcPtr(v) != funcPtr(s.want) {
					t.Errorf("%s, %q: the trait calls another function than the old site", s.name, k)
				}
			}
			if has != s.old(k) {
				t.Errorf("%s, %q: trait present = %v, old condition = %v", s.name, k, has, s.old(k))
			}
			if has {
				selected++
			}
		}
		if selected == 0 {
			t.Errorf("%s: no kind selected; the comparison proves nothing", s.name)
		}
	}
	if d := exact(session.KindCodex).SubmitDelay; d != 0 {
		t.Errorf("codex SubmitDelay = %v, want the default (0)", d)
	}
}

// The screens sessionInputReady refuses on: claude's classifier and codex's update menu, for the
// same panes.
func TestTerminalScreenEqualsTheClassifiers(t *testing.T) {
	panes := []string{"", "1. Resume from summary\n2. Resume full session as-is",
		"✻ Compacting conversation… (1m 2s) 40%", "  ✨ Update available! 0.144.3 -> 0.999.0\n› 1. Update now (runs `npm install -g @openai/codex`)\n  2. Skip\n  3. Skip until next version\n  Press enter to continue\n",
		"› reply with exactly: pong\n• pong\n"}
	seen := map[string]bool{}
	for _, p := range panes {
		old, _ := claude.ClassifyPane(p)
		seen[old] = true
		if got := terminalTraits(session.KindClaude).TerminalScreen(p); got != old {
			t.Errorf("claude %q: trait %q, classifier %q", p, got, old)
		}
		want := ""
		if isCodexUpdateMenu(p) {
			want = "update"
		}
		if got := terminalTraits(session.KindCodex).TerminalScreen(p); got != want {
			t.Errorf("codex %q: trait %q, isCodexUpdateMenu %q", p, got, want)
		}
		seen["codex-"+want] = true
	}
	for _, s := range []string{"resume", "compacting", "codex-update"} {
		if !seen[s] {
			t.Errorf("no fixture reads as %s; the comparison does not exercise it", s)
		}
	}
}

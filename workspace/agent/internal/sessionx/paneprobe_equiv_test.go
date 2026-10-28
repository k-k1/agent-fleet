package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The pane probes move behind agents.PaneModeReader and agents.ModalProbe (#2870). Until the
// kind switches go, the capabilities must answer exactly what the switches do.

// One composer footer per kind (the measured strings in each kind's parser comments) plus the
// shapes that must read as "not drawn". Every fixture goes through every kind, so a capability
// wired to the wrong parser differs from the switch.
var paneFixtures = []string{
	"",
	"Loading…",
	"  ⏸ manual mode on · ← for agents",
	"  ⏸ plan mode on (shift+tab to cycle) · ← for agents",
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
	"┃ Plan auto · claude-opus  \n\n tokens",
	"┃ Build auto · gpt-5  \n\n tokens",
	"? for shortcuts                plan · gemini-3",
	"? for shortcuts                gemini-3",
	"esc to cancel                  gemini-3",
	"/ commands · ? help · tab next tab   Auto → gpt-5-mini",
	"◎ Working esc interrupt",
	"→ Add a follow-up\n  Auto\n  ~/repo",
	"→ Plan, search, build anything",
	"ask a question or describe a task ↵",
	"Kiro is working · Type to steer · Ctrl+S to queue",
	"gpt-5.5 high · ~/repos/x",
	"gpt-5.5 high · ~/repos/x   Plan mode (shift+tab to cycle)",
}

func capabilityPaneMode(kind, s string) string {
	a, _ := RegisteredAgent(kind)
	r, ok := a.(agents.PaneModeReader)
	if !ok {
		return ""
	}
	return r.PaneMode(s)
}

func TestPaneModeReaderEqualsTheKindSwitch(t *testing.T) {
	kinds := append(RegisteredKinds(), "", "Claude", " codex", "pi")
	labelled := map[string]bool{}
	for _, k := range kinds {
		for _, s := range paneFixtures {
			want := paneModeSwitch(k, s)
			if got := capabilityPaneMode(k, s); got != want {
				t.Errorf("%q on %q: capability %q, switch %q", k, s, got, want)
			}
			if want != "" {
				labelled[k] = true
			}
		}
	}
	// Each of the seven parsers recognised at least one fixture, so the comparison above
	// exercised every arm.
	for _, k := range []string{session.KindClaude, session.KindOpencode, session.KindAgy, session.KindCopilot,
		session.KindCursor, session.KindKiro, session.KindCodex} {
		if !labelled[k] {
			t.Errorf("no fixture reads as drawn for %s; the comparison does not exercise its parser", k)
		}
	}
}

func TestSendWaitsForComposerEqualsTheKindSwitch(t *testing.T) {
	waits := 0
	for _, k := range append(RegisteredKinds(), "", "Claude", "pi") {
		a, _ := RegisteredAgent(k)
		r, ok := a.(agents.PaneModeReader)
		got := ok && r.SendWaitsForComposer()
		if want := sendWaitsForComposerSwitch(k); got != want {
			t.Errorf("%q: capability waits = %v, switch = %v", k, got, want)
		}
		if got {
			waits++
		}
	}
	if waits != 4 {
		t.Fatalf("%d kinds wait for the composer, want the switch's 4", waits)
	}
}

func TestModalProbeEqualsKindModalProbes(t *testing.T) {
	withTempHome(t)
	for _, k := range append(RegisteredKinds(), "", "Claude", "pi") {
		a, _ := RegisteredAgent(k)
		p, ok := a.(agents.ModalProbe)
		probe, listed := kindModalProbes[k]
		if ok != listed {
			t.Errorf("%q: implements agents.ModalProbe = %v, in kindModalProbes = %v", k, ok, listed)
			continue
		}
		if !ok {
			continue
		}
		m := session.Meta{Name: "probe-" + k, Dir: t.TempDir(), Kind: k}
		if got, want := p.PromptModal(m), probe(m); got != want {
			t.Errorf("%q: PromptModal = %q, kindModalProbes = %q", k, got, want)
		}
	}
}

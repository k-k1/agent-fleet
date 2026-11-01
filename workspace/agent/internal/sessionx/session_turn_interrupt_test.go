package sessionx

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sendKeysLines returns the send-keys lines of the fake tmux log, key names only.
func sendKeysLines(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "send-keys -t %7 "); ok {
			keys = append(keys, rest)
		}
	}
	return keys
}

// The stop key differs per kind (docs/log/133 §1); one Escape for every kind left opencode,
// cursor and copilot running.
func TestHandleSessionTurnInterruptKeysPerKind(t *testing.T) {
	cases := []struct {
		kind     string
		want     []string
		measured bool
	}{
		{session.KindClaude, []string{"Escape"}, true},
		{session.KindCodex, []string{"Escape"}, true},
		{session.KindKiro, []string{"Escape"}, true},
		{session.KindOpencode, []string{"Escape", "Escape"}, true},
		{session.KindCursor, []string{"C-c"}, true},
		{session.KindCopilot, []string{"C-c"}, true},
		{session.KindAgy, []string{"Escape"}, false},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			logPath := fakeTmux(t)
			stubInterruptState(t, "working")
			name := "turn_int_" + c.kind
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: c.kind})
			rec := postTurn(t, name, `{"op":"interrupt"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got := sendKeysLines(t, logPath)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("keys = %v, want %v", got, c.want)
			}
			wantField := `"interrupt_measured":false`
			if c.measured {
				wantField = `"interrupt_measured":true`
			}
			if !strings.Contains(rec.Body.String(), wantField) {
				t.Fatalf("body = %s, want %s", rec.Body.String(), wantField)
			}
		})
	}
}

// In opencode's subagent detail view Escape only navigates: Up goes first, then the double Esc.
func TestTuiInterruptForOpencodeSubagentView(t *testing.T) {
	p := tuiInterruptFor(session.KindOpencode, func() bool { return true })
	if strings.Join(p.keys, ",") != "Up,Escape,Escape" {
		t.Fatalf("keys = %v", p.keys)
	}
	if p.measured {
		t.Fatal("the subagent-view sequence was never measured; it must not claim so")
	}
	if p.gap < 400*time.Millisecond || p.gap >= time.Second {
		t.Fatalf("gap %v must separate the two Esc yet stay inside opencode's ~1 s window", p.gap)
	}
	q := tuiInterruptFor(session.KindClaude, func() bool { t.Fatal("subagent probe is opencode-only"); return false })
	if strings.Join(q.keys, ",") != "Escape" {
		t.Fatalf("claude keys = %v", q.keys)
	}
}

func stubInterruptState(t *testing.T, st string) {
	t.Helper()
	old := interruptLiveState
	interruptLiveState = func(string, session.Meta) string { return st }
	t.Cleanup(func() { interruptLiveState = old })
}

// Ctrl+C on an idle (or unreadable) cursor/copilot can exit the CLI, so nothing is sent.
func TestHandleSessionTurnInterruptCtrlCNeedsWorking(t *testing.T) {
	for _, st := range []string{"idle", "question", ""} {
		for _, kind := range []string{session.KindCursor, session.KindCopilot} {
			t.Run(kind+"/"+st, func(t *testing.T) {
				logPath := fakeTmux(t)
				stubInterruptState(t, st)
				name := "turn_idle_" + kind
				session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: kind})
				rec := postTurn(t, name, `{"op":"interrupt"}`)
				if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_working") {
					t.Fatalf("status = %d, body = %s, want 409 not_working", rec.Code, rec.Body.String())
				}
				if got := sendKeysLines(t, logPath); len(got) != 0 {
					t.Fatalf("keys sent to a non-working CLI: %v", got)
				}
			})
		}
	}
}

// Two overlapping stops on opencode must not interleave: the second click's Esc pair would
// land on the next queued turn. The second is absorbed and sends nothing.
func TestHandleSessionTurnInterruptCoalesces(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "turn_coalesce"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode})

	var wg sync.WaitGroup
	bodies := make([]string, 2)
	start := time.Now()
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 150 * time.Millisecond)
			bodies[i] = postTurn(t, name, `{"op":"interrupt"}`).Body.String()
		}()
	}
	wg.Wait()
	if got := sendKeysLines(t, logPath); strings.Join(got, ",") != "Escape,Escape" {
		t.Fatalf("keys = %v, want exactly one Escape pair", got)
	}
	if time.Since(start) < opencode.DoubleEscGap {
		t.Fatalf("the Esc pair was sent without the %v gap", opencode.DoubleEscGap)
	}
	if !strings.Contains(bodies[1], `"coalesced":true`) || strings.Contains(bodies[0], "coalesced") {
		t.Fatalf("bodies = %v, want only the second coalesced", bodies)
	}
	if _, busy := interruptsInFlight.Load(name); busy {
		t.Fatal("in-flight marker leaked")
	}
}

// The caps probe presses the keys the stop button presses; a copy of its own went stale once
// (plain Escape does not stop copilot) and made the interrupt probe report a false error.
func TestTUIInterruptKeysMatchesStopButton(t *testing.T) {
	for _, kind := range []string{session.KindClaude, session.KindOpencode, session.KindCopilot, session.KindCursor} {
		want := tuiInterruptFor(kind, func() bool { return false })
		keys, gap := TUIInterruptKeys(kind)
		if strings.Join(keys, ",") != strings.Join(want.keys, ",") || gap != want.gap {
			t.Errorf("%s: TUIInterruptKeys = %v/%v, want %v/%v", kind, keys, gap, want.keys, want.gap)
		}
	}
	if keys, _ := TUIInterruptKeys(session.KindCopilot); strings.Join(keys, ",") != "C-c" {
		t.Errorf("copilot stop = %v, want Ctrl+C", keys)
	}
}

// oldTUIInterruptFor is the kind switch tuiInterruptFor held before each kind declared its own
// agents.TUIInterrupter. It is a deliberate literal copy: the lookup must answer exactly what it
// did for every registered kind, for an alias or case variant the exact-match switch never
// matched, and for an unregistered kind.
func oldTUIInterruptFor(kind string, inOpencodeSubagentView func() bool) tuiInterrupt {
	const defaultGap = 90 * time.Millisecond
	switch kind {
	case session.KindClaude, session.KindCodex, session.KindKiro:
		return tuiInterrupt{keys: []string{"Escape"}, gap: defaultGap, measured: true}
	case session.KindOpencode:
		if inOpencodeSubagentView() {
			return tuiInterrupt{keys: []string{"Up", "Escape", "Escape"}, gap: 600 * time.Millisecond}
		}
		return tuiInterrupt{keys: []string{"Escape", "Escape"}, gap: 600 * time.Millisecond, measured: true}
	case session.KindCursor, session.KindCopilot:
		return tuiInterrupt{keys: []string{"C-c"}, gap: defaultGap, measured: true, needsWorking: true}
	default:
		return tuiInterrupt{keys: []string{"Escape"}, gap: defaultGap, measured: false}
	}
}

func TestTUIInterruptForMatchesOldSwitch(t *testing.T) {
	probes := append([]string{"", "Claude", "CODEX", " cursor", "unknown-kind", "claude-code"}, RegisteredKinds()...)
	for _, kind := range probes {
		for _, sub := range []bool{false, true} {
			got := tuiInterruptFor(kind, func() bool { return sub })
			want := oldTUIInterruptFor(kind, func() bool { return sub })
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q (subagent view %v): %+v, old switch %+v", kind, sub, got, want)
			}
		}
	}
}

// A stop that needs a running turn reads the kind's own live-state verdict, and the old switch
// had one for exactly cursor and copilot; a NeedsWorking kind without it would be refused as
// "not working" forever.
func TestInterruptLiveStateCoversNeedsWorkingKinds(t *testing.T) {
	for _, kind := range RegisteredKinds() {
		if !tuiInterruptFor(kind, func() bool { return false }).needsWorking {
			continue
		}
		if kind != session.KindCursor && kind != session.KindCopilot {
			t.Errorf("%s needs a working verdict before its stop key; the old table had cursor and copilot only", kind)
		}
		if liveStateTraits(kind).State == nil {
			t.Errorf("%s: NeedsWorking but LiveStateTraits has no State, its stop would always be refused", kind)
		}
	}
}

// editedUsageLimitAgent must not hide the wrapped kind's stop keys.
func TestUsageLimitWrapperKeepsTUIInterrupt(t *testing.T) {
	want := map[string]tuiInterrupt{}
	for _, kind := range RegisteredKinds() {
		for _, sub := range []bool{false, true} {
			want[fmt.Sprint(kind, sub)] = tuiInterruptFor(kind, func() bool { return sub })
		}
	}
	for _, kind := range RegisteredKinds() {
		editUsageLimitTraits(t, kind, func(*agents.UsageLimitTraits) {})
	}
	for _, kind := range RegisteredKinds() {
		for _, sub := range []bool{false, true} {
			if got := tuiInterruptFor(kind, func() bool { return sub }); !reflect.DeepEqual(got, want[fmt.Sprint(kind, sub)]) {
				t.Errorf("%s (subagent view %v) behind the wrapper: %+v, want %+v", kind, sub, got, want[fmt.Sprint(kind, sub)])
			}
		}
	}
}

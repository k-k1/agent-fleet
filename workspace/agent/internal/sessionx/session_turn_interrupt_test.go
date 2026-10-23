package sessionx

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	if time.Since(start) < opencodeDoubleEscGap {
		t.Fatalf("the Esc pair was sent without the %v gap", opencodeDoubleEscGap)
	}
	if !strings.Contains(bodies[1], `"coalesced":true`) || strings.Contains(bodies[0], "coalesced") {
		t.Fatalf("bodies = %v, want only the second coalesced", bodies)
	}
	if _, busy := interruptsInFlight.Load(name); busy {
		t.Fatal("in-flight marker leaked")
	}
}

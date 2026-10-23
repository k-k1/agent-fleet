package sessionx

import (
	"net/http"
	"os"
	"strings"
	"testing"

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
	if p.gap >= 1000*1e6 {
		t.Fatalf("gap %v must stay inside opencode's ~1 s double-Esc window", p.gap)
	}
	q := tuiInterruptFor(session.KindClaude, func() bool { t.Fatal("subagent probe is opencode-only"); return false })
	if strings.Join(q.keys, ",") != "Escape" {
		t.Fatalf("claude keys = %v", q.keys)
	}
}

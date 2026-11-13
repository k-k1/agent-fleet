package copilot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestTurnMarkAndEndedAfter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COPILOT_HOME", filepath.Join(t.TempDir(), ".copilot"))
	m := session.Meta{Name: "co-mark", Dir: t.TempDir(), Kind: session.KindCopilot}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	p := EventsPath(SessionID(m))
	os.MkdirAll(filepath.Dir(p), 0o700)
	closed := `{"type":"user.message"}` + "\n" + `{"type":"assistant.turn_end"}` + "\n"
	os.WriteFile(p, []byte(closed), 0o600)
	mark, ok := TurnMark(m)
	if !ok {
		t.Fatal("no mark")
	}
	if after, known := EndedAfter(m, mark); after || !known {
		t.Fatalf("the previous turn's end counted: %v %v", after, known)
	}
	// A graceful shutdown closes the turn too (classifyDetail), so it is an end as well.
	os.WriteFile(p, []byte(closed+`{"type":"user.message"}`+"\n"+`{"type":"session.shutdown"}`+"\n"), 0o600)
	if after, known := EndedAfter(m, mark); !after || !known {
		t.Fatalf("the shutdown did not count: %v %v", after, known)
	}
}

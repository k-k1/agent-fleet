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
	// The previous process's shutdown flushes after the baseline, before the new input shows up.
	os.WriteFile(p, []byte(closed+`{"type":"session.shutdown"}`+"\n"), 0o600)
	if after, known := EndedAfter(m, mark); after || !known {
		t.Fatalf("a late previous shutdown counted as the new turn's end: %v %v", after, known)
	}
	// A graceful shutdown closes the turn too (classifyDetail), so it is an end as well.
	os.WriteFile(p, []byte(closed+`{"type":"user.message"}`+"\n"+`{"type":"session.shutdown"}`+"\n"), 0o600)
	if after, known := EndedAfter(m, mark); !after || !known {
		t.Fatalf("the shutdown did not count: %v %v", after, known)
	}
}

func TestInputSeen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COPILOT_HOME", filepath.Join(t.TempDir(), ".copilot"))
	m := session.Meta{Name: "co-seen", Dir: t.TempDir(), Kind: session.KindCopilot}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	p := EventsPath(SessionID(m))
	os.MkdirAll(filepath.Dir(p), 0o700)
	closed := `{"type":"user.message"}` + "\n" + `{"type":"assistant.turn_end"}` + "\n"
	os.WriteFile(p, []byte(closed), 0o600)
	mark, _ := TurnMark(m)
	if seen, known := InputSeen(m, mark); seen || !known {
		t.Fatalf("the previous input counted: %v %v", seen, known)
	}
	os.WriteFile(p, []byte(closed+`{"type":"assistant.turn_end"}`+"\n"), 0o600)
	if seen, known := InputSeen(m, mark); seen || !known {
		t.Fatalf("a late end is not an input: %v %v", seen, known)
	}
	os.WriteFile(p, []byte(closed+`{"type":"user.message"}`+"\n"), 0o600)
	if seen, known := InputSeen(m, mark); !seen || !known {
		t.Fatalf("the new input was missed: %v %v", seen, known)
	}
	os.WriteFile(p, []byte("x\n"), 0o600) // truncated below the mark
	if _, known := InputSeen(m, mark); known {
		t.Fatal("a rotated source was compared")
	}
}

// A failed turn has no turn_end: its session.error is the end, and TurnFailure reads its text from
// the lines after the mark only (an older failure must not taint the next turn).
func TestEndedAfterAndTurnFailureOnSessionError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COPILOT_HOME", filepath.Join(t.TempDir(), ".copilot"))
	m := session.Meta{Name: "co-fail", Dir: t.TempDir(), Kind: session.KindCopilot}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	p := EventsPath(SessionID(m))
	os.MkdirAll(filepath.Dir(p), 0o700)
	const fail = `{"type":"session.error","data":{"message":"Execution failed: Model \"m\" is not available.","errorType":"query"}}` + "\n"
	const notice = `{"type":"session.error","data":{"message":"Model \"m\" from --model flag is not available. Using \"auto\" instead.","errorType":"model"}}` + "\n"
	const user = `{"type":"user.message"}` + "\n"
	os.WriteFile(p, []byte(user+fail), 0o600) // an older failed turn
	mark, _ := TurnMark(m)

	os.WriteFile(p, []byte(user+fail+user+notice), 0o600)
	if after, _ := EndedAfter(m, mark); after {
		t.Fatal("the fallback notice ended the turn")
	}
	if got := TurnFailure(m, mark); got != "" {
		t.Fatalf("fallback notice read as a failure: %q", got)
	}
	os.WriteFile(p, []byte(user+fail+user+notice+fail), 0o600)
	if after, known := EndedAfter(m, mark); !after || !known {
		t.Fatalf("the error did not end the turn: %v %v", after, known)
	}
	if got := TurnFailure(m, mark); got != `Execution failed: Model "m" is not available.` {
		t.Fatalf("failure = %q", got)
	}
	os.WriteFile(p, []byte(user+fail+user+fail+`{"type":"assistant.message"}`+"\n"+`{"type":"assistant.turn_end"}`+"\n"), 0o600)
	if got := TurnFailure(m, mark); got != "" {
		t.Fatalf("an answered turn read as failed: %q", got)
	}
	if got := TurnFailure(m, "bogus"); got != "" {
		t.Fatalf("an unusable mark read as failed: %q", got)
	}
}

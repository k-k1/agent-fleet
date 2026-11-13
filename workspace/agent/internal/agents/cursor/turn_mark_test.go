package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestTurnMarkAndEndedAfter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cu-mark", Dir: t.TempDir(), Kind: session.KindCursor}
	chat := "11111111-1111-4111-8111-111111111112"
	sids.Write(session.UUID(m.Dir, m.Name), chat)
	p := transcriptPath(m.Dir, chat)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	closed := transcriptFixture + `{"type":"turn_ended","status":"success"}` + "\n"
	os.WriteFile(p, []byte(closed), 0o600)
	mark, ok := TurnMark(m)
	if !ok {
		t.Fatal("no mark")
	}
	if after, known := EndedAfter(m, mark); after || !known {
		t.Fatalf("the previous turn's end counted: %v %v", after, known)
	}
	os.WriteFile(p, []byte(closed+`{"type":"turn_ended","status":"success"}`+"\n"), 0o600)
	if after, known := EndedAfter(m, mark); after || !known {
		t.Fatalf("a late previous end counted as the new turn's: %v %v", after, known)
	}
	os.WriteFile(p, []byte(closed+`{"role":"user","message":{}}`+"\n"+`{"type":"turn_ended","status":"success"}`+"\n"), 0o600)
	if after, known := EndedAfter(m, mark); !after || !known {
		t.Fatalf("the new end did not count: %v %v", after, known)
	}
}

package sessionx

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// The previous run's huge tail and end land after the baseline, before the new input is in the
// source. Growth is no evidence of input: the end is denied, however large it is, and a real
// start followed by a huge turn still completes at once.
func TestTurnIdentityLargePreviousTailInventsNoNewInput(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_bigtail")
	turnEnded()
	pre := captureTurnMark(m.Name)
	huge := `{"type":"tool.execution_complete","data":{"result":{"content":"` + strings.Repeat("x", 140*1024) + `"}}}`
	appendEvents(t, m, huge, `{"type":"assistant.turn_end"}`)
	markSessionWorkingFrom(m.Name, pre)
	DriveState(m, true, false)
	recordPolledTurnEnd(m, "idle")
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd || status.ObservedTurnEnd(sid) != "" {
		t.Fatalf("a previous run's big tail completed the new turn: %+v", st)
	}

	// Control: the real input arrives, a huge turn follows, and its end counts at once.
	appendEvents(t, m, `{"type":"user.message"}`, huge, `{"type":"assistant.turn_end"}`)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("a real big turn was not completed: %+v", st)
	}
}

func agyIdentityFixture(t *testing.T, name string) (session.Meta, string, func(steps [][2]int, endIdx int)) {
	t.Helper()
	withTempHome(t)
	m := session.Meta{Name: name, Kind: session.KindAgy, Dir: t.TempDir(), CreatedAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	conv := "conv-" + name
	root := filepath.Join(paths.GeminiHome(), "antigravity-cli")
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "conversations"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{m.Dir: conv})
	if err := os.WriteFile(filepath.Join(root, "cache", "last_conversations.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "conversations", conv+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, step_payload BLOB)`,
		`CREATE TABLE executor_metadata (idx INTEGER, data BLOB)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	// add appends steps (idx, type) and one executor row ending at endIdx.
	add := func(steps [][2]int, endIdx int) {
		t.Helper()
		for _, s := range steps {
			if _, err := db.Exec(`INSERT INTO steps VALUES (?,?,3,x'78')`, s[0], s[1]); err != nil {
				t.Fatal(err)
			}
		}
		if endIdx >= 0 {
			n++
			if _, err := db.Exec(`INSERT INTO executor_metadata VALUES (?,?)`, n, []byte{0x08, 4, 0x10, 1, 0x18, byte(endIdx)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return m, session.UUID(m.Dir, m.Name), add
}

// A healthy DB where the new user step has not landed yet is a denial, not an unknown: the
// settle window must not run out into the previous run's idle being this turn's completion.
func TestTurnIdentityAgyMissingNewInputDoesNotExpireIntoCompletion(t *testing.T) {
	m, sid, add := agyIdentityFixture(t, "agy_ident_missing")
	add([][2]int{{0, 14}, {1, 15}}, 1) // the previous turn: user, model, run ended at 1
	pre := captureTurnMark(m.Name)
	if !pre.ok {
		t.Fatal("control: no baseline")
	}
	markSessionWorkingFrom(m.Name, pre)

	polledEndSettle = polledEndSettleDefault
	now := time.Now().Add(30 * time.Second) // past the window
	polledEndNow = func() time.Time { return now }
	defer func() { polledEndSettle, polledEndNow = 0, time.Now }()
	if got := DriveState(m, true, false); got != "idle" {
		t.Fatalf("control: the source should read idle, got %q", got)
	}
	recordPolledTurnEnd(m, "idle")
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd || status.ObservedTurnEnd(sid) != "" {
		t.Fatalf("the previous run's idle became this turn's completion: %+v", st)
	}

	// Control: the new input lands and its run ends.
	add([][2]int{{2, 14}, {3, 15}}, 3)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("the real end was not recorded: %+v", st)
	}
}

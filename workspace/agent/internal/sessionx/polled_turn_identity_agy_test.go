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

// agyFakePane puts a tmux on PATH that answers list-panes and capture-pane with the frame
// file's content, so the agy pane reads see a refused turn without a terminal.
func agyFakePane(t *testing.T, frame string) { agyFakePaneSlow(t, frame, "") }

// agyFakePaneSlow answers capture-pane after the given shell sleep, e.g. "sleep 1; ".
func agyFakePaneSlow(t *testing.T, frame, sleepFor string) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "frame")
	if err := os.WriteFile(f, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) echo '1 %1';;\ncapture-pane) " + sleepFor + "cat " + f + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const agyQuotaFrame = "partial answer\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h39m46s.\n─────\n> \n─────\n? for shortcuts      Gemini · high\n"

// A first input (no turn mark yet) that ends on the quota refusal is recorded as a failed turn,
// not a completion; the same end with no banner on screen is still a completion.
func TestPolledTurnEndAgyQuotaRefusalWithoutMarkFails(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		reason      string
	}{
		{"refused", agyQuotaFrame, status.TurnEndReasonFailed},
		{"clean", "done\n─────\n> \n─────\n? for shortcuts      Gemini · high\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, sid, add := agyIdentityFixture(t, "agy_quota_"+tc.name)
			add([][2]int{{0, 14}, {1, 15}}, 1)
			agyFakePane(t, tc.frame)
			status.Persist(sid, "working") // delivered without a turn mark
			if _, ok := status.TurnMarkOf(sid); ok {
				t.Fatal("control: the turn has a mark")
			}
			notifyPolledTurnEnd(m, "idle")
			rec, _ := status.Read(sid)
			if rec.State != "idle" || rec.TurnEndReason != tc.reason {
				t.Fatalf("state=%q reason=%q, want idle/%q", rec.State, rec.TurnEndReason, tc.reason)
			}
		})
	}
}

// An unpainted pane right after the DB says idle holds the completion back (no clean end is
// stored that a refusal drawn a moment later could not undo); once the frame is there, the
// refusal is recorded as a failure.
func TestPolledTurnEndAgyWaitsForThePaneThenFails(t *testing.T) {
	m, sid, add := agyIdentityFixture(t, "agy_quota_pending")
	add([][2]int{{0, 14}, {1, 15}}, 1)
	agyFakePane(t, "partial\n  esc to cancel      Gemini · high\n")
	status.Persist(sid, "working")
	notifyPolledTurnEnd(m, "idle")
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("a completion was stored before the pane confirmed the end: %+v", st)
	}
	agyFakePane(t, agyQuotaFrame)
	notifyPolledTurnEnd(m, "idle")
	if rec, _ := status.Read(sid); rec.State != "idle" || rec.TurnEndReason != status.TurnEndReasonFailed {
		t.Fatalf("state=%q reason=%q, want idle/failed", rec.State, rec.TurnEndReason)
	}
}

// The verdict read from the pane belongs to the record read before it: the next turn's input
// landing while tmux answers must not be turned into an idle/completed state by the old verdict.
func TestPolledTurnEndAgyVerdictDoesNotOverwriteTheNextTurn(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"refused", agyQuotaFrame},
		{"clean", "done\n─────\n> \n─────\n? for shortcuts      Gemini · high\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, sid, add := agyIdentityFixture(t, "agy_quota_race_"+tc.name)
			add([][2]int{{0, 14}, {1, 15}}, 1)
			agyFakePaneSlow(t, tc.frame, "sleep 1; ")
			status.Persist(sid, "working")
			done := make(chan struct{})
			go func() { notifyPolledTurnEnd(m, "idle"); close(done) }()
			time.Sleep(300 * time.Millisecond)
			status.Persist(sid, "working") // the next turn's input, a new record
			<-done
			if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
				t.Fatalf("the old verdict overwrote the next turn: %+v", st)
			}
		})
	}
}

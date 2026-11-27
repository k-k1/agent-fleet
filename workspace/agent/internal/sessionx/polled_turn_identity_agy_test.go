package sessionx

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
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

func TestTurnIdentityAgyQueueListReportsExecutorFailureWithoutDriveState(t *testing.T) {
	m, sid, add := agyIdentityFixture(t, "agy_ident_failure")
	m.QueueDelivery = "delivery1"
	session.WriteMeta(m)
	add([][2]int{{0, 14}, {1, 15}}, 1)
	markSessionWorkingFrom(m.Name, captureTurnMark(m.Name))
	add([][2]int{{2, 14}, {3, 21}, {4, 17}}, 4)

	dbPath := filepath.Join(paths.GeminiHome(), "antigravity-cli", "conversations", "conv-"+m.Name+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	message := "expected pending tool step at index 3, got status CORTEX_STEP_STATUS_CANCELED"
	details := append(binary.AppendUvarint([]byte{0x12}, uint64(len(message))), message...)
	payload := append(binary.AppendUvarint([]byte{0x1a}, uint64(len(details))), details...)
	if _, err := db.Exec(`UPDATE steps SET step_payload=? WHERE idx=4`, payload); err != nil {
		t.Fatal(err)
	}

	got := wireSession(m, true)
	if got.LastTurnEndAt == "" || got.LastTurnEndReason != status.TurnEndReasonFailed {
		t.Fatalf("end = %q, reason = %q", got.LastTurnEndAt, got.LastTurnEndReason)
	}
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("queue listing consumed the notification gate: %+v", st)
	}

	markSessionWorkingFrom(m.Name, captureTurnMark(m.Name))
	got = wireSession(m, true)
	if got.LastTurnEndAt != "" || got.LastTurnEndReason != "" {
		t.Fatalf("new input reused failed turn: end = %q, reason = %q", got.LastTurnEndAt, got.LastTurnEndReason)
	}
	add([][2]int{{5, 14}, {6, 15}}, 6)
	got = wireSession(m, true)
	if got.LastTurnEndAt == "" || got.LastTurnEndReason != "" {
		t.Fatalf("new clean turn: end = %q, reason = %q", got.LastTurnEndAt, got.LastTurnEndReason)
	}
}

func TestAgyQueueFailureNotifiesAfterEveryFailureWithItsObservedText(t *testing.T) {
	m, sid, add := agyIdentityFixture(t, "agy_queue_failure_notice")
	m.QueueDelivery = "delivery1"
	session.WriteMeta(m)
	add([][2]int{{0, 14}, {1, 15}}, 1)
	dbPath := filepath.Join(paths.GeminiHome(), "antigravity-cli", "conversations", "conv-"+m.Name+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	notified := make(chan struct{}, 2)
	agents.SetStateNotifierWithTurnEnd(func(sid, prev, state, text, at string) {
		RecordSessionNotificationWithTurnEnd(sid, prev, state, text, at)
		notified <- struct{}{}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	const message = "expected pending tool step at index 3, got status CORTEX_STEP_STATUS_CANCELED"
	details := append(binary.AppendUvarint([]byte{0x12}, uint64(len(message))), message...)
	payload := append(binary.AppendUvarint([]byte{0x1a}, uint64(len(details))), details...)
	for i, last := range []int{4, 7} {
		markSessionWorkingFrom(m.Name, captureTurnMark(m.Name))
		add([][2]int{{last - 2, 14}, {last - 1, 21}, {last, 17}}, last)
		if _, err := db.Exec(`UPDATE steps SET step_payload=? WHERE idx=?`, payload, last); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			// This manual continuation fails too. Simulate a later read that
			// can still see the executor boundary but cannot read the error.
			list := wireSession(m, true)
			if list.LastTurnEndReason != status.TurnEndReasonFailed {
				t.Fatalf("list missed failure before source changed: %+v", list)
			}
			if _, err := db.Exec(`UPDATE steps SET step_payload=x'' WHERE idx=?`, last); err != nil {
				t.Fatal(err)
			}
		}

		if state := DriveState(m, true, false); state != "idle" {
			t.Fatalf("failure left state %q", state)
		}
		select {
		case <-notified:
		case <-time.After(3 * time.Second):
			t.Fatal("failure was not notified")
		}
		at, reason := status.LatestTurnEnd(sid)
		if at == "" || reason != status.TurnEndReasonFailed {
			t.Fatalf("end = %q, reason = %q", at, reason)
		}
		events := notice.List()
		if len(events) != i+1 {
			t.Fatalf("notifications after step %d = %+v", last, events)
		}
		var found bool
		for _, event := range events {
			if event.ID == "turn-failed:"+m.Name+":"+at {
				found = event.Kind == "turn-failed" && event.Payload["body"] == message
			}
		}
		if !found {
			t.Fatalf("turn failure absent for %q in %+v", at, events)
		}
	}
}

// agyFakePane puts a tmux on PATH that answers list-panes and capture-pane with the frame, so the
// agy pane reads see a refused turn without a terminal.
func agyFakePane(t *testing.T, frame string) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "frame")
	if err := os.WriteFile(f, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) echo '1 %1';;\ncapture-pane) cat " + f + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const agyQuotaFrame = "partial answer\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h39m46s.\n─────\n> \n─────\n? for shortcuts      Gemini · high\n"

// A queue run whose agy turn was quota-refused reads limited in the list BEFORE the sweep opened an
// episode, and the same session reads limited after it: the queue closes an idle run as finished
// and waits on a limited one, so the answer must not depend on which came first. A clean turn, and
// a session that is not a queue run, stay idle.
func TestWireSessionAgyQueueRunQuotaRefusalReadsLimitedBeforeTheSweep(t *testing.T) {
	for _, tc := range []struct {
		name, frame, delivery, want string
	}{
		{"refused queue run", agyQuotaFrame, "d1", agents.StateLimited},
		{"clean queue run", "done\n─────\n> \n─────\n? for shortcuts      Gemini · high\n", "d1", "idle"},
		{"refused, not a queue run", agyQuotaFrame, "", "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, sid, add := agyIdentityFixture(t, "agy_quota_list_"+strings.ReplaceAll(tc.name, " ", "_"))
			m.QueueDelivery = tc.delivery
			session.WriteMeta(m)
			add([][2]int{{0, 14}, {1, 15}}, 1)
			status.Persist(sid, "working")
			agyFakePane(t, tc.frame)
			if got := wireSession(m, true).State; got != tc.want {
				t.Fatalf("state %q, want %q", got, tc.want)
			}
		})
	}
}

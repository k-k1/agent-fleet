package agy

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mkConvDB writes a minimal conversations/<conv>.db with the columns Probe
// reads (the real table has more; the query names only these three).
func mkConvDB(t *testing.T, conv string, rows [][3]any) {
	t.Helper()
	dir := filepath.Join(stateDir(), "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, conv+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if _, err := db.Exec(`INSERT INTO steps (idx, step_type, status, step_payload) VALUES (?,?,?,?)`, i, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
}

// 実機の step_payload を模した fixture: protobuf のワイヤバイト列の中に
// ask_question のツール引数 JSON が平文で埋まっている。
func questionPayload() []byte {
	return append(append([]byte("\x0a\x08s8twu8rq\x12\x0cask_question\xaa\x01"),
		[]byte(`{"questions":[{"is_multi_select":false,"options":["Mountain (M)","Sea (S)"],"question":"Which do you prefer?"}],"toolAction":"Asking preference"}`)...),
		[]byte("\x1a\x24e0e6e5ff-ca63-407e-8b10-159429b392")...)
}

func TestProbePendingQuestion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot20", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot20"), "conv-q")
	mkConvDB(t, "conv-q", [][3]any{
		{14, 3, []byte("user")},
		{138, stepStatusAwaitingUser, questionPayload()},
	})
	st, qs := Probe(m)
	if st != "question" {
		t.Fatalf("state=%q want question", st)
	}
	if len(qs) != 1 || qs[0].Question != "Which do you prefer?" || qs[0].MultiSelect {
		t.Fatalf("questions wrong: %+v", qs)
	}
	if len(qs[0].Options) != 2 || qs[0].Options[0].Label != "Mountain (M)" || qs[0].Options[1].Label != "Sea (S)" {
		t.Fatalf("options wrong: %+v", qs[0].Options)
	}
}

func TestProbePendingPermissionCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot21", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot21"), "conv-p")
	// run_command awaiting permission: status=9, tool name + args JSON in the
	// payload → the synthesized 4-row menu (mirrors the TUI's rows exactly).
	mkConvDB(t, "conv-p", [][3]any{
		{14, 3, []byte("user")},
		{21, stepStatusAwaitingUser, []byte("\x12\x0brun_command\xaa\x01" + `{"CommandLine":"rtk echo x","Cwd":"/tmp"}` + "\x1a")},
	})
	st, qs := Probe(m)
	if st != "permission" || len(qs) != 1 {
		t.Fatalf("got %q %+v; want permission with 1 synthesized question", st, qs)
	}
	if len(qs[0].Options) != 4 || qs[0].Options[3].Label != "No" {
		t.Fatalf("command menu must have the TUI's 4 rows: %+v", qs[0].Options)
	}
	if qs[0].Question != "Requesting permission for: rtk echo x" {
		t.Fatalf("question text wrong: %q", qs[0].Question)
	}
}

func TestProbePendingPermissionFileTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	for name, tc := range map[string]struct {
		tool  string
		nOpts int
	}{
		"create": {"write_to_file", 2},
		"edit":   {"replace_file_content", 2},
	} {
		m := session.Meta{Dir: dir, Name: "slot-" + name, Kind: session.KindAgy}
		sids.Write(session.UUID(dir, "slot-"+name), "conv-"+name)
		mkConvDB(t, "conv-"+name, [][3]any{
			{5, stepStatusAwaitingUser, []byte(tc.tool + `*{"TargetFile":"/tmp/x.txt","CodeContent":"y"}`)},
		})
		st, qs := Probe(m)
		if st != "permission" || len(qs) != 1 || len(qs[0].Options) != tc.nOpts {
			t.Fatalf("%s: got %q %+v; want permission with %d rows", name, st, qs, tc.nOpts)
		}
	}
}

func TestProbePendingPermissionUnknownToolNoCard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot24", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot24"), "conv-u")
	// 未検証ツールのメニュー形は不明 — カードを出すと Down×i が誤爆し得るので
	// state のみ（応答はターミナルで）。
	mkConvDB(t, "conv-u", [][3]any{
		{99, stepStatusAwaitingUser, []byte(`mystery_tool*{"Thing":"z"}`)},
	})
	st, qs := Probe(m)
	if st != "permission" || qs != nil {
		t.Fatalf("got %q %+v; want permission with NO card for unknown tool", st, qs)
	}
}

func TestProbeIdleAndRunningAreNotPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot22", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot22"), "conv-r")
	// Last step running (status=2): a tool is executing, nothing awaits the user.
	mkConvDB(t, "conv-r", [][3]any{
		{14, 3, []byte("user")},
		{21, 2, []byte(`{"CommandLine":"sleep 8"}`)},
	})
	if st, _ := Probe(m); st != "" {
		t.Fatalf("running step misread as pending: %q", st)
	}
}

func TestProbeNoConversationOrDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Dir: "/d", Name: "slot23", Kind: session.KindAgy}
	if st, _ := Probe(m); st != "" {
		t.Fatalf("no-sid probe returned %q", st)
	}
	sids.Write(session.UUID("/d", "slot23"), "conv-missing")
	if st, _ := Probe(m); st != "" {
		t.Fatalf("missing-db probe returned %q", st)
	}
}

// LiveState は Probe と違い「保留でない」も状態として返す必要がある。agy は
// status hook を持たないため、この idle 判定が唯一の turn 終端シグナルで、
// これが無いと /input の楽観 working が消えず、オペレータへの完了報告の arm
// が永久に消費されない（docs/log/30 ②）。
func TestLiveStateClassifiesEveryStepStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"done", stepStatusDone, "idle"},
		{"running", stepStatusRunning, "working"},
		{"awaiting", stepStatusAwaitingUser, "permission"},
		{"unknown", 7, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := "slot-ls-" + tc.name
			m := session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy}
			sids.Write(session.UUID(dir, slot), "conv-ls-"+tc.name)
			mkConvDB(t, "conv-ls-"+tc.name, [][3]any{
				{14, 3, []byte("user")},
				{21, tc.status, []byte(`{"CommandLine":"run_command x"}`)},
			})
			if got := LiveState(m); got != tc.want {
				t.Fatalf("status=%d: got %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// 会話未採用 / DB 不在では「意見なし」("")。停止済みセッションを誤って idle と
// 報告し、偽の完了報告を撃たないための境界。
func TestLiveStateNoOpinionWithoutDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Dir: "/d", Name: "slot-ls-none", Kind: session.KindAgy}
	if got := LiveState(m); got != "" {
		t.Fatalf("no-sid LiveState returned %q", got)
	}
	sids.Write(session.UUID("/d", "slot-ls-none"), "conv-ls-missing")
	if got := LiveState(m); got != "" {
		t.Fatalf("missing-db LiveState returned %q", got)
	}
}

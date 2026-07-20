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

func TestProbePendingPermission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot21", Kind: session.KindAgy}
	sids.Write(session.UUID(dir, "slot21"), "conv-p")
	// run_command awaiting permission: status=9, no questions JSON in the payload.
	mkConvDB(t, "conv-p", [][3]any{
		{14, 3, []byte("user")},
		{21, stepStatusAwaitingUser, []byte(`{"CommandLine":"rtk echo x","Cwd":"/tmp"}`)},
	})
	st, qs := Probe(m)
	if st != "permission" || qs != nil {
		t.Fatalf("got %q %+v; want permission with no questions", st, qs)
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

package agy

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
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

// A fixture shaped like a real step_payload: the ask_question tool's argument JSON sits in
// plain text inside the protobuf wire bytes.
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
	// The menu shape of an unverified tool is unknown: showing a card could fire the wrong
	// Down x i, so report the state only and let the answer happen in the terminal.
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

// Unlike Probe, LiveState must also report "not pending" as a state. agy has no status
// hook, so this idle verdict is the only end-of-turn signal there is: without it /input's
// optimistic working never clears and the arm for the completion report to the operator is
// never consumed (docs/log/30 ②).
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

// With no conversation adopted, or no DB, the answer is "no opinion" (""). That boundary
// keeps a stopped session from being reported as idle and firing a false completion
// report.
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

// The sessions list reads WireLive, not DriveState. It must carry the same working / idle
// verdict: an empty State mid-turn is drawn by the Console as waiting for input. A dead
// session reports nothing, since its DB keeps the last status it had.
func TestWireLiveSurfacesWorkingAndIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	for _, tc := range []struct {
		name   string
		status int
		alive  bool
		want   string
	}{
		{"running", stepStatusRunning, true, "working"},
		{"done", stepStatusDone, true, "idle"},
		{"dead", stepStatusRunning, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := "slot-wl-" + tc.name
			m := session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy}
			sids.Write(session.UUID(dir, slot), "conv-wl-"+tc.name)
			mkConvDB(t, "conv-wl-"+tc.name, [][3]any{
				{14, 3, []byte("user")},
				{132, tc.status, []byte("view_file")},
			})
			if got := (agentImpl{}).WireLive(m, tc.alive).State; got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// executorRow builds an executor_metadata.data blob shaped like v1.2.14's: varint fields 1
// (how the run ended) and 2 before the step idx in field 3, a length-delimited field after
// it. Field 3 coming third makes the walker skip the others the way it must on real rows.
func executorRow(reason, endIdx int) []byte {
	b := []byte{0x08, byte(reason), 0x10, 0x01}
	b = binary.AppendUvarint(append(b, 0x18), uint64(endIdx))
	return append(append(b, 0x4a, 0x04), "uuid"...)
}

// mkConvDBWithTurns is mkConvDB plus the executor_metadata table, one row per finished turn.
func mkConvDBWithTurns(t *testing.T, conv string, rows [][3]any, turns [][]byte) {
	t.Helper()
	mkConvDB(t, conv, rows)
	db, err := sql.Open("sqlite", conversationDBPath(conv))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE executor_metadata (idx INTEGER, data BLOB)`); err != nil {
		t.Fatal(err)
	}
	for i, d := range turns {
		if _, err := db.Exec(`INSERT INTO executor_metadata (idx, data) VALUES (?,?)`, i, d); err != nil {
			t.Fatal(err)
		}
	}
}

// The step sequences below were recorded on v1.2.14 by polling a live conversation DB. The
// model's step row appears only once its response is complete, so mid-turn the last row is
// routinely a finished user / tool / notification step; only an executor_metadata row that
// has reached the last step ends the turn.
func TestLiveStateFollowsTurnEndNotLastStepStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	const (
		user, model, tool, notice = 14, 15, 132, 101
		completed, canceled       = 4, 2
	)
	done := func(types ...int) [][3]any {
		var out [][3]any
		for _, ty := range types {
			out = append(out, [3]any{ty, stepStatusDone, []byte("x")})
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		rows  [][3]any
		turns [][]byte
		want  string
	}{
		{"thinking after the prompt", done(user), nil, "working"},
		{"thinking after a tool", done(user, model, tool), nil, "working"},
		{"background task finished", done(user, model, tool, model, notice), nil, "working"},
		{"text reply while a background task runs", done(user, model, tool, model), nil, "working"},
		{"model streaming", append(done(user, model, tool), [3]any{model, stepStatusStreaming, []byte("x")}), nil, "working"},
		{"turn completed", done(user, model, tool, model), [][]byte{executorRow(completed, 3)}, "idle"},
		{"next prompt after a completed turn", done(user, model, user), [][]byte{executorRow(completed, 1)}, "working"},
		{"Esc while thinking", done(user, model, user), [][]byte{executorRow(completed, 1), executorRow(canceled, 2)}, "idle"},
		{"Esc during a tool", append(done(user, model), [3]any{tool, stepStatusCanceled, []byte("x")}), [][]byte{executorRow(canceled, 2)}, "idle"},
		{"background task restarts an ended turn", done(user, model, tool, model, notice), [][]byte{executorRow(completed, 3)}, "working"},
		{"permission still wins", append(done(user, model), [3]any{tool, stepStatusAwaitingUser, []byte("run_command")}), nil, "permission"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := "slot-te-" + tc.name
			conv := "conv-te-" + strings.ReplaceAll(tc.name, " ", "-")
			m := session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy}
			sids.Write(session.UUID(dir, slot), conv)
			mkConvDBWithTurns(t, conv, tc.rows, tc.turns)
			if got := LiveState(m); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProtoVarintFieldRejectsTruncatedInput(t *testing.T) {
	row := executorRow(4, 300)
	if v, found, ok := protoVarintField(row, 3); !ok || !found || v != 300 {
		t.Fatalf("got %d %v %v, want 300", v, found, ok)
	}
	if _, found, ok := protoVarintField(row, 7); !ok || found {
		t.Fatalf("absent field: found=%v ok=%v, want found=false ok=true", found, ok)
	}
	// Cut inside field 3's two-byte varint, and inside a length-delimited field: neither may
	// read past the end or pass for a well-formed message.
	if _, _, ok := protoVarintField(row[:6], 3); ok {
		t.Fatal("truncated varint accepted")
	}
	if _, _, ok := protoVarintField([]byte{0x4a, 0x09, 'a'}, 3); ok {
		t.Fatal("overlong length-delimited field accepted")
	}
	// Field number 0 and 2^29 are not legal tags, wherever they sit. Each carries a value, so
	// only the field-number check can reject it.
	for name, b := range map[string][]byte{
		"zero tag":          {0x00, 0x00},
		"zero tag after f3": {0x18, 0x00, 0x00, 0x00},
		"field number 2^29": append(binary.AppendUvarint(nil, 1<<29<<3), 0x00),
	} {
		if _, _, ok := protoVarintField(b, 3); ok {
			t.Fatalf("%s accepted", name)
		}
	}
	// Malformed AFTER field 3 still fails: the value must not come from a broken message.
	if _, _, ok := protoVarintField(append(append([]byte(nil), row...), 0x4a, 0x09), 3); ok {
		t.Fatal("trailing garbage accepted")
	}
}

// A DB that has executor_metadata must never fall back to the last step's status: mid-turn that
// status is "done", so an unreadable turn log has to be "no opinion", not idle.
func TestLiveStateTurnLogClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	midTurn := [][3]any{{14, stepStatusDone, []byte("x")}, {15, stepStatusDone, []byte("x")}, {132, stepStatusDone, []byte("x")}}
	for _, tc := range []struct {
		name  string
		rows  [][3]any
		setup func(*sql.DB) error
		want  string
	}{
		{"no table: legacy status rule", midTurn, nil, "idle"},
		{"no rows yet", midTurn, func(db *sql.DB) error {
			_, err := db.Exec(`CREATE TABLE executor_metadata (idx INTEGER, data BLOB)`)
			return err
		}, "working"},
		{"table without data column", midTurn, func(db *sql.DB) error {
			_, err := db.Exec(`CREATE TABLE executor_metadata (idx INTEGER, other BLOB)`)
			return err
		}, ""},
		{"NULL data", midTurn, insertTurn(nil), ""},
		{"malformed data", midTurn, insertTurn([]byte{0x4a, 0x09, 'a'}), ""},
		{"idx beyond int32", midTurn, insertTurn(executorRow(4, 1<<40)), ""},
		{"illegal field number", [][3]any{{14, stepStatusDone, []byte("x")}}, insertTurn([]byte{0x00, 0x00}), ""},
		// Measured on v1.2.14: the first prompt canceled before any reply writes field 1=2,
		// field 2=1 and no field 3, because proto3 omits the zero.
		{"first prompt canceled: field 3 omitted", [][3]any{{14, stepStatusDone, []byte("x")}}, insertTurn([]byte{0x08, 0x02, 0x10, 0x01}), "idle"},
		{"field 3 omitted but turn moved on", midTurn, insertTurn([]byte{0x08, 0x02, 0x10, 0x01}), "working"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := "slot-tl-" + tc.name
			conv := "conv-tl-" + strings.NewReplacer(" ", "-", ":", "").Replace(tc.name)
			m := session.Meta{Dir: dir, Name: slot, Kind: session.KindAgy}
			sids.Write(session.UUID(dir, slot), conv)
			mkConvDB(t, conv, tc.rows)
			if tc.setup != nil {
				db, err := sql.Open("sqlite", conversationDBPath(conv))
				if err != nil {
					t.Fatal(err)
				}
				if err := tc.setup(db); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			if got := LiveState(m); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func insertTurn(data []byte) func(*sql.DB) error {
	return func(db *sql.DB) error {
		if _, err := db.Exec(`CREATE TABLE executor_metadata (idx INTEGER, data BLOB)`); err != nil {
			return err
		}
		_, err := db.Exec(`INSERT INTO executor_metadata (idx, data) VALUES (0, ?)`, data)
		return err
	}
}

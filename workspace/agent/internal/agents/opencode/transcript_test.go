package opencode

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// newOpencodeTestDB builds a throwaway opencode-shaped SQLite db (message + part) and
// returns an open handle. Mirrors the real schema's relevant columns.
func newOpencodeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insMsg(t *testing.T, db *sql.DB, id, ses string, tc int, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES(?,?,?,?,?)`, id, ses, tc, tc, data); err != nil {
		t.Fatal(err)
	}
}
func insPart(t *testing.T, db *sql.DB, id, mid, ses string, tc int, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO part(id,message_id,session_id,time_created,data) VALUES(?,?,?,?,?)`, id, mid, ses, tc, data); err != nil {
		t.Fatal(err)
	}
}

// renderSession turns the fixture's message/part rows of ses into turns through parseMessage, the
// renderer the opencode 2 reader feeds the same message/part shape to (v2SessionTurns). It is the
// fixture's harness, not a reader: messages in creation order, each with its parts in order.
func renderSession(t *testing.T, db *sql.DB, ses string) []transcript.Turn {
	t.Helper()
	rows, err := db.Query(`SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created, id`, ses)
	if err != nil {
		t.Fatal(err)
	}
	type msg struct {
		id   string
		data []byte
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.data); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	win := modelWindowLookup()
	var turns []transcript.Turn
	for i, m := range msgs {
		var parts [][]byte
		pr, err := db.Query(`SELECT data FROM part WHERE message_id = ? ORDER BY time_created, id`, m.id)
		if err != nil {
			t.Fatal(err)
		}
		for pr.Next() {
			var d []byte
			if err := pr.Scan(&d); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, d)
		}
		pr.Close()
		if turn, ok := parseMessage(m.id, m.data, parts, i, win); ok {
			turns = append(turns, turn)
		}
	}
	return turns
}

func TestOpencodeReadSession(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_x"

	// m1: user prompt.
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000},"path":{"cwd":"/home/dev/repos/x"}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"hello opencode"}`)

	// m2: assistant with framing (dropped), a tool trace, and final text.
	insMsg(t, db, "m2", ses, 2000, `{"role":"assistant","modelID":"deepseek-v4-pro","variant":"max","tokens":{"input":100,"output":20,"cache":{"read":80,"write":5}},"time":{"created":2000,"completed":9000}}`)
	insPart(t, db, "p2a", "m2", ses, 1, `{"type":"step-start"}`)
	insPart(t, db, "p2b", "m2", ses, 2, `{"type":"reasoning","text":"thinking..."}`)
	insPart(t, db, "p2c", "m2", ses, 3, `{"type":"tool","tool":"bash","state":{"input":{"command":"ls -la"},"output":"a\nb"}}`)
	insPart(t, db, "p2e", "m2", ses, 4, `{"type":"tool","tool":"write","state":{"input":{"filePath":"/x/f.txt","content":"hello"}}}`)
	insPart(t, db, "p2d", "m2", ses, 5, `{"type":"text","text":"done"}`)

	// m3: assistant with ONLY framing (step-start/finish) — no displayable part, dropped.
	insMsg(t, db, "m3", ses, 3000, `{"role":"assistant","modelID":"deepseek-v4-pro"}`)
	insPart(t, db, "p3a", "m3", ses, 1, `{"type":"step-start"}`)
	insPart(t, db, "p3b", "m3", ses, 2, `{"type":"step-finish"}`)

	// A message from ANOTHER session must not leak in.
	insMsg(t, db, "z1", "ses_other", 1500, `{"role":"user"}`)
	insPart(t, db, "pz", "z1", "ses_other", 1, `{"type":"text","text":"other session"}`)

	turns := renderSession(t, db, ses)
	if len(turns) != 2 {
		t.Fatalf("want 2 turns (m3 dropped, other session excluded), got %d: %+v", len(turns), turns)
	}

	if turns[0].Role != "user" || turns[0].Text != "hello opencode" || turns[0].Cwd != "/home/dev/repos/x" {
		t.Fatalf("turn0 = %+v", turns[0])
	}
	if turns[0].TS == "" {
		t.Fatalf("turn0 timestamp empty, want RFC3339 from time.created")
	}

	a := turns[1]
	if a.Role != "assistant" || a.Model != "deepseek-v4-pro" {
		t.Fatalf("turn1 role/model = %q/%q", a.Role, a.Model)
	}
	if a.Effort != "max" {
		t.Fatalf("turn1 effort = %q, want max (variant)", a.Effort)
	}
	// One opencode message is a whole turn, so created is its START. The mirror footer
	// wants the end — without EndTS a 7s tool-running turn is stamped 7s early (and a
	// long one, hours early).
	if want := time.UnixMilli(2000).UTC().Format(time.RFC3339); a.TS != want {
		t.Fatalf("turn1 TS = %q, want %q (time.created)", a.TS, want)
	}
	if want := time.UnixMilli(9000).UTC().Format(time.RFC3339); a.EndTS != want {
		t.Fatalf("turn1 EndTS = %q, want %q (time.completed)", a.EndTS, want)
	}
	if a.InTok != 100 || a.OutTok != 20 || a.CacheRead != 80 || a.CacheCreate != 5 {
		t.Fatalf("turn1 usage = %d/%d/%d/%d, want 100/20/80/5", a.InTok, a.OutTok, a.CacheRead, a.CacheCreate)
	}
	// Parts: thinking, tool(bash w/ output), tool(write w/ diff), text; step-* dropped.
	if len(a.Parts) != 4 {
		t.Fatalf("turn1 parts = %d, want 4: %+v", len(a.Parts), a.Parts)
	}
	if a.Parts[0].Kind != "thinking" || a.Parts[0].Text != "thinking..." {
		t.Fatalf("turn1 part0 = %+v, want thinking 'thinking...'", a.Parts[0])
	}
	if a.Parts[1].Kind != "tool" || a.Parts[1].Tool != "bash" || a.Parts[1].Info != "ls -la" || a.Parts[1].Output != "a\nb" {
		t.Fatalf("turn1 part1 = %+v, want tool bash 'ls -la' output 'a\\nb'", a.Parts[1])
	}
	if a.Parts[2].Kind != "tool" || a.Parts[2].Tool != "write" || a.Parts[2].File != "/x/f.txt" ||
		len(a.Parts[2].Edits) != 1 || a.Parts[2].Edits[0].New != "hello" {
		t.Fatalf("turn1 part2 = %+v, want write diff of /x/f.txt", a.Parts[2])
	}
	if a.Parts[3].Kind != "text" || a.Parts[3].Text != "done" || a.Text != "done" {
		t.Fatalf("turn1 part3/text = %+v / %q", a.Parts[3], a.Text)
	}
	// m3 dropped but consumed a message ordinal — the assistant turn keeps ordinal 1.
	if a.Idx != 1 {
		t.Fatalf("turn1 idx = %d, want 1", a.Idx)
	}
}

// A turn still running has no time.completed. EndTS must stay empty rather than take
// some stand-in, so the Console can fall back to the start instead of showing a time
// the turn has not reached yet.
func TestOpencodeEndTSOmittedWhileRunning(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_run"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","modelID":"m","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"working"}`)

	turns := renderSession(t, db, ses)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	if turns[0].TS == "" {
		t.Fatalf("TS empty, want time.created")
	}
	if turns[0].EndTS != "" {
		t.Fatalf("EndTS = %q, want empty while the turn runs", turns[0].EndTS)
	}
}

func TestOpencodeQuestions(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_q"
	// A message with a completed question (shown as an answered block) and a running one
	// (surfaced as the pending question).
	insMsg(t, db, "mq", ses, 1000, `{"role":"assistant","modelID":"m"}`)
	insPart(t, db, "pqa", "mq", ses, 1, `{"type":"tool","tool":"question","state":{"status":"completed","input":{"questions":[{"header":"H","question":"pick?","options":[{"label":"A"},{"label":"B"}]}]},"output":"User has answered your questions: \"pick?\"=\"B\". You may continue."}}`)
	insPart(t, db, "pqb", "mq", ses, 2, `{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"header":"H2","question":"next?","options":[{"label":"X"},{"label":"Y"}]}]}}}`)

	turns := renderSession(t, db, ses)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d", len(turns))
	}
	// Only the completed question renders as a part (the running one is pending, skipped).
	if len(turns[0].Parts) != 1 || turns[0].Parts[0].Kind != "question" {
		t.Fatalf("parts = %+v, want a single answered question part", turns[0].Parts)
	}
	q := turns[0].Parts[0]
	if len(q.Questions) != 1 || q.Questions[0].Question != "pick?" || q.Answer != "B" {
		t.Fatalf("question part = %+v, want pick?/answer B", q)
	}

}

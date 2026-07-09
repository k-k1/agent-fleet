package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
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
		`CREATE TABLE todo (session_id TEXT, content TEXT, status TEXT, priority TEXT, position INTEGER)`,
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

func TestOpencodeReadSession(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_x"

	// m1: user prompt.
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000},"path":{"cwd":"/home/dev/repos/x"}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"hello opencode"}`)

	// m2: assistant with framing (dropped), a tool trace, and final text.
	insMsg(t, db, "m2", ses, 2000, `{"role":"assistant","modelID":"deepseek-v4-pro","variant":"max","tokens":{"input":100,"output":20,"cache":{"read":80,"write":5}},"time":{"created":2000}}`)
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

	turns := readSession(db, ses)
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

func TestOpencodeQuestions(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_q"
	// A message with a completed question (shown as an answered block) and a running one
	// (surfaced as the pending question).
	insMsg(t, db, "mq", ses, 1000, `{"role":"assistant","modelID":"m"}`)
	insPart(t, db, "pqa", "mq", ses, 1, `{"type":"tool","tool":"question","state":{"status":"completed","input":{"questions":[{"header":"H","question":"pick?","options":[{"label":"A"},{"label":"B"}]}]},"output":"User has answered your questions: \"pick?\"=\"B\". You may continue."}}`)
	insPart(t, db, "pqb", "mq", ses, 2, `{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"header":"H2","question":"next?","options":[{"label":"X"},{"label":"Y"}]}]}}}`)

	turns := readSession(db, ses)
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

	// pending returns the running question.
	pd := pending(db, ses)
	if len(pd) != 1 || pd[0].Question != "next?" || len(pd[0].Options) != 2 {
		t.Fatalf("pending = %+v, want the running 'next?' question", pd)
	}
}

func TestOpencodeMode(t *testing.T) {
	db := newOpencodeTestDB(t)
	// Newest message's agent decides the mode: "plan" → plan, else normal.
	insMsg(t, db, "b1", "ses_b", 1, `{"role":"assistant","agent":"build"}`)
	if got := mode(db, "ses_b"); got != "normal" {
		t.Fatalf("build agent -> %q, want normal", got)
	}
	insMsg(t, db, "p1", "ses_p", 1, `{"role":"assistant","agent":"plan"}`)
	if got := mode(db, "ses_p"); got != "plan" {
		t.Fatalf("plan agent -> %q, want plan", got)
	}
}

func TestOpencodeTasks(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_t"
	// Inserted out of position order to verify ORDER BY position.
	if _, err := db.Exec(`INSERT INTO todo(session_id,content,status,priority,position) VALUES(?,?,?,?,?)`, ses, "second", "pending", "medium", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO todo(session_id,content,status,priority,position) VALUES(?,?,?,?,?)`, ses, "first", "completed", "high", 1); err != nil {
		t.Fatal(err)
	}
	// A todo from another session must not leak.
	if _, err := db.Exec(`INSERT INTO todo(session_id,content,status,priority,position) VALUES(?,?,?,?,?)`, "ses_other", "nope", "pending", "low", 1); err != nil {
		t.Fatal(err)
	}
	ts := tasks(db, ses)
	if len(ts) != 2 || ts[0].Subject != "first" || ts[0].Status != "completed" ||
		ts[1].Subject != "second" || ts[1].Status != "pending" {
		t.Fatalf("tasks = %+v, want [first/completed, second/pending] in position order", ts)
	}
}

func TestOpencodeSessionResumable(t *testing.T) {
	// sessionResumable opens the db at $HOME/.local/share/opencode/opencode.db;
	// build one there so the real path resolves.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dbDir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	ins := func(id, ses string, tc int, data string) {
		if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES(?,?,?,?,?)`, id, ses, tc, tc, data); err != nil {
			t.Fatal(err)
		}
	}
	// done: last turn is a completed assistant message.
	ins("d1", "ses_done", 1, `{"role":"user"}`)
	ins("d2", "ses_done", 2, `{"role":"assistant","time":{"completed":123}}`)
	// interrupted: last assistant message has no completed time.
	ins("i1", "ses_int", 1, `{"role":"user"}`)
	ins("i2", "ses_int", 2, `{"role":"assistant","time":{}}`)
	// pending: last row is a user message (assistant never replied).
	ins("p1", "ses_pend", 1, `{"role":"user"}`)
	db.Close()

	cases := []struct {
		ses  string
		want bool
	}{
		{"", true},          // no captured session
		{"ses_none", true},  // unknown session — nothing to re-run
		{"ses_done", true},  // completed last turn — safe to resume
		{"ses_int", false},  // interrupted mid-turn — would re-run
		{"ses_pend", false}, // user message unanswered — would generate
	}
	for _, c := range cases {
		if got := sessionResumable(c.ses); got != c.want {
			t.Errorf("sessionResumable(%q) = %v, want %v", c.ses, got, c.want)
		}
	}
}

func TestOpencodeReadSessionEmpty(t *testing.T) {
	db := newOpencodeTestDB(t)
	if turns := readSession(db, "ses_none"); len(turns) != 0 {
		t.Fatalf("empty session -> %d turns, want 0", len(turns))
	}
}

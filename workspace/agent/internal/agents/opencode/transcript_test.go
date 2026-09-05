package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT, time_created INTEGER, time_compacting INTEGER)`,
		`CREATE TABLE session_input (id TEXT PRIMARY KEY, session_id TEXT, prompt TEXT, delivery TEXT, admitted_seq INTEGER, promoted_seq INTEGER, time_created INTEGER)`,
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

	turns := readSession(db, ses)
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

func TestOpencodeQueued(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_q"
	ins := func(id, prompt string, promoted any, tc int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO session_input(id,session_id,prompt,delivery,admitted_seq,promoted_seq,time_created) VALUES(?,?,?,?,?,?,?)`,
			id, ses, prompt, "queue", tc, promoted, tc); err != nil {
			t.Fatal(err)
		}
	}
	// Pending rows (promoted_seq NULL) in arrival order; a promoted row is excluded.
	ins("i2", `"second queued"`, nil, 2000)
	ins("i1", `"first queued"`, nil, 1000)
	ins("i0", `"already promoted"`, 5, 500)
	// Another session's queue must not leak.
	if _, err := db.Exec(`INSERT INTO session_input(id,session_id,prompt,delivery,admitted_seq,promoted_seq,time_created) VALUES('ix','ses_other','"nope"','queue',1,NULL,1)`); err != nil {
		t.Fatal(err)
	}
	got := queued(db, ses)
	if len(got) != 2 || got[0] != "first queued" || got[1] != "second queued" {
		t.Fatalf("queued = %+v, want [first queued, second queued]", got)
	}
}

func TestOpencodePromptText(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`"plain string"`, "plain string"},                                              // JSON string
		{`[{"type":"text","text":"a"},{"type":"file","text":""},{"text":"b"}]`, "a\nb"}, // parts array
		{`{"text":"obj text"}`, "obj text"},                                             // object with text
		{`{"parts":[{"type":"text","text":"nested"}]}`, "nested"},                       // object with parts
		{`raw unencoded`, `raw unencoded`},                                              // not JSON — raw fallback
	}
	for _, c := range cases {
		if got := promptText(c.raw); got != c.want {
			t.Errorf("promptText(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestOpencodeSidechain(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_parent"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_child',?,'/d',1)`, ses); err != nil {
		t.Fatal(err)
	}
	// Unrelated root session — must not be pulled in.
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES('ses_root',NULL,'/d',1)`); err != nil {
		t.Fatal(err)
	}
	// Parent user turn, then the subagent's turns land while the parent assistant turn
	// is still in flight, then the parent assistant completes.
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"do it"}`)
	insMsg(t, db, "m2", ses, 2000, `{"role":"assistant","modelID":"m","time":{"created":2000}}`)
	insPart(t, db, "p2", "m2", ses, 1, `{"type":"tool","tool":"task","state":{"status":"completed","input":{"description":"explore stuff","prompt":"inspect files","subagent_type":"Explore"},"output":"findings"}}`)
	insMsg(t, db, "c1", "ses_child", 3000, `{"role":"assistant","modelID":"m","time":{"created":3000}}`)
	insPart(t, db, "pc1", "c1", "ses_child", 1, `{"type":"text","text":"subagent findings"}`)
	insMsg(t, db, "m3", ses, 4000, `{"role":"assistant","modelID":"m","time":{"created":4000}}`)
	insPart(t, db, "p3", "m3", ses, 1, `{"type":"text","text":"summary"}`)
	// An unrelated root session's message stays out.
	insMsg(t, db, "r1", "ses_root", 3500, `{"role":"user"}`)
	insPart(t, db, "pr1", "r1", "ses_root", 1, `{"type":"text","text":"other"}`)

	turns := readSession(db, ses)
	if len(turns) != 4 {
		t.Fatalf("want 4 turns (parent 3 + child 1), got %d: %+v", len(turns), turns)
	}
	if turns[1].Sidechain || turns[0].Sidechain || turns[3].Sidechain {
		t.Fatalf("parent turns must not be sidechain: %+v", turns)
	}
	// The parent task becomes one compact delegation event; the child turns remain
	// sidechain data for optional diagnostics and are hidden by the normal Console view.
	p := turns[1].Parts[0]
	if p.Kind != "delegation" || p.Tool != "task" || p.Info != "explore stuff" ||
		p.Prompt != "inspect files" || p.AgentType != "Explore" || p.Status != "completed" || p.Output != "findings" {
		t.Fatalf("task delegation = %+v", p)
	}
	c := turns[2]
	if !c.Sidechain || c.Text != "subagent findings" {
		t.Fatalf("turn2 = %+v, want sidechain 'subagent findings'", c)
	}
	// Idx is the merged ordinal (stable render key).
	if turns[2].Idx != 2 || turns[3].Idx != 3 {
		t.Fatalf("idx = %d/%d, want 2/3", turns[2].Idx, turns[3].Idx)
	}
}

func TestOpencodeCompacting(t *testing.T) {
	db := newOpencodeTestDB(t)
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created,time_compacting) VALUES('ses_c',NULL,'/d',1,12345)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created,time_compacting) VALUES('ses_n',NULL,'/d',1,NULL)`); err != nil {
		t.Fatal(err)
	}
	if !compacting(db, "ses_c") {
		t.Fatal("ses_c: want compacting=true")
	}
	if compacting(db, "ses_n") || compacting(db, "ses_missing") {
		t.Fatal("ses_n/missing: want compacting=false")
	}
}

func TestOpencodeLiveStateQuestion(t *testing.T) {
	// LiveState opens the db at $HOME/.local/share/opencode/opencode.db — build one
	// there (like TestOpencodeSessionResumable) so the real path resolves.
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
	stmts := []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT, time_created INTEGER, time_compacting INTEGER)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	dir := "/home/dev/repos/x"
	ses := "ses_lq"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	// In-flight assistant turn (no completed time) whose question tool is running.
	if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES('m1',?,1000,1000,?)`,
		ses, `{"role":"assistant","time":{}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO part(id,message_id,session_id,time_created,data) VALUES('p1','m1',?,1,?)`,
		ses, `{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"question":"pick?","options":[{"label":"A"}]}]}}}`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	m := session.Meta{Dir: dir, Name: "n"}
	if got := LiveState(m); got != "question" {
		t.Fatalf("LiveState = %q, want question", got)
	}
}

// newOpencodeLiveStore builds the store at the path LiveState actually opens
// ($HOME/.local/share/opencode/opencode.db) and returns an open handle — the setup
// TestOpencodeLiveStateQuestion does inline, shared by the LiveState tests below.
func newOpencodeLiveStore(t *testing.T) *sql.DB {
	t.Helper()
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
	t.Cleanup(func() { db.Close() })
	for _, s := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT, time_created INTEGER, time_compacting INTEGER)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// A real store error must degrade to "" (unknown → the caller falls back to the plugin
// status), never to "idle". opencode's schema is its own unversioned contract and it
// does migrate; answering "idle" from a failed read would silently flip a live turn to
// awaiting input with no stop button — the claude false-idle bug, reached through the store
// contract instead of a TUI string, and with no reverse-heal to catch it.
func TestOpencodeLiveStateStoreErrorIsUnknown(t *testing.T) {
	db := newOpencodeLiveStore(t)
	dir, ses := "/home/dev/repos/x", "ses_err"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","time":{}}`) // in-flight turn
	m := session.Meta{Dir: dir, Name: "n"}
	if got := LiveState(m); got != "working" {
		t.Fatalf("baseline LiveState = %q, want working", got)
	}

	// Simulate a future opencode migration renaming the v1 table this reads.
	if _, err := db.Exec(`ALTER TABLE message RENAME TO message_v3`); err != nil {
		t.Fatal(err)
	}
	// (a) store-derived resolution: activeSessionErr's query itself fails.
	if got := LiveState(m); got != "" {
		t.Errorf("after schema change LiveState = %q, want %q (unknown); a silent \"idle\" is the false-idle bug", got, "")
	}
	// (b) plugin-mapped resolution: the slot resolves without touching `message`, so the
	// failure surfaces on LiveState's own message query instead.
	sids.Write(session.UUID(dir, "n"), ses)
	t.Cleanup(func() { sids.Remove(session.UUID(dir, "n")) })
	if got := LiveState(m); got != "" {
		t.Errorf("mapped slot after schema change LiveState = %q, want %q (unknown)", got, "")
	}
}

// The ErrNoRows guard: the plugin records session.created before the first message, so a
// mapped-but-empty conversation is genuinely sitting at the composer. That must stay
// "idle" and not get swept into the unknown branch above.
func TestOpencodeLiveStateNoMessagesIsIdle(t *testing.T) {
	db := newOpencodeLiveStore(t)
	dir, ses := "/home/dev/repos/y", "ses_empty"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	sids.Write(session.UUID(dir, "n2"), ses)
	t.Cleanup(func() { sids.Remove(session.UUID(dir, "n2")) })
	if got := LiveState(session.Meta{Dir: dir, Name: "n2"}); got != "idle" {
		t.Fatalf("conversation with no messages yet: LiveState = %q, want idle", got)
	}
}

// A message payload that isn't the shape we parse is the same contract break as a schema
// change — unknown, not idle.
func TestOpencodeLiveStateBadPayloadIsUnknown(t *testing.T) {
	db := newOpencodeLiveStore(t)
	dir, ses := "/home/dev/repos/z", "ses_bad"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	insMsg(t, db, "m1", ses, 1000, `{"role":`) // truncated / unparsable
	if got := LiveState(session.Meta{Dir: dir, Name: "n3"}); got != "" {
		t.Fatalf("unparsable message payload: LiveState = %q, want %q (unknown)", got, "")
	}
}

func TestOpencodeActiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // sids store lives under $HOME/.config/agent-fleet
	db := newOpencodeTestDB(t)
	dir := "/home/dev/repos/temp"
	insSes := func(id string, tc int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,?)`, id, dir, tc); err != nil {
			t.Fatal(err)
		}
	}
	// An OLD conversation in the dir (created + messaged long before the slot).
	insSes("ses_old", 1000)
	insMsg(t, db, "mo", "ses_old", 2000, `{"role":"user"}`)

	// szyyh2f regression: a NEW slot (created at t=10000) in a previously-used dir
	// must NOT hijack the dir's old conversation.
	slot := session.Meta{Dir: dir, Name: "new1", CreatedAt: time.UnixMilli(10000).UTC().Format(time.RFC3339)}
	if got := activeSession(db, slot); got != "" {
		t.Fatalf("new slot resolved %q, want none (old conversation must not be hijacked)", got)
	}

	// A conversation opened AFTER the slot was created resolves (plugin-less fallback).
	insSes("ses_own", 11000)
	insMsg(t, db, "mn", "ses_own", 12000, `{"role":"user"}`)
	if got := activeSession(db, slot); got != "ses_own" {
		t.Fatalf("post-slot conversation = %q, want ses_own", got)
	}

	// The plugin-captured per-slot mapping wins over the store-derived fallback.
	insSes("ses_mapped", 13000)
	insMsg(t, db, "mm", "ses_mapped", 14000, `{"role":"user"}`)
	sids.Write(session.UUID(dir, "new1"), "ses_mapped")
	if got := activeSession(db, slot); got != "ses_mapped" {
		t.Fatalf("mapped = %q, want ses_mapped", got)
	}

	// A STALE mapping (session gone from the store) falls back to the store lookup.
	sids.Write(session.UUID(dir, "new1"), "ses_gone")
	if got := activeSession(db, slot); got != "ses_mapped" && got != "ses_own" {
		t.Fatalf("stale mapping fallback = %q, want a post-slot store session", got)
	}
}

// The parts of a whole conversation are fetched a batch of messages at a time. Across a
// batch boundary each message must still get its OWN parts and its own ordinal — group
// them wrong and every turn after the first batch shows another turn's content.
func TestOpencodeReadSessionAcrossPartBatches(t *testing.T) {
	db := newOpencodeTestDB(t)
	prev := partBatch
	partBatch = 2
	t.Cleanup(func() { partBatch = prev })
	ses := "ses_batch"
	n := partBatch + 3
	for i := 0; i < n; i++ {
		id := "m" + strconv.Itoa(i)
		insMsg(t, db, id, ses, i+1, `{"role":"user","time":{"created":1000}}`)
		insPart(t, db, "p"+strconv.Itoa(i), id, ses, 1, `{"type":"text","text":"prompt `+strconv.Itoa(i)+`"}`)
	}
	turns := readSession(db, ses)
	if len(turns) != n {
		t.Fatalf("got %d turns, want %d", len(turns), n)
	}
	for i, tr := range turns {
		if tr.Idx != i {
			t.Fatalf("turn %d has idx %d", i, tr.Idx)
		}
		if want := "prompt " + strconv.Itoa(i); tr.Text != want {
			t.Fatalf("turn %d text = %q, want %q — parts grouped onto the wrong message", i, tr.Text, want)
		}
	}
}

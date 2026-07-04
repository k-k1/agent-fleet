package main

import (
	"database/sql"
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
	insPart(t, db, "p2c", "m2", ses, 3, `{"type":"tool","tool":"bash","state":{"input":{"command":"ls -la"}}}`)
	insPart(t, db, "p2d", "m2", ses, 4, `{"type":"text","text":"done"}`)

	// m3: assistant with ONLY framing/reasoning — no displayable part, must be dropped.
	insMsg(t, db, "m3", ses, 3000, `{"role":"assistant","modelID":"deepseek-v4-pro"}`)
	insPart(t, db, "p3", "m3", ses, 1, `{"type":"reasoning","text":"more thinking"}`)

	// A message from ANOTHER session must not leak in.
	insMsg(t, db, "z1", "ses_other", 1500, `{"role":"user"}`)
	insPart(t, db, "pz", "z1", "ses_other", 1, `{"type":"text","text":"other session"}`)

	turns := opencodeReadSession(db, ses)
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
	// Parts: tool (bash / "ls -la") then text "done"; reasoning + step-* dropped.
	if len(a.Parts) != 2 {
		t.Fatalf("turn1 parts = %d, want 2 (tool+text): %+v", len(a.Parts), a.Parts)
	}
	if a.Parts[0].Kind != "tool" || a.Parts[0].Tool != "bash" || a.Parts[0].Info != "ls -la" {
		t.Fatalf("turn1 part0 = %+v, want tool bash 'ls -la'", a.Parts[0])
	}
	if a.Parts[1].Kind != "text" || a.Parts[1].Text != "done" || a.Text != "done" {
		t.Fatalf("turn1 part1/text = %+v / %q", a.Parts[1], a.Text)
	}
	// m3 dropped but consumed a message ordinal — the assistant turn keeps ordinal 1.
	if a.Idx != 1 {
		t.Fatalf("turn1 idx = %d, want 1", a.Idx)
	}
}

func TestOpencodeReadSessionEmpty(t *testing.T) {
	db := newOpencodeTestDB(t)
	if turns := opencodeReadSession(db, "ses_none"); len(turns) != 0 {
		t.Fatalf("empty session -> %d turns, want 0", len(turns))
	}
}

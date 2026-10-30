package cursor

import (
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A launch with no model (the picker's Default) must run Auto, not whatever model cursor
// persisted from the last launch that passed one: without --model the CLI reads its own
// cli-config.json, and a Free plan refuses anything but Auto (#2928).
func TestDefaultTerminalLaunchPinsAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range []struct{ model, want string }{
		{"", "auto"},
		{"auto", "auto"},
		{"composer-2.5", "composer-2.5"},
	} {
		m := session.Meta{Name: "cu-" + c.model, Dir: t.TempDir(), Kind: session.KindCursor, Model: c.model}
		plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
		if err != nil {
			t.Fatal(err)
		}
		want := "--model " + session.ShellQuote(c.want)
		if !strings.Contains(plan.Program, want) || strings.Count(plan.Program, "--model") != 1 {
			t.Errorf("model %q: program %q, want exactly one %q", c.model, plan.Program, want)
		}
		if id := sids.Read(session.UUID(m.Dir, m.Name)); id == "" || !strings.HasSuffix(plan.Program, "--resume "+session.ShellQuote(id)) {
			t.Errorf("model %q: program %q does not resume the minted chat %q", c.model, plan.Program, id)
		}
	}
}

// A chat that recorded its own model resumes on it when no flag is passed (recall.go), so a
// Default resume must keep passing none — pinning Auto there would undo a /model switch the
// catalog cannot name.
func TestDefaultResumeKeepsTheChatsOwnModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cu-resume", Dir: t.TempDir(), Kind: session.KindCursor}
	chatID := "11111111-1111-4111-8111-111111111111"
	sids.Write(session.UUID(m.Dir, m.Name), chatID)
	writeStoreDB(t, chatID, `{"mode":"default","lastUsedModel":"gemini-3.7-flash"}`)
	for _, model := range []string{"", "auto"} {
		m.Model = model
		plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(plan.Program, "--model") || !strings.Contains(plan.Program, chatID) {
			t.Errorf("model %q: resume program %q must pass --resume and no --model", model, plan.Program)
		}
	}
}

// What a Default launch does depends on what cursor's store.db says about the chat: no store
// yet or one with no model recorded pins Auto, a recorded model is restored by cursor itself,
// and a store that exists but cannot be read (locked past busy_timeout, corrupt, schema drift)
// keeps the no-flag launch, so a read failure never overrides a chat's own model.
func TestDefaultLaunchFollowsTheChatStore(t *testing.T) {
	for _, c := range []struct {
		name  string
		store func(t *testing.T, chatID string)
		want  string // "" = no --model
	}{
		{"no store.db", func(*testing.T, string) {}, "auto"},
		{"no model recorded", func(t *testing.T, id string) { writeStoreDB(t, id, `{"mode":"default"}`) }, "auto"},
		{"model recorded", func(t *testing.T, id string) { writeStoreDB(t, id, `{"lastUsedModel":"gemini-3.7-flash"}`) }, ""},
		{"corrupt store.db", func(t *testing.T, id string) { writeStoreFile(t, id, []byte("not a sqlite database")) }, ""},
		{"meta row missing", func(t *testing.T, id string) {
			writeStoreRows(t, id, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`)
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			m := session.Meta{Name: "cu-store", Dir: t.TempDir(), Kind: session.KindCursor}
			chatID := "22222222-2222-4222-8222-222222222222"
			sids.Write(session.UUID(m.Dir, m.Name), chatID)
			c.store(t, chatID)
			plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
			if err != nil {
				t.Fatal(err)
			}
			n := strings.Count(plan.Program, "--model")
			if c.want == "" && n != 0 || c.want != "" && (n != 1 || !strings.Contains(plan.Program, "--model "+session.ShellQuote(c.want))) {
				t.Errorf("program %q, want model %q", plan.Program, c.want)
			}
		})
	}
}

// The Managed child reads the same persisted default, so its Default pins Auto too.
func TestDefaultManagedChildPinsAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newThreadHandle("s1", t.TempDir(), "slot-1")
	for _, c := range []struct{ model, want string }{{"", "auto"}, {"auto", "auto"}, {"composer-2.5", "composer-2.5"}} {
		args := command(h.Handle, agents.ThreadSettings{Model: c.model}).Args
		i := slices.Index(args, "--model")
		n := 0
		for _, a := range args {
			if a == "--model" {
				n++
			}
		}
		if i < 0 || i+1 >= len(args) || args[i+1] != c.want || n != 1 {
			t.Errorf("model %q: args %q, want --model %s", c.model, args, c.want)
		}
	}
}

func TestLaunchModel(t *testing.T) {
	for _, c := range []struct {
		model   string
		keepOwn bool
		want    string
	}{
		{"", false, "auto"},
		{"auto", false, "auto"},
		{"", true, ""},
		{"auto", true, ""},
		{"composer-2.5", false, "composer-2.5"},
		{"composer-2.5", true, "composer-2.5"},
	} {
		if got := launchModel(c.model, c.keepOwn); got != c.want {
			t.Errorf("launchModel(%q, %v) = %q, want %q", c.model, c.keepOwn, got, c.want)
		}
	}
}

func writeStoreDB(t *testing.T, chatID, meta string) {
	t.Helper()
	v := hex.EncodeToString([]byte(meta))
	writeStoreRows(t, chatID, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`, `INSERT INTO meta VALUES ('0', '`+v+`')`)
}

func storePath(t *testing.T, chatID string) string {
	t.Helper()
	dir := filepath.Join(Home(), "chats", "ws", chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "store.db")
}

func writeStoreRows(t *testing.T, chatID string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+storePath(t, chatID))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

func writeStoreFile(t *testing.T, chatID string, b []byte) {
	t.Helper()
	if err := os.WriteFile(storePath(t, chatID), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

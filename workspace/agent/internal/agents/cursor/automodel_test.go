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
		if want := "--model " + session.ShellQuote(c.want); !strings.Contains(plan.Program, want) {
			t.Errorf("model %q: program %q lacks %q", c.model, plan.Program, want)
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

// The Managed child reads the same persisted default, so its Default pins Auto too.
func TestDefaultManagedChildPinsAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newThreadHandle("s1", t.TempDir(), "slot-1")
	for _, c := range []struct{ model, want string }{{"", "auto"}, {"auto", "auto"}, {"composer-2.5", "composer-2.5"}} {
		args := command(h.Handle, agents.ThreadSettings{Model: c.model}).Args
		i := slices.Index(args, "--model")
		if i < 0 || i+1 >= len(args) || args[i+1] != c.want {
			t.Errorf("model %q: args %q, want --model %s", c.model, args, c.want)
		}
	}
}

func TestLaunchModel(t *testing.T) {
	for _, c := range []struct{ model, recorded, want string }{
		{"", "", "auto"},
		{"auto", "", "auto"},
		{"", "gemini-3.7-flash", ""},
		{"auto", "default", ""},
		{"composer-2.5", "", "composer-2.5"},
		{"composer-2.5", "gpt-5.2", "composer-2.5"},
	} {
		if got := launchModel(c.model, c.recorded); got != c.want {
			t.Errorf("launchModel(%q, %q) = %q, want %q", c.model, c.recorded, got, c.want)
		}
	}
}

func writeStoreDB(t *testing.T, chatID, meta string) {
	t.Helper()
	dir := filepath.Join(Home(), "chats", "ws", chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := hex.EncodeToString([]byte(meta))
	for _, s := range []string{`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`, `INSERT INTO meta VALUES ('0', '` + v + `')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

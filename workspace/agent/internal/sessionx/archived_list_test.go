package sessionx

// The archived shelf (GET /sessions/archived) is read by the restore modal for a handful of
// meta fields. These tests pin that the listing stays a meta-only read: no transcript reads,
// no overview-facts refreshes, no working-copy marker writes, and a Resumable verdict equal
// to what each kind's own WireLive gives a stopped session.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// listArchivedRows calls the handler and decodes the rows named prefix* (the metas dir is
// shared by the whole package's tests), as raw maps, so a field the slim row must not carry
// is visible as a key rather than a zero value.
func listArchivedRows(t testing.TB, prefix string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleListArchived(rec, httptest.NewRequest("GET", "/sessions/archived", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range body.Sessions {
		if n, _ := r["name"].(string); strings.HasPrefix(n, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// writeClaudeTranscript puts a transcript for m where claude's reader looks for it.
func writeClaudeTranscript(t testing.TB, root string, m session.Meta, lines int) string {
	t.Helper()
	dir := filepath.Join(root, "projects", "proj-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"text","text":"reply %d"}],`+
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":300,"cache_creation_input_tokens":5}}}`+"\n", i)
	}
	p := filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHandleListArchived_DoesNotReadTranscriptsOrTouchTheWorkingCopy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	claudeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(claudeDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cl := session.Meta{Name: "arch_list_claude", Dir: claudeDir, Kind: session.KindClaude, Archived: true}
	cx := session.Meta{Name: "arch_list_codex", Dir: t.TempDir(), Kind: session.KindCodex, Archived: true}
	session.WriteMeta(cl)
	session.WriteMeta(cx)
	writeClaudeTranscript(t, root, cl, 20)

	rows := listArchivedRows(t, "arch_list_")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		// claude's WireLive fills these from the transcript tail.
		for _, k := range []string{"lastSay", "context", "tokenSpends"} {
			if _, ok := r[k]; ok {
				t.Errorf("%v: row carries %q, which only a transcript read can produce", r["name"], k)
			}
		}
	}
	// overviewFactsFor registers an entry (and starts a background transcript parse) the
	// first time it sees a session; the shelf must never be the one to do that.
	overviewFactsMu.Lock()
	for _, name := range []string{cl.Name, cx.Name} {
		if _, ok := overviewFactsCache[name]; ok {
			t.Errorf("%s: listing started an overview-facts refresh", name)
		}
	}
	overviewFactsMu.Unlock()
	// gitx.WorkingCopyID would have written this marker into the working copy's git dir.
	if _, err := os.Stat(filepath.Join(claudeDir, ".git", "agent-fleet-working-copy-id")); !os.IsNotExist(err) {
		t.Errorf("listing wrote a working-copy marker (stat err = %v)", err)
	}
}

func TestHandleListArchived_SlimRow(t *testing.T) {
	dir := t.TempDir()
	session.WriteMeta(session.Meta{
		Name: "arch_row_a", Dir: dir, Kind: session.KindCodex, Repo: "r", Branch: "b", Title: "T",
		CreatedAt: "2026-10-01T10:00:00Z", Locked: true, Archived: true,
	})
	session.WriteMeta(session.Meta{Name: "arch_row_live", Dir: dir, Kind: session.KindCodex}) // not archived
	rows := listArchivedRows(t, "arch_row_")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want only the archived one", len(rows))
	}
	r := rows[0]
	want := map[string]any{
		"name": "arch_row_a", "dir": dir, "kind": "codex", "repo": "r", "branch": "b", "title": "T",
		"createdAt": "2026-10-01T10:00:00Z", "locked": true, "archived": true, "resumable": true,
	}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
	if s, _ := r["started"].(string); s == "" {
		t.Error("started is empty")
	}
	if r["display"] == "" || r["display"] == nil {
		t.Error("display is empty")
	}
}

func TestHandleListArchived_ResumableFollowsTheFolder(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "deleted-worktree")
	session.WriteMeta(session.Meta{Name: "arch_res_gone", Dir: gone, Kind: session.KindClaude, Archived: true})
	session.WriteMeta(session.Meta{Name: "arch_res_here", Dir: t.TempDir(), Kind: session.KindClaude, Archived: true})
	session.WriteMeta(session.Meta{Name: "arch_res_shell", Dir: gone, Kind: session.KindShell, Archived: true})
	got := map[string]any{}
	for _, r := range listArchivedRows(t, "arch_res_") {
		got[r["name"].(string)] = r["resumable"]
	}
	want := map[string]any{"arch_res_gone": false, "arch_res_here": true, "arch_res_shell": true}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s resumable = %v, want %v", k, got[k], v)
		}
	}
}

// TestStoppedResumableMatchesWireLive keeps the shelf's shortcut honest: for every registered
// kind, with the folder present and gone, it must equal what that kind's WireLive says.
func TestStoppedResumableMatchesWireLive(t *testing.T) {
	var kinds []string
	for k := range agentRegistry {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		for _, exists := range []bool{true, false} {
			dir := t.TempDir()
			if !exists {
				dir = filepath.Join(dir, "gone")
			}
			m := session.Meta{Name: "arch_parity_" + k, Dir: dir, Kind: k}
			if want, got := AgentOf(k).WireLive(m, false).Resumable, stoppedResumable(m); got != want {
				t.Errorf("kind %s, dir exists=%v: stoppedResumable = %v, WireLive = %v", k, exists, got, want)
			}
		}
	}
}

// BenchmarkHandleListArchived measures the shelf with a few hundred archived sessions, half
// claude with a sizeable transcript each and half codex. "full" is the former per-row
// wireSession loop, "slim" is the handler.
func BenchmarkHandleListArchived(b *testing.B) {
	root := b.TempDir()
	b.Setenv("CLAUDE_CONFIG_DIR", root)
	var metas []session.Meta
	var logs []string
	for i := 0; i < 300; i++ {
		m := session.Meta{Name: fmt.Sprintf("arch_bench_%03d", i), Dir: b.TempDir(), Kind: session.KindClaude, Archived: true,
			CreatedAt: fmt.Sprintf("2026-09-%02dT10:00:00Z", i%28+1)}
		if i%2 == 1 {
			m.Kind = session.KindCodex
		} else {
			logs = append(logs, writeClaudeTranscript(b, root, m, 3000))
		}
		session.WriteMeta(m)
		metas = append(metas, m)
	}
	// claude memoizes its tail read by transcript mtime, so each iteration bumps the mtimes:
	// a cold read is what the first open of the modal after a session ran pays.
	tick := 0
	touch := func() {
		b.StopTimer()
		tick++
		now := time.Now().Add(time.Duration(tick) * time.Second)
		for _, p := range logs {
			_ = os.Chtimes(p, now, now)
		}
		b.StartTimer()
	}
	b.Run("full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			touch()
			for _, m := range metas {
				_ = wireSession(m, false)
			}
		}
	})
	b.Run("slim", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			touch()
			if n := len(listArchivedRows(b, "arch_bench_")); n != len(metas) {
				b.Fatalf("rows = %d", n)
			}
		}
	})
}

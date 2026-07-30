package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTranscriptTouchedIgnoresBookkeeping pins the contract the duplicate-report bug
// turned on (2026-07-30 s2bl5pv / sannme2 / sp2qemx): 転写の「鮮度」はファイルの mtime
// ではなく **user/assistant 行の timestamp** で決まる。claude はターンと無関係な記帳行
// （away_summary・custom-title・…）を後から追記するので、mtime を見ていた頃は静止した
// セッションが「実行中」に化け、報告済みの指示が「作業を再開した」と誤読されていた。
func TestTranscriptTouchedIgnoresBookkeeping(t *testing.T) {
	const sid = "aaaa1111-2222-5bbb-8ccc-333344445555"

	// write plants a transcript for sid and returns its path. The file's mtime is
	// always "now" — that is exactly what must NOT decide the answer.
	write := func(t *testing.T, lines ...string) string {
		t.Helper()
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		dir := filepath.Join(root, "projects", "proj-a")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rec := func(typ string, at time.Time, extra string) string {
		return fmt.Sprintf(`{"type":%q,"timestamp":%q%s}`, typ, at.UTC().Format(time.RFC3339Nano), extra)
	}
	// The bookkeeping tail claude actually writes after a finished turn.
	bookkeeping := func(at time.Time) []string {
		return []string{
			rec("system", at, `,"subtype":"stop_hook_summary"`),
			rec("system", at, `,"subtype":"turn_duration"`),
			`{"type":"last-prompt","lastPrompt":"x"}`,
			`{"type":"custom-title","customTitle":"[AF] x"}`,
			`{"type":"agent-name"}`,
			`{"type":"mode"}`,
			`{"type":"permission-mode"}`,
			`{"type":"file-history-snapshot"}`,
			rec("system", at, `,"subtype":"away_summary"`),
		}
	}

	t.Run("bookkeeping tail does not refresh", func(t *testing.T) {
		old := time.Now().Add(-30 * time.Minute)
		lines := append([]string{rec("user", old, ""), rec("assistant", old, "")}, bookkeeping(time.Now())...)
		write(t, lines...)
		at, ok := TranscriptTouched(sid)
		if !ok {
			t.Fatal("ok = false, want the assistant record's time")
		}
		if d := at.Sub(old).Abs(); d > time.Second {
			t.Errorf("touched = %v, want the 30-min-old assistant record (off by %v)", at, d)
		}
		if TranscriptBusy(sid) {
			t.Error("busy = true — a bookkeeping-only tail must not read as a running turn")
		}
	})

	t.Run("real record refreshes", func(t *testing.T) {
		write(t, rec("user", time.Now().Add(-time.Hour), ""), rec("assistant", time.Now(), ""))
		if !TranscriptBusy(sid) {
			t.Error("busy = false — a fresh assistant record IS the turn running")
		}
	})

	t.Run("sidechain counts (old-era inline subagents)", func(t *testing.T) {
		write(t, rec("assistant", time.Now(), `,"isSidechain":true`))
		if !TranscriptBusy(sid) {
			t.Error("busy = false — an inline sidechain turn is still work in flight")
		}
	})

	t.Run("no real record at all", func(t *testing.T) {
		write(t, bookkeeping(time.Now())...)
		if _, ok := TranscriptTouched(sid); ok {
			t.Error("ok = true, want false when the transcript holds no user/assistant record")
		}
	})

	t.Run("real record older than the tail window", func(t *testing.T) {
		// The last real record sits before transcriptTailWindow bytes of bookkeeping,
		// so the first read misses it and the whole-file fallback must find it.
		old := time.Now().Add(-30 * time.Minute)
		lines := []string{rec("assistant", old, "")}
		filler := `{"type":"file-history-delta","payload":"` + strings.Repeat("x", 4096) + `"}`
		for i := 0; i < (transcriptTailWindow/4096)+2; i++ {
			lines = append(lines, filler)
		}
		write(t, lines...)
		at, ok := TranscriptTouched(sid)
		if !ok {
			t.Fatal("ok = false — the whole-file fallback did not run")
		}
		if d := at.Sub(old).Abs(); d > time.Second {
			t.Errorf("touched = %v, want the old assistant record", at)
		}
	})

	t.Run("missing transcript", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		if _, ok := TranscriptTouched(sid); ok {
			t.Error("ok = true for a session with no transcript")
		}
	})
}

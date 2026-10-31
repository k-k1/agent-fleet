package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// skipFixture is a claude session with a main transcript and a counting seam on the parse.
type skipFixture struct {
	t      *testing.T
	m      session.Meta
	main   string
	proj   string
	sid    string
	parses int
	born   time.Time
}

func newSkipFixture(t *testing.T) *skipFixture {
	t.Helper()
	useTempUsageDir(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	f := &skipFixture{t: t, born: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	f.m = session.Meta{Name: "skip3112", Dir: t.TempDir(), Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: f.born.Format(time.RFC3339)}
	f.sid = session.UUID(f.m.Dir, f.m.Name)
	f.proj = filepath.Join(cfg, "projects", "p1")
	f.main = filepath.Join(f.proj, f.sid+".jsonl")
	orig := foldUsageTurns
	foldUsageTurns = func(m session.Meta) []transcript.Turn { f.parses++; return orig(m) }
	t.Cleanup(func() { foldUsageTurns = orig })
	return f
}

func (f *skipFixture) at(d time.Duration) string { return f.born.Add(d).Format(time.RFC3339Nano) }

// write appends to (or creates) p and moves its mtime on, so a same-size rewrite still differs.
func (f *skipFixture) write(p, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	fh, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := fh.WriteString(body); err != nil {
		f.t.Fatal(err)
	}
	fh.Close()
}

// fold is one commit, as foldAllSessionUsage does it (not trailing).
func (f *skipFixture) fold() int {
	f.t.Helper()
	n, err := commitSessionUsageFold(f.m, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *skipFixture) rows() (own, side int) {
	for _, r := range usagex.ReadRows() {
		if r.Ref == f.m.Name {
			if r.Sidechain {
				side++
			} else {
				own++
			}
		}
	}
	return
}

// A transcript that has not changed since its last fold is not parsed again, and an appended
// turn is folded on the next pass (#3112).
func TestFoldSkipsUnchangedTranscript(t *testing.T) {
	f := newSkipFixture(t)
	f.write(f.main, claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "r1", 1000, 10)+
		claudeUserLine(f.at(2*time.Minute))) // closes turn 1
	if n := f.fold(); n != 1 || f.parses != 1 {
		t.Fatalf("first fold = %d rows, %d parses; want 1, 1", n, f.parses)
	}
	for i := 0; i < 3; i++ {
		if n := f.fold(); n != 0 || f.parses != 1 {
			t.Fatalf("unchanged fold %d = %d rows, %d parses; want no new parse", i, n, f.parses)
		}
	}
	f.write(f.main, claudeUsageLine(f.at(3*time.Minute), "r2", 2000, 20)+claudeUserLine(f.at(4*time.Minute)))
	if n := f.fold(); n != 1 || f.parses != 2 {
		t.Fatalf("fold after an append = %d rows, %d parses; want 1, 2", n, f.parses)
	}
	if own, _ := f.rows(); own != 2 {
		t.Fatalf("ledger holds %d rows, want 2 (each turn once)", own)
	}
	// includeTrailing (deletion / settle) never skips.
	before := f.parses
	usageFoldMu.Lock()
	st := readUsageFoldState()
	_, _, err := foldSessionUsageLocked(f.m, &st, true)
	usageFoldMu.Unlock()
	if err != nil || f.parses != before+1 {
		t.Fatalf("includeTrailing: err=%v parses=%d, want a parse", err, f.parses-before)
	}
}

// A subagent transcript that grows while the main one is unchanged is still folded, and the
// main one is not parsed for it.
func TestFoldSkipStillFoldsGrowingSubagent(t *testing.T) {
	f := newSkipFixture(t)
	agent := filepath.Join(f.proj, f.sid, "subagents", "agent-a1.jsonl")
	f.write(f.main, claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "r1", 1000, 10)+claudeUserLine(f.at(2*time.Minute)))
	f.write(agent, claudeUserLine(f.at(3*time.Minute))+claudeUsageLine(f.at(4*time.Minute), "a1", 2000, 20))
	f.fold()
	f.fold()
	parses := f.parses
	if own, side := f.rows(); own != 1 || side != 1 {
		t.Fatalf("rows own=%d side=%d, want 1/1", own, side)
	}
	f.write(agent, claudeUserLine(f.at(5*time.Minute))+claudeUsageLine(f.at(6*time.Minute), "a2", 2000, 30))
	if n := f.fold(); n != 1 {
		t.Fatalf("fold after the subagent grew = %d rows, want 1", n)
	}
	if f.parses != parses {
		t.Fatalf("main transcript parsed %d more times for a subagent-only change", f.parses-parses)
	}
	if _, side := f.rows(); side != 2 {
		t.Fatalf("sidechain rows = %d, want 2", side)
	}
}

// A state.json written before the stamp existed folds once (no double count), records the stamp,
// and then skips.
func TestFoldSkipUpgradeFromStatelessMark(t *testing.T) {
	f := newSkipFixture(t)
	f.write(f.main, claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "r1", 1000, 10)+claudeUserLine(f.at(2*time.Minute)))
	f.fold()
	usageFoldMu.Lock()
	st := readUsageFoldState()
	mk := st.Sessions[f.m.Name]
	if mk.Stamp == "" {
		usageFoldMu.Unlock()
		t.Fatal("no stamp recorded after a successful fold")
	}
	mk.Stamp = "" // what an old build wrote
	st.Sessions[f.m.Name] = mk
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	f.parses = 0
	if n := f.fold(); n != 0 || f.parses != 1 {
		t.Fatalf("fold of an old mark = %d rows, %d parses; want 0, 1", n, f.parses)
	}
	f.fold()
	f.fold()
	if f.parses != 1 {
		t.Fatalf("parses = %d after the stamp was recorded, want 1", f.parses)
	}
	if own, _ := f.rows(); own != 1 {
		t.Fatalf("ledger holds %d rows, want 1", own)
	}
}

// A failed append must not leave a stamp behind: the turns have to come back as a delta.
func TestFoldSkipNoStampWhenAppendFails(t *testing.T) {
	f := newSkipFixture(t)
	dir := usagex.Dir()
	f.write(f.main, claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "r1", 1000, 10)+claudeUserLine(f.at(2*time.Minute)))
	raw := filepath.Join(dir, "raw")
	if err := os.WriteFile(raw, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := commitSessionUsageFold(f.m, false); err == nil {
		t.Fatal("append failed but no error")
	}
	if mk := readUsageFoldState().Sessions[f.m.Name]; mk.Stamp != "" || mk.Groups != 0 {
		t.Fatalf("a failed fold recorded %+v", mk)
	}
	if err := os.Remove(raw); err != nil {
		t.Fatal(err)
	}
	if n := f.fold(); n != 1 {
		t.Fatalf("fold after recovery = %d rows, want 1", n)
	}
}

// A kind with no stamp falls back to parsing every pass.
func TestFoldSkipKindWithoutStampStillParses(t *testing.T) {
	if _, ok := sessionx.UsageStamp(session.Meta{Name: "x", Kind: session.KindShell}); ok {
		t.Fatal("a kind with no stamper reported one")
	}
}

// One pass over many untouched sessions costs no transcript parse once each has been folded
// (the before/after figure in #3112's fix: 40 sessions, 5 passes = 200 parses before, 40 after).
func TestFoldSkipManyArchivedSessions(t *testing.T) {
	f := newSkipFixture(t)
	var metas []session.Meta
	for i := 0; i < 40; i++ {
		m := f.m
		m.Name = "arch" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		sid := session.UUID(m.Dir, m.Name)
		f.write(filepath.Join(f.proj, sid+".jsonl"), claudeUserLine(f.at(0))+claudeUsageLine(f.at(time.Minute), "r"+m.Name, 100, 1)+claudeUserLine(f.at(2*time.Minute)))
		metas = append(metas, m)
	}
	for pass := 0; pass < 5; pass++ {
		for _, m := range metas {
			if _, err := commitSessionUsageFold(m, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("parses over 5 passes of 40 sessions: %d", f.parses)
	if f.parses != 40 {
		t.Fatalf("parses = %d, want 40 (one per session)", f.parses)
	}
}

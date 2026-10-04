package sessionsearch

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// fakeFleet stands in for the session layer.
type fakeFleet struct {
	metas  []session.Meta
	turns  map[string][]transcript.Turn
	alive  map[string]bool
	reads  map[string]int
	probes map[string]int
}

func installFake(t *testing.T) *fakeFleet {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := &fakeFleet{turns: map[string][]transcript.Turn{}, alive: map[string]bool{}, reads: map[string]int{}, probes: map[string]int{}}
	oldList, oldTurns, oldAlive, oldCan, oldPath, oldExists := listMetas, turnsOf, aliveOf, canTranscript, indexPath, metaExists
	dir := t.TempDir()
	listMetas = func() []session.Meta { return append([]session.Meta(nil), f.metas...) }
	turnsOf = func(m session.Meta) []transcript.Turn { f.reads[m.Name]++; return f.turns[m.Name] }
	aliveOf = func(m session.Meta) bool { f.probes[m.Name]++; return f.alive[m.Name] }
	canTranscript = func(m session.Meta) bool { return m.Kind != session.KindShell }
	indexPath = func() string { return filepath.Join(dir, "index.db") }
	metaExists = func(name string) bool {
		for _, m := range f.metas {
			if m.Name == name {
				return true
			}
		}
		return false
	}
	resetStoreForTest()
	t.Cleanup(func() {
		resetStoreForTest()
		listMetas, turnsOf, aliveOf, canTranscript, indexPath, metaExists = oldList, oldTurns, oldAlive, oldCan, oldPath, oldExists
	})
	return f
}

func say(idx int, role, text string) transcript.Turn {
	return transcript.Turn{Idx: idx, Role: role, Text: text, TS: time.Now().UTC().Format(time.RFC3339)}
}

func search(t *testing.T, q string) Result {
	t.Helper()
	res, err := Search(Query{Q: q, Limit: 10, PerSession: 3})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPassSkipsSettledSessionsAndRereadsResumedOnes(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{
		{Name: "done", Kind: "claude", StoppedAt: "2026-10-01T00:00:00Z"},
		{Name: "live", Kind: "codex"},
		{Name: "term", Kind: session.KindShell},
	}
	f.alive["live"] = true
	f.turns["done"] = []transcript.Turn{say(1, "user", "migrate the database")}
	f.turns["live"] = []transcript.Turn{say(1, "user", "database index tuning")}
	for i := 0; i < 2; i++ {
		if err := runPass(); err != nil {
			t.Fatal(err)
		}
	}
	if f.reads["done"] != 1 || f.reads["live"] != 2 || f.reads["term"] != 0 {
		t.Fatalf("reads = %v: a settled session is read once, a live one every pass, a shell never", f.reads)
	}
	if res := search(t, "database"); len(res.Hits) != 2 || res.Indexed != 2 || res.Total != 2 {
		t.Fatalf("result = %+v", res)
	}
	// Resumed and stopped again: a new StoppedAt, so it is read again and the new turn is found.
	f.metas[0].StoppedAt = "2026-10-02T00:00:00Z"
	f.turns["done"] = append(f.turns["done"], say(2, "assistant", "rollback plan written"))
	_ = runPass()
	if f.reads["done"] != 2 || len(search(t, "rollback").Hits) != 1 {
		t.Fatalf("a re-stopped session must be re-read (reads %v)", f.reads)
	}
}

// An archived session is settled without a liveness probe: it cannot be running.
func TestPassDoesNotProbeArchivedSessions(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{
		{Name: "shelf", Kind: "claude", StoppedAt: "2026-09-01T00:00:00Z", Archived: true},
		{Name: "listed", Kind: "claude", StoppedAt: "2026-10-01T00:00:00Z"},
	}
	f.turns["shelf"] = []transcript.Turn{say(1, "user", "archived talk")}
	f.turns["listed"] = []transcript.Turn{say(1, "user", "listed talk")}
	_ = runPass()
	_ = runPass()
	if f.probes["shelf"] != 0 || f.probes["listed"] == 0 || f.reads["shelf"] != 1 {
		t.Fatalf("probes = %v reads = %v", f.probes, f.reads)
	}
}

func TestPassKeepsRowsWhenAStoppedSessionReadsEmpty(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{{Name: "cur", Kind: "cursor"}}
	f.alive["cur"] = true
	f.turns["cur"] = []transcript.Turn{say(1, "user", "kept conversation")}
	_ = runPass()
	f.metas[0].StoppedAt = "2026-10-02T00:00:00Z"
	f.alive["cur"] = false
	f.turns["cur"] = nil // a stopped Managed cursor session reads as empty
	_ = runPass()
	if len(search(t, "kept").Hits) != 1 {
		t.Fatal("an empty read erased indexed rows")
	}
}

func TestDeletedSessionsAreNeverFound(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{{Name: "gone", Kind: "claude"}, {Name: "here", Kind: "claude"}}
	f.turns["gone"] = []transcript.Turn{say(1, "user", "secret plan")}
	f.turns["here"] = []transcript.Turn{say(1, "user", "public plan")}
	_ = runPass()
	f.metas = f.metas[1:] // trashed after the pass
	if res := search(t, "plan"); len(res.Hits) != 1 || res.Hits[0].Session != "here" {
		t.Fatalf("a session without a meta was found before the next pass: %+v", res.Hits)
	}
	_ = runPass()
	s, _ := openStore()
	if n, _ := s.Names(); len(n) != 1 {
		t.Fatalf("the pass must prune deleted sessions: %v", n)
	}
	Forget("here")
	if n, _ := s.Names(); len(n) != 0 {
		t.Fatalf("Forget left rows: %v", n)
	}
}

// The trash can remove the meta and call Forget while the pass is reading that session's
// transcript; the pass must not write the text back afterwards.
func TestPassDoesNotRewriteASessionTrashedMidRead(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{{Name: "doomed", Kind: "claude"}}
	f.turns["doomed"] = []transcript.Turn{say(1, "user", "secret plan")}
	_ = runPass() // indexed once, so Forget has rows to remove
	f.turns["doomed"] = append(f.turns["doomed"], say(2, "user", "more secret"))
	read := turnsOf
	turnsOf = func(m session.Meta) []transcript.Turn {
		turns := read(m)
		f.metas = nil // the trash: meta removed, then Forget
		Forget(m.Name)
		return turns
	}
	_ = runPass()
	s, _ := openStore()
	if n, _ := s.Names(); len(n) != 0 {
		t.Fatalf("a session trashed mid-read was written back: %v", n)
	}
}

func TestSearchCapsPerSessionAndRanksScheduledRunsDown(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{
		{Name: "long", Kind: "claude", Repo: "app"},
		{Name: "cron", Kind: "codex", Origin: session.OriginSchedule, Repo: "app"},
		{Name: "other", Kind: "codex", Repo: "lib"},
	}
	for i := 0; i < 6; i++ {
		f.turns["long"] = append(f.turns["long"], say(i, "user", "flaky test again"))
	}
	f.turns["cron"] = []transcript.Turn{say(1, "user", "flaky test")}
	f.turns["other"] = []transcript.Turn{say(1, "user", "flaky test")}
	_ = runPass()
	res := search(t, "flaky")
	per := map[string]int{}
	for _, h := range res.Hits {
		per[h.Session]++
	}
	if per["long"] != 3 || per["cron"] != 1 || per["other"] != 1 {
		t.Fatalf("per-session counts = %v", per)
	}
	pos := map[string]int{}
	for i, h := range res.Hits {
		if _, ok := pos[h.Session]; !ok {
			pos[h.Session] = i
		}
	}
	if pos["cron"] < pos["other"] {
		t.Fatalf("the scheduled run outranked an identical attended turn: %+v", res.Hits)
	}
	r2, _ := Search(Query{Q: "flaky", Limit: 10, PerSession: 3, Repo: "lib"})
	if len(r2.Hits) != 1 || r2.Hits[0].Session != "other" {
		t.Fatalf("repo filter: %+v", r2.Hits)
	}
	r3, _ := Search(Query{Q: "flaky", Limit: 10, PerSession: 3, Session: "cron"})
	if len(r3.Hits) != 1 || r3.Hits[0].Session != "cron" {
		t.Fatalf("session filter: %+v", r3.Hits)
	}
}

func TestScoreHalvesWithAge(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	fresh := score(-2, now.Format(time.RFC3339), "user", now)
	old := score(-2, now.AddDate(0, 0, -180).Format(time.RFC3339), "user", now)
	if fresh != 2 || math.Abs(old-1) > 1e-9 {
		t.Fatalf("fresh %v old %v", fresh, old)
	}
	if got := score(-2, "", session.OriginSchedule, now); got != 1 {
		t.Fatalf("schedule weight: %v", got)
	}
}

func TestHandlersHonourTheSwitchForSessionsOnly(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{{Name: "s1", Kind: "claude"}}
	f.turns["s1"] = []transcript.Turn{say(4, "user", "hello world"), say(5, "assistant", "hi there")}
	_ = runPass()
	get := func(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", target, nil))
		return rec
	}
	if rec := get(HandleSearch, "/session-search?q=hello&from=s1"); rec.Code != 200 {
		t.Fatalf("default-on switch refused: %d %s", rec.Code, rec.Body)
	}
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiprefs.Path(), []byte(`{"sessionSearch":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := get(HandleSearch, "/session-search?q=hello&from=s1"); rec.Code != 403 {
		t.Fatalf("switched off, a session's call must be refused: %d", rec.Code)
	}
	if rec := get(HandleWindow, "/session-search/turns?session=s1&idx=4&from=s1"); rec.Code != 403 {
		t.Fatalf("switched off, a session's read must be refused: %d", rec.Code)
	}
	if rec := get(HandleSearch, "/session-search?q=hello"); rec.Code != 200 {
		t.Fatalf("the Console's own call is not governed by the switch: %d", rec.Code)
	}
	if rec := get(HandleWindow, "/session-search/turns?session=s1&idx=4&after=1"); rec.Code != 200 {
		t.Fatalf("window: %d %s", rec.Code, rec.Body)
	}
	if rec := get(HandleWindow, "/session-search/turns?session=nope&idx=4"); rec.Code != 404 {
		t.Fatalf("unknown session: %d", rec.Code)
	}
	if rec := get(HandleSearch, "/session-search?q=%20"); rec.Code != 400 {
		t.Fatalf("empty query: %d", rec.Code)
	}
}

func TestReadWindowCapsBytes(t *testing.T) {
	f := installFake(t)
	f.metas = []session.Meta{{Name: "big", Kind: "claude"}}
	for i := 0; i < 5; i++ {
		f.turns["big"] = append(f.turns["big"], say(i, "user", repeat("x", 10*1024)))
	}
	_ = runPass()
	w, err := ReadWindow("big", 2, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range w.Turns {
		total += len(d.Text)
	}
	if !w.Clipped || total > maxWindowBytes+len("…") {
		t.Fatalf("clipped=%v total=%d", w.Clipped, total)
	}
}

func repeat(s string, n int) string {
	b := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}

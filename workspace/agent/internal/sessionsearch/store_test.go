package sessionsearch

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustMatch(t *testing.T, s *Store, q string) []rawHit {
	t.Helper()
	expr, err := MatchQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range held {
		names = append(names, n)
	}
	hits, err := s.match(expr, matchOpts{Sessions: names, Now: time.Now(), PerSession: 100, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func rowIDs(t *testing.T, s *Store, name string) []int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT id FROM turns WHERE session=? ORDER BY ord`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestStoreFindsTwoCharacterJapaneseAndEnglish(t *testing.T) {
	s := openTestStore(t)
	if err := s.Apply("s1", "claude", []Doc{
		{Idx: 1, Role: "user", Text: "前回の認証エラーを直したい"},
		{Idx: 2, Role: "assistant", Text: "The token refresh was missing in auth.go"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"認証": 1, "認証エラー": 1, "証エ": 1, "エラー 認証": 1, "認": 1, "Token": 1, "tok*": 1, "auth.go": 1, "認証 token": 0, "エラ認証": 0} {
		if got := len(mustMatch(t, s, q)); got != want {
			t.Errorf("%q: %d hits, want %d", q, got, want)
		}
	}
}

func TestStoreApplyAppendsWhenTheTranscriptGrew(t *testing.T) {
	s := openTestStore(t)
	docs := []Doc{{Idx: 1, Role: "user", Text: "alpha"}, {Idx: 2, Role: "assistant", Text: "bravo partial"}}
	if err := s.Apply("s1", "codex", docs, ""); err != nil {
		t.Fatal(err)
	}
	before := rowIDs(t, s, "s1")
	// The last row was still streaming; it grew, and a new turn followed.
	docs = []Doc{docs[0], {Idx: 2, Role: "assistant", Text: "bravo finished"}, {Idx: 3, Role: "user", Text: "charlie"}}
	if err := s.Apply("s1", "codex", docs, ""); err != nil {
		t.Fatal(err)
	}
	after := rowIDs(t, s, "s1")
	if len(after) != 3 || after[0] != before[0] || after[1] == before[1] {
		t.Fatalf("expected the first row kept and the rest rewritten: before %v after %v", before, after)
	}
	if len(mustMatch(t, s, "partial")) != 0 || len(mustMatch(t, s, "finished")) != 1 || len(mustMatch(t, s, "charlie")) != 1 {
		t.Fatal("index does not reflect the grown transcript")
	}
	// Unchanged: nothing is rewritten.
	if err := s.Apply("s1", "codex", docs, ""); err != nil {
		t.Fatal(err)
	}
	if again := rowIDs(t, s, "s1"); again[1] != after[1] || again[2] != after[2] {
		t.Fatalf("an unchanged transcript was rewritten: %v -> %v", after, again)
	}
}

func TestStoreApplyReplacesADifferentConversation(t *testing.T) {
	s := openTestStore(t)
	_ = s.Apply("s1", "claude", []Doc{{Idx: 1, Role: "user", Text: "old one"}, {Idx: 2, Role: "assistant", Text: "old two"}, {Idx: 3, Role: "user", Text: "old three"}}, "")
	if err := s.Apply("s1", "claude", []Doc{{Idx: 1, Role: "user", Text: "new one"}}, ""); err != nil {
		t.Fatal(err)
	}
	if len(mustMatch(t, s, "old")) != 0 || len(mustMatch(t, s, "new")) != 1 {
		t.Fatal("a shorter, different transcript must replace the rows")
	}
	if n, _ := s.Names(); n["s1"] != 1 {
		t.Fatalf("docs count = %v", n)
	}
}

func TestStoreForgetAndWindow(t *testing.T) {
	s := openTestStore(t)
	var docs []Doc
	for i := 0; i < 9; i++ {
		docs = append(docs, Doc{Idx: i * 2, Role: "user", Text: "turn " + string(rune('a'+i))})
	}
	_ = s.Apply("s1", "claude", docs, "")
	_ = s.Apply("s2", "claude", []Doc{{Idx: 0, Role: "user", Text: "turn other"}}, "")
	win, err := s.window("s1", 8, 1, 2) // idx 8 is ord 4
	if err != nil || len(win) != 4 || win[0].Idx != 6 || win[3].Idx != 12 {
		t.Fatalf("window = %+v, %v", win, err)
	}
	if win, _ := s.window("s1", 9, 0, 0); len(win) != 1 || win[0].Idx != 8 {
		t.Fatalf("an idx between rows centres on the earlier one: %+v", win)
	}
	if err := s.Forget("s1"); err != nil {
		t.Fatal(err)
	}
	if hits := mustMatch(t, s, "turn"); len(hits) != 1 || hits[0].Session != "s2" {
		t.Fatalf("after forget: %+v", hits)
	}
	if n, _ := s.Names(); len(n) != 1 {
		t.Fatalf("names after forget: %v", n)
	}
}

func TestStoreRebuildsOnSchemaChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Apply("s1", "claude", []Doc{{Idx: 1, Role: "user", Text: "hello"}}, "")
	if _, err := s.db.Exec(`UPDATE meta SET v='0' WHERE k='schema'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, _ := s.Names(); len(n) != 0 {
		t.Fatalf("an index of another schema must be dropped: %v", n)
	}
}

// Two transcripts that end alike but differ earlier are different conversations: the earlier
// text must not stay searchable, with or without new turns after it.
func TestStoreApplyNoticesAnEditedPrefix(t *testing.T) {
	for _, appendOne := range []bool{false, true} {
		s := openTestStore(t)
		docs := []Doc{{Idx: 1, Role: "user", Text: "oldsecret"}, {Idx: 2, Role: "user", Text: "middle"},
			{Idx: 3, Role: "user", Text: "anchor"}, {Idx: 4, Role: "user", Text: "tail"}}
		if err := s.Apply("s1", "claude", docs, ""); err != nil {
			t.Fatal(err)
		}
		docs[0].Text = "replacement"
		if appendOne {
			docs = append(docs, Doc{Idx: 5, Role: "user", Text: "more"})
		}
		if err := s.Apply("s1", "claude", docs, ""); err != nil {
			t.Fatal(err)
		}
		if len(mustMatch(t, s, "oldsecret")) != 0 || len(mustMatch(t, s, "replacement")) != 1 {
			t.Fatalf("append=%v: an edited first turn left the old text searchable", appendOne)
		}
		if got := len(mustMatch(t, s, "middle")); got != 1 {
			t.Fatalf("append=%v: %d rows for an unchanged turn", appendOne, got)
		}
	}
}

// Round trips through the real SQLite: what the text holds, the same words as a query find.
func TestStoreMatchesFoldedAndNumberLikeText(t *testing.T) {
	s := openTestStore(t)
	_ = s.Apply("s1", "claude", []Doc{
		{Idx: 1, Role: "user", Text: "バグ修正 stageⅢ ①foo 一〇二"},
		{Idx: 2, Role: "user", Text: "ﾃﾞｰﾀﾍﾞｰｽ"},
	}, "")
	for _, q := range []string{"ﾊﾞｸﾞ", "バグ", "stageⅢ", "①foo", "一〇二", "データベース", "ﾃﾞｰﾀ"} {
		if got := len(mustMatch(t, s, q)); got != 1 {
			t.Errorf("%q: %d hits, want 1", q, got)
		}
	}
}

// One session with hundreds of strong matches must not push a filtered session's only match out.
func TestStoreMatchFiltersAndCapsBeforeTheLimit(t *testing.T) {
	s := openTestStore(t)
	var many []Doc
	for i := 0; i < candidatePool+1; i++ {
		many = append(many, Doc{Idx: i, Role: "user", Text: "flaky"})
	}
	_ = s.Apply("loud", "claude", many, "")
	_ = s.Apply("quiet", "claude", []Doc{{Idx: 1, Role: "user", Text: "a much longer turn that mentions flaky only once among many other words"}}, "")
	expr, _ := MatchQuery("flaky")
	hits, err := s.match(expr, matchOpts{Sessions: []string{"quiet"}, Now: time.Now(), PerSession: 3, Limit: candidatePool})
	if err != nil || len(hits) != 1 || hits[0].Session != "quiet" {
		t.Fatalf("filtered: %d hits, %v", len(hits), err)
	}
	hits, _ = s.match(expr, matchOpts{Sessions: []string{"loud", "quiet"}, Now: time.Now(), PerSession: 3, Limit: candidatePool})
	per := map[string]int{}
	for _, h := range hits {
		per[h.Session]++
	}
	if per["loud"] != 3 || per["quiet"] != 1 {
		t.Fatalf("per-session cap in SQL: %v", per)
	}
}

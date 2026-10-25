package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func changesPageGet(t *testing.T, query string) (int, agentMemChangesWire) {
	t.Helper()
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/changes"+query, nil)
	var w agentMemChangesWire
	_ = json.Unmarshal(b, &w)
	return code, w
}

// Reading the history page by page with the cursor lists every change once, in the order one big
// read gives, and a change published between two pages neither repeats a row nor skips one.
func TestAgentMemoryChangesPagingWalksTheWholeHistory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		saveScoped(t, c, "m"+strconv.Itoa(i), "user", now.Add(time.Duration(i)*time.Minute))
	}
	all := agentMemChangesT(t)
	if len(all) != 7 {
		t.Fatalf("fixture: %d changes", len(all))
	}

	code, p1 := changesPageGet(t, "?limit=3")
	if code != http.StatusOK || len(p1.Changes) != 3 || p1.Next == "" {
		t.Fatalf("page 1: %d %+v", code, p1)
	}
	// A write between the pages lands ahead of the cursor.
	saveScoped(t, c, "late", "user", now.Add(time.Hour))
	var got []string
	for _, v := range p1.Changes {
		got = append(got, v.Commit)
	}
	next := p1.Next
	for pages := 0; next != "" && pages < 10; pages++ {
		code, p := changesPageGet(t, "?limit=3&before="+next)
		if code != http.StatusOK {
			t.Fatalf("page: %d", code)
		}
		for _, v := range p.Changes {
			got = append(got, v.Commit)
		}
		next = p.Next
	}
	if len(got) != 7 {
		t.Fatalf("walked %d rows, want 7: %v", len(got), got)
	}
	for i, v := range all {
		if got[i] != v.Commit {
			t.Fatalf("row %d: got %s want %s", i, got[i], v.Commit)
		}
	}
}

// A page after the first still decides "newest change of a memory" over what is newer than it, so
// the older row of a memory that changed after the cursor is not offered a revert.
func TestAgentMemoryChangesPagingMarksLatestAcrossPages(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	saveScoped(t, c, "twice", "user", now)
	for i := 0; i < 3; i++ {
		saveScoped(t, c, "f"+strconv.Itoa(i), "user", now.Add(time.Duration(i+1)*time.Minute))
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "twice", Description: "d twice", Scope: "user", Body: "b2", Revision: 1}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, p1 := changesPageGet(t, "?limit=2")
	_, p2 := changesPageGet(t, "?limit=2&before="+p1.Next)
	_, p3 := changesPageGet(t, "?limit=2&before="+p2.Next)
	if p3.Next != "" || len(p3.Changes) != 1 || p3.Changes[0].Name != "twice" || p3.Changes[0].Latest {
		t.Fatalf("older row of a memory changed since: %+v", p3)
	}
	if !p1.Changes[0].Latest || p1.Changes[0].Name != "twice" {
		t.Fatalf("newest row: %+v", p1.Changes[0])
	}
}

func TestAgentMemoryChangesPagingRefusesABadCursor(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	saveScoped(t, agentMemCallerT(t, "claude-main"), "a", "user", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	if code, _ := changesPageGet(t, "?before=not-a-commit"); code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", code)
	}
	if code, _ := changesPageGet(t, "?before=0123456789abcdef0123456789abcdef01234567"); code != http.StatusNotFound {
		t.Fatalf("unknown commit: %d", code)
	}
}

// The forgotten list pages by (time, path): every memory once, the newest first, and one forgotten
// between two pages moves to the front rather than repeating or skipping an older row.
func TestAgentMemoryMemberForgottenPaging(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	setForgottenMax(t, 2)
	for i, n := range []string{"a", "b", "c", "d", "e"} {
		forgeForgotten(t, "user", n, "2026-02-0"+strconv.Itoa(i+1)+"T00:00:00Z", false)
	}
	// Two forgotten in the same second: the path decides, so the order is total.
	forgeForgotten(t, "user", "tie-y", "2026-01-01T00:00:00Z", false)
	forgeForgotten(t, "user", "tie-x", "2026-01-01T00:00:00Z", false)

	code, p1, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(p1) != "e,d" || p1.Next == "" {
		t.Fatalf("page 1: %d %s", code, body)
	}
	forgeForgotten(t, "user", "fresh", "2026-03-01T00:00:00Z", false)
	var got []string
	got = append(got, namesOf(p1))
	next := p1.Next
	for pages := 0; next != "" && pages < 10; pages++ {
		code, p, body := forgottenGet(t, "?before="+next)
		if code != http.StatusOK {
			t.Fatalf("page: %d %s", code, body)
		}
		got = append(got, namesOf(p))
		next = p.Next
	}
	// Equal times are ordered by the digest of the path (the cursor's own key).
	first, second := "tie-x", "tie-y"
	if agentMemTombDigest("user/tie-y.md") < agentMemTombDigest("user/tie-x.md") {
		first, second = second, first
	}
	want := []string{"e,d", "c,b", "a," + first, second}
	if len(got) != len(want) {
		t.Fatalf("pages %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pages %v, want %v", got, want)
		}
	}
	// "fresh" came after page 1 and sorts ahead of it: the first page now starts with it.
	if _, p, _ := forgottenGet(t, ""); namesOf(p) != "fresh,e" {
		t.Fatalf("new first page: %s", namesOf(p))
	}
}

func TestAgentMemoryMemberForgottenPagingRefusesABadCursor(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	for _, b := range []string{"x", "2026-01-01|", "nope|user/a.md", "2026-01-01T00:00:00Z|user/a.md"} {
		if code, _, _ := forgottenGet(t, "?before="+b); code != http.StatusBadRequest {
			t.Fatalf("%q: %d", b, code)
		}
	}
}

// The cursor never carries a path: a candidate withheld at the judging-budget boundary (a
// token-shaped project id, or a name whose text fails the scan) must not leak through `next`, and
// the next page still advances past it, even when every row of a page is withheld.
func TestAgentMemoryMemberForgottenCursorLeaksNoWithheldPath(t *testing.T) {
	secretID := "ghp_" + strings.Repeat("q7zx", 9)
	if agentMemCleanText(secretID) || !agentMemValidProjectID(secretID) {
		t.Fatalf("fixture: %q", secretID)
	}
	_, _, _ = agentMemTestEnv(t)
	old := agentMemTombJudgeMax
	agentMemTombJudgeMax = 1
	t.Cleanup(func() { agentMemTombJudgeMax = old })
	if _, err := agentMemCheckDir("projects/"+secretID, true); err != nil {
		t.Fatal(err)
	}
	forgeForgotten(t, "projects/"+secretID, "hidden", "2026-03-01T00:00:00Z", false)
	forgeForgotten(t, "user", "leaky", "2026-02-01T00:00:00Z", true) // withheld by its text
	forgeForgotten(t, "user", "older", "2026-01-01T00:00:00Z", false)
	var got []string
	next, pages := "", 0
	for {
		q := ""
		if next != "" {
			q = "?before=" + next
		}
		code, w, body := forgottenGet(t, q)
		if code != http.StatusOK || strings.Contains(body, secretID) || strings.Contains(body, "leaky") && w.Next != "" && strings.Contains(w.Next, "leaky") {
			t.Fatalf("page %d: %d %s", pages, code, body)
		}
		got = append(got, namesOf(w))
		if next = w.Next; next == "" || pages > 5 {
			break
		}
		pages++
	}
	if strings.Join(got, "|") != "||older" {
		t.Fatalf("pages %v: a page of only withheld rows must still advance", got)
	}
}

// A cursor is a point in the list order, not a row: when the row it named is gone (unforgotten) or
// moved (forgotten again), the same-time rows after it are still reached, none skipped.
func TestAgentMemoryMemberForgottenCursorSurvivesItsRowChanging(t *testing.T) {
	for _, how := range []string{"removed", "moved"} {
		t.Run(how, func(t *testing.T) {
			_, _, _ = agentMemTestEnv(t)
			setForgottenMax(t, 1)
			for _, n := range []string{"a", "b", "c"} {
				forgeForgotten(t, "user", n, "2026-02-01T00:00:00Z", false)
			}
			_, p1, _ := forgottenGet(t, "")
			if len(p1.Forgotten) != 1 || p1.Next == "" {
				t.Fatalf("page 1: %+v", p1)
			}
			gone := p1.Forgotten[0].Name
			if how == "removed" {
				tomb, _ := agentMemCheckDir("user/"+agentMemTombDir, false)
				if err := os.Remove(filepath.Join(tomb, gone)); err != nil {
					t.Fatal(err)
				}
			} else {
				forgeForgotten(t, "user", gone, "2026-04-01T00:00:00Z", false)
			}
			var rest []string
			for next, n := p1.Next, 0; next != "" && n < 5; n++ {
				_, p, _ := forgottenGet(t, "?before="+next)
				for _, v := range p.Forgotten {
					rest = append(rest, v.Name)
				}
				next = p.Next
			}
			if len(rest) != 2 {
				t.Fatalf("%s: %q was page 1; later pages gave %v, want the other two", how, gone, rest)
			}
		})
	}
}

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

func forgottenGet(t *testing.T, query string) (int, agentMemForgottenWire, string) {
	t.Helper()
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/member-forgotten"+query, nil)
	var w agentMemForgottenWire
	_ = json.Unmarshal(b, &w)
	return code, w, string(b)
}

func saveScoped(t *testing.T, c agentMemCaller, name, scope string, at time.Time) {
	t.Helper()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d " + name, Scope: scope, Body: "b"}, at); err != nil {
		t.Fatal(err)
	}
}

func forgetViaRevert(t *testing.T, name, scope string, c agentMemCaller, at time.Time) {
	t.Helper()
	rel := "user/" + name + ".md"
	if scope == agentMemScopeProject {
		rel = "projects/" + c.Project.ID + "/" + name + ".md"
	}
	commit, err := agentMemLatestChange(rel)
	if err != nil || commit == "" {
		t.Fatalf("latest of %s: %q %v", rel, commit, err)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: commit, Forget: true}, at); err != nil {
		t.Fatalf("forget %s: %v", name, err)
	}
}

// A memory forgotten before any window of the changes list is listed from its tombstone, in
// every scope or in one, with who forgot it and a commit a restore can name. Without the route
// the only source is the 300 newest changes.
func TestAgentMemoryMemberForgottenListsOutsideTheWindow(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	saveScoped(t, c, "old-proj", "project", now)
	saveScoped(t, c, "old-user", "user", now)
	saveScoped(t, c, "kept", "user", now)
	forgetViaRevert(t, "old-proj", "project", c, now.Add(time.Minute))
	forgetViaRevert(t, "old-user", "user", c, now.Add(2*time.Minute))
	for i, n := range []string{"n1", "n2", "n3"} {
		saveScoped(t, c, n, "project", now.Add(time.Duration(i+3)*time.Minute))
	}
	win, err := agentMemListChanges(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range win.Changes {
		if strings.HasPrefix(ch.Name, "old-") {
			t.Fatalf("fixture: a forget is inside the window: %+v", win.Changes)
		}
	}
	AgentMemoryEnabled = func() bool { return false }

	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || len(w.Forgotten) != 2 || w.Withheld != 0 || w.Truncated {
		t.Fatalf("all scopes: %d %s", code, body)
	}
	if w.Forgotten[0].Name != "old-user" || w.Forgotten[1].Name != "old-proj" {
		t.Fatalf("newest forget first: %s", body)
	}
	p := w.Forgotten[1]
	if p.Op != "forget" || p.Live || !p.Revertible || !p.Latest || p.Project == nil || p.Project.ID != c.Project.ID ||
		p.AuthorKind != "member" || p.AuthorSession != "console" || p.Commit == "" || p.At == "" {
		t.Fatalf("project row: %+v", p)
	}
	if code, w, body = forgottenGet(t, "?scope=user"); code != http.StatusOK || len(w.Forgotten) != 1 || w.Forgotten[0].Name != "old-user" {
		t.Fatalf("user scope: %d %s", code, body)
	}
	if code, w, body = forgottenGet(t, "?scope=project&project="+c.Project.ID); code != http.StatusOK || len(w.Forgotten) != 1 || w.Forgotten[0].Name != "old-proj" {
		t.Fatalf("project scope: %d %s", code, body)
	}
	for _, q := range []string{"?scope=other", "?scope=project&project=nope", "?scope=project&project=../x"} {
		if code, _, _ := forgottenGet(t, q); code < 400 {
			t.Errorf("%s: status %d", q, code)
		}
	}
	// The listed commit goes through the existing revert and brings the memory back; the list
	// then no longer has it.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: p.Commit}, now.Add(time.Hour)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, w, body = forgottenGet(t, ""); len(w.Forgotten) != 1 || w.Forgotten[0].Name != "old-user" {
		t.Fatalf("after restore: %s", body)
	}
	if got := agentMemLoadUsage("projects/" + c.Project.ID)["old-proj"]; got != 0 {
		t.Errorf("listing counted a use: %d", got)
	}
}

// Tombstones are untrusted files: a symlink, a directory, an oversized file or a name that is
// not a memory name is no tombstone, and a name that is a secret is counted but never listed.
func TestAgentMemoryMemberForgottenRefusesUntrustedTombstones(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	saveScoped(t, c, "gone", "user", now)
	forgetViaRevert(t, "gone", "user", c, now.Add(time.Minute))
	saveScoped(t, c, "linked", "user", now)
	forgetViaRevert(t, "linked", "user", c, now.Add(2*time.Minute))
	tomb := filepath.Join(agentMemDir(), "user", agentMemTombDir)
	// "linked" becomes a short symlink (its own size is under the size limit, so only the
	// type check can refuse it); "big" is oversized; "Bad Name" is not a name; the AWS-looking
	// name is a secret.
	if err := os.Remove(filepath.Join(tomb, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gone", filepath.Join(tomb, "linked")); err != nil {
		t.Fatal(err)
	}
	// All lowercase and a valid name, so only the scan can refuse it (the upper-case AWS fixture
	// would fail the name check instead).
	secretName := "ghp_" + strings.Repeat("q7zx", 9)
	if agentMemCleanText(secretName) || !agentMemValidName(secretName) {
		t.Fatalf("fixture: %q must be a valid name that the scanner rejects", secretName)
	}
	for n, content := range map[string]string{"big": strings.Repeat("1", 4096), "Bad Name": "1\n", secretName: "1\n"} {
		if err := os.WriteFile(filepath.Join(tomb, n), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(tomb, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || len(w.Forgotten) != 1 || w.Forgotten[0].Name != "gone" {
		t.Fatalf("untrusted tombstones: %d %s", code, body)
	}
	if w.Withheld < 1 || strings.Contains(body, secretName) || strings.Contains(body, "linked") || strings.Contains(body, "Bad Name") {
		t.Fatalf("withheld name leaked or uncounted: %s", body)
	}
	// A symlinked .forgotten directory lists nothing.
	if err := os.RemoveAll(tomb); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), tomb); err != nil {
		t.Fatal(err)
	}
	if code, w, body = forgottenGet(t, "?scope=user"); code != http.StatusOK || len(w.Forgotten) != 0 {
		t.Fatalf("symlinked tombstone dir: %d %s", code, body)
	}
}

// A forgotten memory whose earlier text hides a secret behind a JSON escape is counted, not
// listed; a memory that was re-created (live again) is not listed even if a tombstone lingers.
func TestAgentMemoryMemberForgottenWithholdsAndSkipsLive(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	trailers := func(op, name string) string {
		return "agent-memory: " + op + "\n\nAF-Trigger: agent-memory\nAF-Op: " + op + "\nAF-Memory: af/user/" + name + ".md\nAF-Author-Kind: claude\nAF-Author-Session: s\n"
	}
	agentMemCommitRaw(t, map[string]string{"af/user/esc.md": "---\nname: esc\ndescription: \"key \\u0041" + agentMemFakeAWS()[1:] + "\"\nrevision: 1\n---\nb\n"}, trailers("create", "esc"))
	if _, err := memoryGitRun("rm", "--quiet", "--", "af/user/esc.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", trailers("forget", "esc")); err != nil {
		t.Fatal(err)
	}
	tomb, err := agentMemCheckDir("user/"+agentMemTombDir, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"esc", "never-published"} {
		if err := os.WriteFile(filepath.Join(tomb, n), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saveScoped(t, c, "back", "user", now)
	if err := os.WriteFile(filepath.Join(tomb, "back"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, w, body := forgottenGet(t, "?scope=user")
	if code != http.StatusOK || len(w.Forgotten) != 0 || w.Withheld != 1 || strings.Contains(body, "esc") || strings.Contains(body, "back") {
		t.Fatalf("withheld and live: %d %s", code, body)
	}
}

// forgeForgotten writes a create and a forget of rel/name with the given author time, and the
// tombstone: a forgotten memory whose time the test controls. secret puts a token in the text
// that the forget removed.
func forgeForgotten(t *testing.T, rel, name, when string, secret bool) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_DATE", when)
	t.Setenv("GIT_COMMITTER_DATE", when)
	desc := "d"
	if secret {
		desc = agentMemFakeAWS()
	}
	path := "af/" + rel + "/" + name + ".md"
	tr := func(op string) string {
		return "agent-memory: " + op + "\n\nAF-Trigger: agent-memory\nAF-Op: " + op + "\nAF-Memory: " + path + "\nAF-Author-Kind: claude\nAF-Author-Session: s\n" + agentMemSeqTrailer + ": " + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n"
	}
	agentMemCommitRaw(t, map[string]string{path: "---\nname: " + name + "\ndescription: " + desc + "\nrevision: 1\n---\nb\n"}, tr("create"))
	if _, err := memoryGitRun("rm", "--quiet", "--", path); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", tr("forget")); err != nil {
		t.Fatal(err)
	}
	dir, err := agentMemCheckDir(rel+"/"+agentMemTombDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setForgottenMax(t *testing.T, n int) {
	old := agentMemForgottenMax
	agentMemForgottenMax = n
	t.Cleanup(func() { agentMemForgottenMax = old })
}

func namesOf(w agentMemForgottenWire) string {
	var ns []string
	for _, v := range w.Forgotten {
		ns = append(ns, v.Name)
	}
	return strings.Join(ns, ",")
}

// The cut keeps the newest forgotten memories, whatever their names sort like: an old "a" must not
// push out a newer "z". Chosen by scope and name first, the answer would hold a, b0, b1.
func TestAgentMemoryMemberForgottenCutKeepsTheNewest(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	setForgottenMax(t, 3)
	forgeForgotten(t, "user", "a", "2026-01-01T00:00:00Z", false)
	for i, n := range []string{"b0", "b1", "b2", "b3"} {
		forgeForgotten(t, "user", n, "2026-02-0"+string(rune('1'+i))+"T00:00:00Z", false)
	}
	forgeForgotten(t, "user", "z", "2026-03-01T00:00:00Z", false)
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(w) != "z,b3,b2" || !w.Truncated {
		t.Fatalf("cut: %d %s", code, body)
	}
}

// Truncated says an eligible row was left out; candidates that are live, unpublished or withheld
// do not make it true, and the withheld count covers the whole scan, not only the part before the cut.
func TestAgentMemoryMemberForgottenTruncatedOnlyForEligibleRows(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	setForgottenMax(t, 3)
	forgeForgotten(t, "user", "a", "2026-02-01T00:00:00Z", false)
	forgeForgotten(t, "user", "b", "2026-02-02T00:00:00Z", false)
	forgeForgotten(t, "user", "c", "2026-02-03T00:00:00Z", false)
	forgeForgotten(t, "user", "leaky", "2026-01-01T00:00:00Z", true) // oldest: after the cut
	saveScoped(t, c, "live", "user", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	tomb, _ := agentMemCheckDir("user/"+agentMemTombDir, true)
	for _, n := range []string{"live", "unpublished"} {
		if err := os.WriteFile(filepath.Join(tomb, n), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(w) != "c,b,a" || w.Truncated || w.Withheld != 1 {
		t.Fatalf("exact fit: %d %s", code, body)
	}
	forgeForgotten(t, "user", "d", "2026-02-04T00:00:00Z", false)
	if _, w, body = forgottenGet(t, ""); namesOf(w) != "d,c,b" || !w.Truncated || w.Withheld != 1 {
		t.Fatalf("one over: %s", body)
	}
}

// A project whose forgotten rows did not fit is still named, so the Console can offer it.
func TestAgentMemoryMemberForgottenNamesProjectsBeyondTheCut(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	setForgottenMax(t, 1)
	proj := "projects/" + c.Project.ID
	if _, err := agentMemCheckDir(proj, true); err != nil {
		t.Fatal(err)
	}
	forgeForgotten(t, "user", "u", "2026-03-01T00:00:00Z", false)
	forgeForgotten(t, proj, "p", "2026-01-01T00:00:00Z", false)
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(w) != "u" || !w.Truncated || len(w.Projects) != 1 || w.Projects[0].ID != c.Project.ID {
		t.Fatalf("projects beyond the cut: %d %s", code, body)
	}
	if _, w, body = forgottenGet(t, "?scope=project&project="+c.Project.ID); namesOf(w) != "p" || w.Truncated {
		t.Fatalf("project scope: %s", body)
	}
}

// Tombstones without a published change cost nothing against the judging budget and cannot hide a
// later scope: with more of them than the budget, the project that sorts after them is still
// listed and named.
func TestAgentMemoryMemberForgottenUnpublishedTombstonesSpendNoBudget(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	old := agentMemTombJudgeMax
	agentMemTombJudgeMax = 5
	t.Cleanup(func() { agentMemTombJudgeMax = old })
	proj := "projects/" + c.Project.ID
	if _, err := agentMemCheckDir(proj, true); err != nil {
		t.Fatal(err)
	}
	forgeForgotten(t, proj, "only-gone", "2026-03-01T00:00:00Z", false)
	tomb, _ := agentMemCheckDir("user/"+agentMemTombDir, true)
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(tomb, "junk"+string(rune('a'+i))), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(w) != "only-gone" || w.Truncated || w.Incomplete || len(w.Projects) != 1 {
		t.Fatalf("unpublished tombstones: %d %s", code, body)
	}
}

// Past the judging budget the oldest published tombstones are left unjudged and the answer says
// so: the newest are judged first, so an old "a" never displaces a newer "z", and the project of
// an unjudged memory is still offered.
func TestAgentMemoryMemberForgottenBudgetSpendsOnTheNewestFirst(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	old := agentMemTombJudgeMax
	agentMemTombJudgeMax = 2
	t.Cleanup(func() { agentMemTombJudgeMax = old })
	proj := "projects/" + c.Project.ID
	if _, err := agentMemCheckDir(proj, true); err != nil {
		t.Fatal(err)
	}
	forgeForgotten(t, "user", "a", "2026-01-01T00:00:00Z", false)
	forgeForgotten(t, "user", "b", "2026-02-01T00:00:00Z", false)
	forgeForgotten(t, "user", "z", "2026-03-01T00:00:00Z", false)
	forgeForgotten(t, proj, "oldest", "2025-12-01T00:00:00Z", false)
	code, w, body := forgottenGet(t, "")
	if code != http.StatusOK || namesOf(w) != "z,b" || w.Truncated || !w.Incomplete || len(w.Projects) != 1 || w.Projects[0].ID != c.Project.ID {
		t.Fatalf("budget: %d %s", code, body)
	}
	// The unjudged rest is reachable: the next page starts after the last judged memory.
	if w.Next == "" {
		t.Fatalf("budget: no cursor for the unjudged rest: %s", body)
	}
	if _, p2, body2 := forgottenGet(t, "?before="+w.Next); namesOf(p2) != "a,oldest" || p2.Next != "" {
		t.Fatalf("budget, next page: %s", body2)
	}
}

// A token-shaped project id passes the form check, so the id of an unjudged (over-budget) memory
// must be scanned before it is named, as the judged path does: withheld either way, never listed.
func TestAgentMemoryMemberForgottenWithholdsSecretProjectIDOutsideTheBudget(t *testing.T) {
	secretID := "ghp_" + strings.Repeat("q7zx", 9)
	if agentMemCleanText(secretID) || !agentMemValidProjectID(secretID) {
		t.Fatalf("fixture: %q must be a valid project id that the scanner rejects", secretID)
	}
	for _, budget := range []int{1, 2} {
		t.Run(string(rune('0'+budget)), func(t *testing.T) {
			_, _, _ = agentMemTestEnv(t)
			old := agentMemTombJudgeMax
			agentMemTombJudgeMax = budget
			t.Cleanup(func() { agentMemTombJudgeMax = old })
			proj := "projects/" + secretID
			if _, err := agentMemCheckDir(proj, true); err != nil {
				t.Fatal(err)
			}
			forgeForgotten(t, proj, "old", "2026-01-01T00:00:00Z", false)
			forgeForgotten(t, "user", "fresh", "2026-03-01T00:00:00Z", false)
			code, w, body := forgottenGet(t, "")
			if code != http.StatusOK || strings.Contains(body, secretID) || len(w.Projects) != 0 || w.Withheld != 1 || namesOf(w) != "fresh" {
				t.Fatalf("budget %d: %d %s", budget, code, body)
			}
		})
	}
}

// Commit times have one-second resolution, so two forgets in one second tie on time. They list by
// commit order, newest first, whichever way the path digests happen to sort: the names are picked
// so that the digest order alone would put the earlier forget first.
func TestAgentMemoryMemberForgottenSameSecondGoesByCommitOrder(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	const when = "2026-10-04T09:00:00Z"
	older, newer := "tie-a", "tie-b"
	if agentMemTombDigest("user/"+older+".md") > agentMemTombDigest("user/"+newer+".md") {
		older, newer = newer, older
	}
	forgeForgotten(t, "user", older, when, false)
	forgeForgotten(t, "user", newer, when, false)
	_, w, body := forgottenGet(t, "")
	if namesOf(w) != newer+","+older {
		t.Fatalf("same-second forgets, newest first: %s", body)
	}
	// Paging by the cursor keeps the order.
	setForgottenMax(t, 1)
	_, p1, body1 := forgottenGet(t, "")
	if namesOf(p1) != newer || p1.Next == "" {
		t.Fatalf("page 1: %s", body1)
	}
	if _, p2, body2 := forgottenGet(t, "?before="+p1.Next); namesOf(p2) != older {
		t.Fatalf("page 2: %s", body2)
	}
}

// A purge between two pages rewrites history and drops commits. The cursor must not depend on
// where a commit sits in the history, or a row already listed comes back on the next page.
func TestAgentMemoryMemberForgottenPagingSurvivesAPurge(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	const when = "2026-10-04T09:00:00Z"
	forgeForgotten(t, "user", "purged-1", when, false)
	forgeForgotten(t, "user", "purged-2", when, false)
	forgeForgotten(t, "user", "purged-3", when, false)
	for _, n := range []string{"tie-a", "tie-b", "tie-c"} {
		forgeForgotten(t, "user", n, when, false)
	}
	setForgottenMax(t, 1)
	_, p1, body := forgottenGet(t, "")
	if namesOf(p1) != "tie-c" || p1.Next == "" {
		t.Fatalf("page 1: %s", body)
	}
	for _, n := range []string{"purged-1", "purged-2", "purged-3"} {
		pv, err := agentMemPurgePreview(agentMemScopeUser, "", n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeUser, Name: n, Plan: pv.Plan, Confirm: n}); err != nil {
			t.Fatalf("purge %s: %v", n, err)
		}
	}
	var got []string
	for next, pages := p1.Next, 0; next != "" && pages < 10; pages++ {
		_, p, body := forgottenGet(t, "?before="+next)
		if len(p.Forgotten) == 0 {
			t.Fatalf("empty page: %s", body)
		}
		got = append(got, namesOf(p))
		next = p.Next
	}
	if strings.Join(got, " ") != "tie-b tie-a" {
		t.Fatalf("pages after the purge: %v", got)
	}
}

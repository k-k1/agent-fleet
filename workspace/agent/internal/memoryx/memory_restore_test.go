package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memoryRestoreSeed builds two generations, "v1 → change → v2", and returns both revs. On
// the claude side v2 carries all three kinds of change — (1) overwrite, (2) addition,
// (3) deletion — so one test can check that restore undoes all three directions.
func memoryRestoreSeed(t *testing.T, cfg, slug string) (v1, v2 string) {
	t.Helper()
	mem := filepath.Join(cfg, "projects", slug, "memory")

	first, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !first.Committed {
		t.Fatalf("seed v1: %+v err=%v", first, err)
	}

	memoryWrite(t, filepath.Join(mem, "a.md"), "rewritten\n")               // (1) overwrite
	memoryWrite(t, filepath.Join(mem, "added.md"), "added later\n")         // (2) addition
	if err := os.Remove(filepath.Join(mem, "nested", "b.md")); err != nil { // (3) deletion
		t.Fatal(err)
	}
	second, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !second.Committed {
		t.Fatalf("seed v2: %+v err=%v", second, err)
	}
	return first.Rev, second.Rev
}

func memoryReadLive(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// Per-project restore (docs/log/39 ④): all three directions (overwrite, addition, deletion)
// come back, the pre-restore snapshot and the restore commit are stacked on the history, and
// no other kind is touched.
func TestMemoryRestoreProjectScope(t *testing.T) {
	home, cfg, slug := memoryTestEnv(t)
	v1, _ := memoryRestoreSeed(t, cfg, slug)
	mem := filepath.Join(cfg, "projects", slug, "memory")

	// Move the codex side too, after v2, so the project scope can be shown not to cross over.
	codexIndex := filepath.Join(home, ".codex", "memories", "MEMORY.md")
	memoryWrite(t, codexIndex, "codex moved on\n")

	before := memoryCommitCount(t)
	res, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1, "", time.Now())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// live is back at v1's contents (all three directions).
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "first\n" {
		t.Errorf("overwritten file not restored: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "nested", "b.md")); got != "nested\n" {
		t.Errorf("deleted file not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(mem, "added.md")); !os.IsNotExist(err) {
		t.Errorf("file added after the target rev survived the restore (err=%v)", err)
	}

	// History is not rewritten; two commits are stacked (pre-restore + restore).
	if n := memoryCommitCount(t); n != before+2 {
		t.Fatalf("commits %d → %d, want +2 (pre-restore + restore)", before, n)
	}
	if res.PreRestore == "" || res.Rev == "" || !res.Committed {
		t.Fatalf("restore result: %+v", res)
	}
	if res.From != v1 {
		t.Errorf("From = %q, want %q", res.From, v1)
	}
	list, err := memoryListSnapshots(4, "")
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Rev != res.Rev || list[0].Trigger != memoryTriggerRestore {
		t.Errorf("newest snapshot = %+v, want the restore commit", list[0])
	}
	if list[1].Rev != res.PreRestore || list[1].Trigger != memoryTriggerPreRestore {
		t.Errorf("second snapshot = %+v, want the pre-restore commit", list[1])
	}
	// The source and the applied scope can be recovered from the trailers (audit, and the clue
	// for undoing the undo).
	body, err := memoryGitRun("log", "-1", "--pretty=%B", res.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "AF-Restore-Rev: "+v1) ||
		!strings.Contains(body, "AF-Restore-Scope: claude/projects/"+slug) {
		t.Errorf("restore trailers missing:\n%s", body)
	}
	if !strings.HasPrefix(body, "restore: ") {
		t.Errorf("restore commit subject should say restore:\n%s", body)
	}

	// Nothing outside the scope (codex) is rolled back.
	if got := memoryReadLive(t, codexIndex); got != "codex moved on\n" {
		t.Errorf("project-scoped restore touched the codex root: %q", got)
	}
	// The pre-restore snapshot really did preserve the live state from before the restore.
	if d, err := memoryDiff("", res.PreRestore, ""); err != nil || !strings.Contains(d, "codex moved on") {
		t.Errorf("pre-restore snapshot did not capture the live state: err=%v\n%s", err, d)
	}
}

// All-scope restore and undoing the undo (★2): restoring once more to the pre-restore rev
// brings the original state back. A timestamp (at) resolves to the same restore.
func TestMemoryRestoreAllScopeIsReversible(t *testing.T) {
	home, cfg, slug := memoryTestEnv(t)
	memoryRestoreSeed(t, cfg, slug)
	mem := filepath.Join(cfg, "projects", slug, "memory")
	codexIndex := filepath.Join(home, ".codex", "memories", "MEMORY.md")

	// There is no instant to aim at between v1 and v2, so take a fresh all-scope snapshot "now"
	// instead of using a rev, and aim the at parameter at that timestamp.
	memoryWrite(t, codexIndex, "codex v3\n")
	v3, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v3.Committed {
		t.Fatalf("v3: %+v err=%v", v3, err)
	}
	at, err := memoryGitRun("log", "-1", "--pretty=%aI", v3.Rev)
	if err != nil {
		t.Fatal(err)
	}

	// Move live again, then restore everything to v3 by timestamp.
	memoryWrite(t, filepath.Join(mem, "a.md"), "v4\n")
	memoryWrite(t, codexIndex, "codex v4\n")
	back, err := memoryRestore(memoryRestoreScope{All: true}, "", at, time.Now())
	if err != nil {
		t.Fatalf("restore all: %v", err)
	}
	if back.From != v3.Rev {
		t.Fatalf("at resolved to %s, want %s", back.From, v3.Rev)
	}
	if got := memoryReadLive(t, codexIndex); got != "codex v3\n" {
		t.Errorf("codex root not restored by all-scope: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "rewritten\n" {
		t.Errorf("claude root not restored by all-scope: %q", got)
	}
	if len(back.Scopes) != 2 {
		t.Errorf("all-scope should cover both roots, got %v", back.Scopes)
	}

	// Undo the undo: restoring to the pre-restore rev brings the v4 state back.
	undo, err := memoryRestore(memoryRestoreScope{All: true}, back.PreRestore, "", time.Now())
	if err != nil {
		t.Fatalf("undo restore: %v", err)
	}
	if !undo.Committed {
		t.Errorf("undo restore did not change anything: %+v", undo)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "v4\n" {
		t.Errorf("undo did not bring back the pre-restore state: %q", got)
	}
	if got := memoryReadLive(t, codexIndex); got != "codex v4\n" {
		t.Errorf("undo did not bring back the pre-restore codex state: %q", got)
	}
}

// The reverse of ★1: restore never writes or deletes outside the allowlist. Checks that it
// does not write through a symlink planted in live (whose target is the credentials) and that
// no non-memory file (transcript, settings, credentials) is deleted.
func TestMemoryRestoreNeverTouchesNonMemoryFiles(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	v1, _ := memoryRestoreSeed(t, cfg, slug)
	proj := filepath.Join(cfg, "projects", slug)
	mem := filepath.Join(proj, "memory")
	creds := filepath.Join(cfg, ".credentials.json")

	// Swap the restore destination itself for a symlink pointing outside the allowlist. Writing
	// through the link would corrupt the credentials, which must not happen.
	outside := filepath.Join(cfg, "outside.md")
	memoryWrite(t, outside, "outside content\n")
	if err := os.Remove(filepath.Join(mem, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mem, "a.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if got := memoryReadLive(t, outside); got != "outside content\n" {
		t.Errorf("restore wrote through a symlink: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "first\n" {
		t.Errorf("symlink was not replaced by the restored file: %q", got)
	}
	// Not one non-memory file is removed.
	for _, p := range []string{
		creds,
		filepath.Join(cfg, "settings.json"),
		filepath.Join(cfg, "af-usage.json"),
		filepath.Join(proj, "abcd-1234.jsonl"),
		filepath.Join(proj, "subagents", "agent-1.jsonl"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("restore removed a non-memory file %s: %v", p, err)
		}
	}
	if got := memoryReadLive(t, creds); got != `{"token":"SECRET"}` {
		t.Errorf("credentials were modified: %q", got)
	}
	// The credential contents are absent from the history after the restore too.
	if blobs, _ := memoryGitRun("grep", "-I", "-l", "SECRET", memoryBranch); strings.TrimSpace(blobs) != "" {
		t.Errorf("credential contents reachable in repo: %s", blobs)
	}
}

// Directories left empty by a deletion are folded up, but pruning always stops on a branch
// where something non-memory remains. (Everything inside `memory/` is version-controlled
// regardless of extension — the allowlist is decided by path. Non-memory files live outside
// `memory/`, e.g. the transcripts directly under the project.)
func TestMemoryRestorePrunesEmptyDirsOnly(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	proj := filepath.Join(cfg, "projects", slug)
	mem := filepath.Join(proj, "memory")

	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("v1: %+v err=%v", v1, err)
	}
	// A subdirectory added after v1 becomes empty on restore, so it is pruned.
	memoryWrite(t, filepath.Join(mem, "onlymem", "c.md"), "c\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mem, "onlymem")); !os.IsNotExist(err) {
		t.Errorf("emptied memory dir was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(mem); err != nil {
		t.Errorf("memory dir with remaining files was pruned: %v", err)
	}

	// Restoring to a point with no memory at all folds up memory/ itself, but pruning always
	// stops at the project directory, where the transcript lives (os.Remove only removes an
	// empty directory).
	if err := os.RemoveAll(mem); err != nil {
		t.Fatal(err)
	}
	empty, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !empty.Committed {
		t.Fatalf("empty snapshot: %+v err=%v", empty, err)
	}
	memoryWrite(t, filepath.Join(mem, "back.md"), "back\n")
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, empty.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore to empty: %v", err)
	}
	if _, err := os.Stat(mem); !os.IsNotExist(err) {
		t.Errorf("fully emptied memory dir was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "abcd-1234.jsonl")); err != nil {
		t.Errorf("pruning climbed past the memory dir and hit the transcript: %v", err)
	}
}

// The tree API answers "what was there at that point". A project that is gone from live today
// is still selectable from the snapshot of the time (= memory deleted by mistake can be
// restored).
func TestMemoryTreeAtListsHistoricalProjects(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("v1: %+v err=%v", v1, err)
	}
	// Delete the project's memory from live entirely, then take a fresh snapshot.
	if err := os.RemoveAll(filepath.Join(cfg, "projects", slug, "memory")); err != nil {
		t.Fatal(err)
	}
	v2, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v2.Committed {
		t.Fatalf("v2: %+v err=%v", v2, err)
	}

	sha, kinds, projects, err := memoryTreeAt(v1.Rev, "")
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if sha != v1.Rev {
		t.Errorf("tree rev = %s, want %s", sha, v1.Rev)
	}
	if len(projects) != 1 || projects[0].Slug != slug || projects[0].Files != 3 {
		t.Fatalf("tree projects = %+v", projects)
	}
	if projects[0].Display != "demo" || projects[0].Bytes <= 0 {
		t.Errorf("tree project detail = %+v", projects[0])
	}
	if len(kinds) != 2 || kinds[0].Kind != "claude" || !kinds[0].Scopes || kinds[1].Scopes {
		t.Fatalf("tree kinds = %+v", kinds)
	}
	// As of now (v2) the claude side is empty.
	_, kinds2, projects2, err := memoryTreeAt(v2.Rev, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects2) != 0 || len(kinds2) != 1 || kinds2[0].Kind != "codex" {
		t.Fatalf("tree at v2 = %+v / %+v", kinds2, projects2)
	}

	// And restoring to v1 brings the memory back (the use case this exists for).
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := memoryReadLive(t, filepath.Join(cfg, "projects", slug, "memory", "MEMORY.md")); got == "" {
		t.Error("deleted project memory was not restored")
	}
}

// Scope validation: an unknown kind, an invalid slug and an empty scope are all rejected.
func TestMemoryResolveScopeRejectsBadInput(t *testing.T) {
	memoryTestEnv(t)
	for _, sc := range []memoryRestoreScope{
		{},
		{Kinds: []string{"opencode"}},
		{Projects: []string{"../escape"}},
		{Projects: []string{"a/b"}},
		{Projects: []string{""}},
	} {
		if _, err := memoryResolveScope(sc); err == nil {
			t.Errorf("scope %+v was accepted", sc)
		}
	}
	// An all-scope request swallows the project one (a duplicated prefix is not applied twice).
	targets, err := memoryResolveScope(memoryRestoreScope{All: true, Projects: []string{"-home-dev-repos-demo"}})
	if err != nil || len(targets) != 2 {
		t.Fatalf("all+projects = %+v err=%v", targets, err)
	}
}

// A round trip over REST (route registration, response shape, error codes). A route missing
// on the CP side is covered by control-plane's own tests; the Agent side is pinned here.
func TestMemoryRestoreAPI(t *testing.T) {
	h := memoryAPIHandler(t)
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	slug := "-home-dev-repos-demo"
	v1, _ := memoryRestoreSeed(t, cfg, slug)

	// tree: the choices come from the contents at that point in time.
	w := smokeDo(t, h, "GET", "/agents/memory/tree?rev="+v1, "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", w.Code, w.Body.String())
	}
	var tree struct {
		Rev      string              `json:"rev"`
		Kinds    []memoryTreeKind    `json:"kinds"`
		Projects []memoryTreeProject `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatalf("tree decode: %v (%s)", err, w.Body.String())
	}
	if tree.Rev != v1 || len(tree.Projects) != 1 || tree.Projects[0].Slug != slug {
		t.Fatalf("tree = %+v", tree)
	}

	// restore: round-trip with a project scope.
	w = smokeDo(t, h, "POST", "/agents/memory/restore", "smoke-token",
		`{"rev":"`+v1+`","scope":{"projects":["`+slug+`"]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	var res memoryRestoreResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !res.Committed || res.Rev == "" {
		t.Fatalf("restore result: %+v err=%v (%s)", res, err, w.Body.String())
	}
	if len(res.Deleted) != 1 || !strings.HasSuffix(res.Deleted[0], "/added.md") {
		t.Errorf("deleted = %v", res.Deleted)
	}
	if len(res.Written) != 2 {
		t.Errorf("written = %v", res.Written)
	}
	if res.Busy {
		t.Errorf("no session is running, busy should be false: %+v", res)
	}
	if got := memoryReadLive(t, filepath.Join(cfg, "projects", slug, "memory", "a.md")); got != "first\n" {
		t.Errorf("live not restored through the API: %q", got)
	}

	// Input validation: 400 / 404 with a stable code.
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"rev":"` + v1 + `"}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"` + v1 + `","scope":{"kinds":["opencode"]}}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"` + v1 + `","scope":{"projects":["../escape"]}}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"nope","scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
		{`{"scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
		{`{"rev":"--upload-pack=evil","scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
	} {
		w := smokeDo(t, h, "POST", "/agents/memory/restore", "smoke-token", c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("restore %s: %d %s (want %d %s)", c.body, w.Code, w.Body.String(), c.code, c.want)
		}
	}
}

// While there is no snapshot at all, restore and tree both answer 404 with a stable code.
func TestMemoryRestoreBeforeAnySnapshot(t *testing.T) {
	h := memoryAPIHandler(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/agents/memory/restore", `{"rev":"HEAD","scope":{"all":true}}`},
		{"GET", "/agents/memory/tree?rev=HEAD", ""},
	} {
		w := smokeDo(t, h, c.method, c.path, "smoke-token", c.body)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), errCodeMemoryNoSnapshots) {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// The UI toggle for automatic snapshots (docs/log/39 resolution #1): the setting persists
// inside the claude mount, and the environment's forced OFF beats the toggle.
func TestMemoryAutoToggle(t *testing.T) {
	h := memoryAPIHandler(t)
	readAuto := func() (auto, locked bool) {
		t.Helper()
		w := smokeDo(t, h, "GET", "/agents/memory/roots", "smoke-token", "")
		var out struct {
			Auto   bool `json:"auto"`
			Locked bool `json:"autoLocked"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("roots decode: %v (%s)", err, w.Body.String())
		}
		return out.Auto, out.Locked
	}

	if auto, locked := readAuto(); !auto || locked {
		t.Fatalf("default should be auto=on unlocked, got %v/%v", auto, locked)
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":false}`); w.Code != http.StatusOK {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
	if auto, _ := readAuto(); auto {
		t.Error("toggle off did not persist")
	}
	if memoryAutoEnabled() {
		t.Error("the snapshot loop would still run after the toggle was turned off")
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("missing auto: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":true}`); w.Code != http.StatusOK {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	if auto, _ := readAuto(); !auto {
		t.Error("toggle on did not persist")
	}

	// The operator's forced OFF cannot be undone from the UI.
	t.Setenv("AF_MEMORY_SNAPSHOT", "off")
	if auto, locked := readAuto(); auto || !locked {
		t.Fatalf("env override should force auto off and mark it locked, got %v/%v", auto, locked)
	}
	smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":true}`)
	if auto, _ := readAuto(); auto {
		t.Error("UI toggle overrode the operator's AF_MEMORY_SNAPSHOT=off")
	}
}

// TestMemoryP2RoutesRegistered stays in package main (memory_routes_test.go); see the same
// note in memory_handlers_test.go.

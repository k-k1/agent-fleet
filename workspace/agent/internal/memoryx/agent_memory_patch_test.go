package memoryx

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var agentMemPatchNow = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// agentMemPatchSeed saves "m" with the given body and returns its caller.
func agentMemPatchSeed(t *testing.T, body string) agentMemCaller {
	t.Helper()
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "desc", Type: "project", Kinds: []string{"claude", "codex"}, Body: body}, agentMemPatchNow); err != nil {
		t.Fatal(err)
	}
	return c
}

func agentMemPatchOf(rev int, edits ...agentMemPatchEdit) agentMemPatchReq {
	return agentMemPatchReq{Name: "m", Revision: rev, Edits: edits}
}

func agentMemPatchCommitCount(t *testing.T) string {
	t.Helper()
	out, err := memoryGitRun("rev-list", "--count", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func TestAgentMemoryPatchAppliesEditsTogether(t *testing.T) {
	c := agentMemPatchSeed(t, "alpha one\nbeta two\ngamma three")
	// The second edit's `old` exists only in the original: edits are matched against the same
	// original text, not against each other's output.
	res, err := agentMemPatch(c, agentMemPatchOf(1,
		agentMemPatchEdit{Old: "beta two", New: "beta 2 (alpha one)"},
		agentMemPatchEdit{Old: "gamma", New: "delta"},
		agentMemPatchEdit{Old: "alpha", New: ""},
	), agentMemPatchNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 2 || res.Created || res.Commit == "" {
		t.Fatalf("result = %+v", res)
	}
	e, err := agentMemRead(c, agentMemScopeProject, "m")
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\nbeta 2 (alpha one)\ndelta three"; e.Body != want {
		t.Errorf("body = %q, want %q", e.Body, want)
	}
	msg, err := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AF-Op: update", "AF-Author-Kind: claude", "AF-Author-Session: claude-main"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message lacks %q:\n%s", want, msg)
		}
	}
}

func TestAgentMemoryPatchRefusals(t *testing.T) {
	c := agentMemPatchSeed(t, "aaa bbb ccc\nsecond line")
	before := agentMemPatchCommitCount(t)
	secret := "AKIA" + "ABCDEFGHIJKLMNOP"
	empty := ""
	cases := []struct {
		name string
		req  agentMemPatchReq
		code string
	}{
		{"empty old", agentMemPatchOf(1, agentMemPatchEdit{Old: "", New: "x"}), errCodeMemoryBadRequest},
		{"zero matches", agentMemPatchOf(1, agentMemPatchEdit{Old: "zzz", New: "x"}), errCodeMemoryBadRequest},
		{"multiple matches", agentMemPatchOf(1, agentMemPatchEdit{Old: "line", New: "x"}, agentMemPatchEdit{Old: "e", New: "y"}), errCodeMemoryBadRequest},
		{"self-overlapping old matches twice", agentMemPatchOf(1, agentMemPatchEdit{Old: "aa", New: "x"}), errCodeMemoryBadRequest},
		{"overlapping edits", agentMemPatchOf(1, agentMemPatchEdit{Old: "bbb ccc", New: "x"}, agentMemPatchEdit{Old: "aaa bbb", New: "y"}), errCodeMemoryBadRequest},
		{"contained edits", agentMemPatchOf(1, agentMemPatchEdit{Old: "bbb ccc", New: "x"}, agentMemPatchEdit{Old: "ccc", New: "y"}), errCodeMemoryBadRequest},
		{"partial failure: second edit does not match", agentMemPatchOf(1, agentMemPatchEdit{Old: "aaa", New: "ok"}, agentMemPatchEdit{Old: "nope", New: "x"}), errCodeMemoryBadRequest},
		{"stale revision", agentMemPatchOf(7, agentMemPatchEdit{Old: "aaa", New: "x"}), errCodeMemoryConflict},
		{"revision zero", agentMemPatchOf(0, agentMemPatchEdit{Old: "aaa", New: "x"}), errCodeMemoryBadRequest},
		{"no edits and no description", agentMemPatchOf(1), errCodeMemoryBadRequest},
		{"empty description", agentMemPatchReq{Name: "m", Revision: 1, Description: &empty}, errCodeMemoryBadRequest},
		{"body would be empty", agentMemPatchOf(1, agentMemPatchEdit{Old: "aaa bbb ccc\nsecond line", New: "  "}), errCodeMemoryBadRequest},
		{"missing memory", agentMemPatchReq{Name: "absent", Revision: 1, Edits: []agentMemPatchEdit{{Old: "a", New: "b"}}}, errCodeMemoryNotFound},
		{"bad name", agentMemPatchReq{Name: "../x", Revision: 1, Edits: []agentMemPatchEdit{{Old: "a", New: "b"}}}, errCodeMemoryBadRequest},
	}
	for _, tc := range cases {
		_, err := agentMemPatch(c, tc.req, agentMemPatchNow)
		if got := agentMemCode(err); err == nil || got != tc.code {
			t.Errorf("%s: err = %v (code %q), want code %q", tc.name, err, got, tc.code)
		}
	}
	_, err := agentMemPatch(c, agentMemPatchOf(1, agentMemPatchEdit{Old: "bbb", New: "key " + secret}), agentMemPatchNow)
	if _, ok := err.(*agentMemSecretErr); !ok {
		t.Errorf("secret in new: err = %v, want a secret refusal", err)
	}
	d := "token " + secret
	if _, err := agentMemPatch(c, agentMemPatchReq{Name: "m", Revision: 1, Description: &d}, agentMemPatchNow); err == nil {
		t.Error("secret in description was accepted")
	} else if _, ok := err.(*agentMemSecretErr); !ok {
		t.Errorf("secret in description: err = %v", err)
	}

	// Nothing above wrote: same revision, same body, same history.
	e, err := agentMemRead(c, agentMemScopeProject, "m")
	if err != nil || e.Revision != 1 || e.Body != "aaa bbb ccc\nsecond line" {
		t.Fatalf("after refusals: %+v, %v", e, err)
	}
	if got := agentMemPatchCommitCount(t); got != before {
		t.Errorf("commits %s -> %s: a refused patch wrote", before, got)
	}
	if _, err := agentMemRead(c, agentMemScopeProject, "absent"); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("a refused patch created a memory: %v", err)
	}
}

func TestAgentMemoryPatchKeepsFrontmatter(t *testing.T) {
	c := agentMemPatchSeed(t, "keep this\nchange that")
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		t.Fatal(err)
	}
	// Give the memory the fields only other writers set: a pin, and the import's source.
	agentMemMu.Lock()
	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	l, ok, err := agentMemLoadFile(filepath.Join(dir, "m.md"), agentMemScopeProject, "m")
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	e := l.Entry
	e.Pinned, e.Source, e.SourceHash = true, "claude-import:foo.md", "abc123"
	data := agentMemRender(e)
	if err := agentMemWriteFile(filepath.Join(dir, "m.md"), data); err != nil {
		t.Fatal(err)
	}
	agentMemPrimeScan(data, agentMemScopeProject, "m")
	agentMemMu.Unlock()
	before, err := agentMemRead(c, agentMemScopeProject, "m")
	if err != nil {
		t.Fatal(err)
	}

	codex := agentMemCallerT(t, "codex-wt")
	if _, err := agentMemPatch(codex, agentMemPatchOf(before.Revision, agentMemPatchEdit{Old: "change that", New: "changed"}), agentMemPatchNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := agentMemRead(c, agentMemScopeProject, "m")
	if err != nil {
		t.Fatal(err)
	}
	if after.Body != "keep this\nchanged" || after.Revision != before.Revision+1 {
		t.Fatalf("after = %+v", after)
	}
	if !after.Pinned || after.Source != before.Source || after.SourceHash != before.SourceHash || after.Created != before.Created ||
		after.Type != before.Type || strings.Join(after.Kinds, ",") != "claude,codex" || after.Description != "desc" {
		t.Errorf("a field the patch does not name changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.AuthorKind != "codex" || after.AuthorSession != "codex-wt" || after.Updated == before.Updated {
		t.Errorf("author/updated not recorded: %+v", after)
	}
}

func TestAgentMemoryPatchDescriptionOnly(t *testing.T) {
	c := agentMemPatchSeed(t, "body stays")
	d := "  a better description  "
	res, err := agentMemPatch(c, agentMemPatchReq{Name: "m", Revision: 1, Description: &d}, agentMemPatchNow.Add(time.Hour))
	if err != nil || res.Revision != 2 {
		t.Fatalf("%+v, %v", res, err)
	}
	e, _ := agentMemRead(c, agentMemScopeProject, "m")
	if e.Description != "a better description" || e.Body != "body stays" {
		t.Errorf("entry = %+v", e)
	}
	// Edits and a description together are one revision.
	d2 := "third"
	if res, err = agentMemPatch(c, agentMemPatchReq{Name: "m", Revision: 2, Description: &d2, Edits: []agentMemPatchEdit{{Old: "stays", New: "moved"}}}, agentMemPatchNow); err != nil || res.Revision != 3 {
		t.Fatalf("%+v, %v", res, err)
	}
}

// Two writers holding revision 1: exactly one lands, the other is told to read again, and the
// memory ends at revision 2 with the winner's text, never a mix.
func TestAgentMemoryPatchVersusSaveIsCompareAndSwap(t *testing.T) {
	c := agentMemPatchSeed(t, "one two three")
	for round := 0; round < 5; round++ {
		cur, err := agentMemRead(c, agentMemScopeProject, "m")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = agentMemPatch(c, agentMemPatchOf(cur.Revision, agentMemPatchEdit{Old: "two", New: "PATCHED"}), agentMemPatchNow)
		}()
		go func() {
			defer wg.Done()
			_, errs[1] = agentMemSave(c, agentMemSaveReq{Name: "m", Description: "desc", Type: "project", Body: "SAVED", Revision: cur.Revision}, agentMemPatchNow)
		}()
		wg.Wait()
		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
			} else if agentMemCode(err) != errCodeMemoryConflict {
				t.Fatalf("round %d: loser got %v, want conflict", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d writers won, want exactly 1 (%v)", round, wins, errs)
		}
		got, err := agentMemRead(c, agentMemScopeProject, "m")
		if err != nil || got.Revision != cur.Revision+1 {
			t.Fatalf("round %d: %+v, %v", round, got, err)
		}
		if got.Body != "SAVED" && !strings.Contains(got.Body, "PATCHED") {
			t.Fatalf("round %d: body %q is neither writer's", round, got.Body)
		}
		// Reset to a patchable body for the next round.
		if _, err := agentMemSave(c, agentMemSaveReq{Name: "m", Description: "desc", Type: "project", Body: "one two three", Revision: got.Revision}, agentMemPatchNow); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentMemoryPatchHandlerNeverCreates(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	post := func(body string) int {
		return smokeDo(t, buildMux(), http.MethodPost, "/agents/memory/entries/patch", "", body).Code
	}
	if code := post(`{"session":"claude-main","name":"nope","revision":1,"edits":[{"old":"a","new":"b"}]}`); code != http.StatusNotFound {
		t.Errorf("patch of a missing memory: %d, want 404", code)
	}
	if code := post(`{"session":"claude-main","name":"nope","revision":0,"edits":[{"old":"a","new":"b"}]}`); code != http.StatusBadRequest {
		t.Errorf("patch at revision 0: %d, want 400", code)
	}
}

func TestAgentMemorySaveOfNewNameHintsNearNames(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	save := func(name, desc string) agentMemWriteResult {
		t.Helper()
		res, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: desc, Body: "b"}, agentMemPatchNow)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	save("go-test-memory-cap", "cap the go test memory")
	save("unrelated-thing", "nothing to do with it")
	res := save("go-test-memory-limit", "limit for go tests")
	if !res.Created {
		t.Fatal("a near name must still be created")
	}
	hint := strings.Join(res.Warnings, "\n")
	if !strings.Contains(hint, "prefer memory_patch on go-test-memory-cap") || strings.Contains(hint, "unrelated-thing") {
		t.Errorf("warnings = %q", res.Warnings)
	}
	if res := save("deploy-checklist", "d"); len(res.Warnings) != 0 {
		t.Errorf("a distant name got a hint: %q", res.Warnings)
	}
	// An update of the memory itself is not "near itself".
	cur, _ := agentMemRead(c, agentMemScopeProject, "go-test-memory-cap")
	res2, err := agentMemSave(c, agentMemSaveReq{Name: "go-test-memory-cap", Description: "d", Body: "b2", Revision: cur.Revision}, agentMemPatchNow)
	if err != nil || len(res2.Warnings) != 0 {
		t.Errorf("update: %+v, %v", res2, err)
	}
}

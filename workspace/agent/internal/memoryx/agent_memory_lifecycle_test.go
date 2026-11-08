package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var lifeNow = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// lifeSet rewrites a stored memory the way a later writer (lint, feedback, the consolidator, the
// member) will: the stored entry, changed in memory, published through the central apply so the
// schema check and a commit are part of it. The revision moves on like any write.
func lifeSet(t *testing.T, c agentMemCaller, scope, name string, mut func(e *agentMemEntry)) {
	t.Helper()
	if err := lifeSetErr(c, scope, name, mut); err != nil {
		t.Fatal(err)
	}
}

func lifeSetErr(c agentMemCaller, scope, name string, mut func(e *agentMemEntry)) error {
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return err
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		return err
	}
	l, ok, err := agentMemLoadFile(filepath.Join(dir, name+".md"), scope, name)
	if err != nil || !ok {
		return os.ErrNotExist
	}
	e := l.Entry
	e.Revision++
	mut(&e)
	repoRel := rel + "/" + name + ".md"
	_, err = agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: agentMemRender(e)}}, "update", repoRel, agentMemMember, lifeNow)
	return err
}

func lifeSave(t *testing.T, c agentMemCaller, req agentMemSaveReq) {
	t.Helper()
	if req.Description == "" {
		req.Description = "about " + req.Name
	}
	if req.Body == "" {
		req.Body = "body of " + req.Name
	}
	if _, err := agentMemSave(c, req, lifeNow); err != nil {
		t.Fatalf("save %s: %v", req.Name, err)
	}
}

func lifeStored(t *testing.T, c agentMemCaller, scope, name string) agentMemEntry {
	t.Helper()
	rel, _ := agentMemScopeDir(scope, c)
	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	l, ok, err := agentMemLoadFile(filepath.Join(dir, name+".md"), scope, name)
	if err != nil || !ok || l.Withheld {
		t.Fatalf("load %s: %v %v", name, ok, err)
	}
	return l.Entry
}

func lifeFull() agentMemLife {
	return agentMemLife{
		MemKind: "procedure", Anchors: []string{"docs/decisions/0130-cultivated-memory.md", "go test ./..., -run X", "[[other]]"},
		Status: "quiet", StatusBy: "member",
		SupersededBy: &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "example.invalid/o/r", Path: "docs/a.md", Heading: "Why", Commit: "0123abc"}},
		InvalidAt:    "2026-11-01T00:00:00Z",
		Reasons: []agentMemReason{
			{By: "lint", Rule: "path-missing", Note: "docs/gone.md is not on main", Revision: 2},
			{By: "feedback", Rule: "stale", Note: "said \"stale\", with a comma, a colon: and \\ backslash", Revision: 2},
		},
	}
}

// A memory without the new fields renders exactly as before, so nothing changes for it, and one
// with them survives a render and a parse unchanged.
func TestLifeRenderParseRoundTrip(t *testing.T) {
	plain := agentMemEntry{Name: "n", Description: "d", Revision: 1, AuthorKind: "claude", AuthorSession: "s", Created: "c", Updated: "u", Body: "b"}
	want := "---\nname: \"n\"\ndescription: \"d\"\nrevision: 1\nauthor_kind: \"claude\"\nauthor_session: \"s\"\ncreated: \"c\"\nupdated: \"u\"\n---\nb\n"
	if got := string(agentMemRender(plain)); got != want {
		t.Fatalf("a memory with no lifecycle fields changed on disk:\n%s", got)
	}

	full := plain
	full.agentMemLife = lifeFull()
	data := agentMemRender(full)
	back, problems, ok := agentMemParseStrict(data)
	if !ok || len(problems) != 0 {
		t.Fatalf("parse: %v %v\n%s", ok, problems, data)
	}
	if !reflect.DeepEqual(back.agentMemLife, full.agentMemLife) {
		t.Fatalf("round trip:\n got %+v\nwant %+v\n%s", back.agentMemLife, full.agentMemLife, data)
	}
	if err := agentMemCheckLife(data); err != nil {
		t.Fatalf("the full set should pass the schema: %v", err)
	}
	// Every key is plain frontmatter that a reader without the schema skips (version skew): the
	// keys it knows come out the same.
	for _, k := range []string{"kind:", "anchors:", "status:", "status_by:", "superseded_by:", "invalid_at:", "reasons:"} {
		if !strings.Contains(string(data), "\n"+k+" ") {
			t.Errorf("no %s line in\n%s", k, data)
		}
	}
	// A memory with a memory target.
	m := plain
	m.SupersededBy = &agentMemSuperseded{Memory: "newer", Scope: "project"}
	if b, _, _ := agentMemParseStrict(agentMemRender(m)); b.SupersededBy == nil || b.SupersededBy.Memory != "newer" {
		t.Fatalf("memory target lost: %+v", b.SupersededBy)
	}
}

// One schema validates every writer's output: the central apply refuses a file that breaks it,
// whichever writer built it, and says which field, never the value.
func TestLifeSchemaRefusesBadOutput(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	lifeSave(t, c, agentMemSaveReq{Name: "m"})
	bad := map[string]func(l *agentMemLife){
		"kind":             func(l *agentMemLife) { l.MemKind = "mood" },
		"status":           func(l *agentMemLife) { l.Status = "paused" },
		"status_by":        func(l *agentMemLife) { l.Status, l.StatusBy = "quiet", "robot" },
		"status_by needs":  func(l *agentMemLife) { l.StatusBy = "member" },
		"invalid_at":       func(l *agentMemLife) { l.InvalidAt = "yesterday" },
		"anchors":          func(l *agentMemLife) { l.Anchors = []string{"a\nb"} },
		"anchor empty":     func(l *agentMemLife) { l.Anchors = []string{"  "} },
		"too many anchors": func(l *agentMemLife) { l.Anchors = make([]string, 40) },
		"reason by":        func(l *agentMemLife) { l.Reasons = []agentMemReason{{By: "agent", Rule: "stale"}} },
		"reason rule":      func(l *agentMemLife) { l.Reasons = []agentMemReason{{By: "lint", Rule: "Not A Rule"}} },
		"reason note":      func(l *agentMemLife) { l.Reasons = []agentMemReason{{By: "lint", Rule: "x", Note: "a\nb"}} },
		"both targets": func(l *agentMemLife) {
			l.SupersededBy = &agentMemSuperseded{Memory: "a", Document: &agentMemDocTarget{Repo: "r", Path: "p", Commit: "abcdef0"}}
		},
		"no target":          func(l *agentMemLife) { l.SupersededBy = &agentMemSuperseded{} },
		"memory target name": func(l *agentMemLife) { l.SupersededBy = &agentMemSuperseded{Memory: "../x"} },
		"document path up": func(l *agentMemLife) {
			l.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "r", Path: "a/../b", Commit: "abcdef0"}}
		},
		"document path abs": func(l *agentMemLife) {
			l.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "r", Path: "/a", Commit: "abcdef0"}}
		},
		"document commit": func(l *agentMemLife) {
			l.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "r", Path: "a", Commit: "main"}}
		},
		"document repo": func(l *agentMemLife) {
			l.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Path: "a", Commit: "abcdef0"}}
		},
		"memory target scoped": func(l *agentMemLife) { l.SupersededBy = &agentMemSuperseded{Memory: "a", Scope: "team"} },
	}
	for name, mut := range bad {
		err := lifeSetErr(c, agentMemScopeProject, "m", func(e *agentMemEntry) { mut(&e.agentMemLife) })
		if agentMemCode(err) != errCodeMemoryBadRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// The same file with a value the reader would silently drop is refused too: the strict read
	// sees what the lenient one hides.
	e := lifeStored(t, c, agentMemScopeProject, "m")
	data := strings.Replace(string(agentMemRender(e)), "---\nname", "---\nstatus: \"paused\"\nname", 1)
	if err := agentMemCheckLife([]byte(data)); err == nil {
		t.Error("a hand-written bad status passed the writer check")
	}
	if lenient, ok := agentMemParse([]byte(data)); !ok || lenient.Status != "" {
		t.Errorf("a reader should drop it: %+v", lenient.Status)
	}
	// Nothing was published by the refusals.
	if got := lifeStored(t, c, agentMemScopeProject, "m"); got.Revision != 1 {
		t.Errorf("a refused write moved the revision to %d", got.Revision)
	}
}

// An agent sets kind and anchors on a save; the values are validated and secret-scanned like the body.
func TestLifeSaveKindAndAnchors(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	anchors := []string{" pkg/a.go ", "pkg/a.go", "TestX"}
	lifeSave(t, c, agentMemSaveReq{Name: "m", MemKind: sp("lesson"), Anchors: &anchors})
	got := lifeStored(t, c, agentMemScopeProject, "m")
	if got.MemKind != "lesson" || !reflect.DeepEqual(got.Anchors, []string{"pkg/a.go", "TestX"}) {
		t.Fatalf("stored = %+v", got.agentMemLife)
	}
	for name, req := range map[string]agentMemSaveReq{
		"bad kind":   {MemKind: sp("mood")},
		"bad anchor": {Anchors: &[]string{"a\nb"}},
		"secret":     {Anchors: &[]string{"token " + agentMemFakeAWS()}},
	} {
		req.Name, req.Description, req.Body, req.Revision = "m", "d", "b", 1
		_, err := agentMemSave(c, req, lifeNow)
		var se *agentMemSecretErr
		if name == "secret" {
			if !asSecretErr(err, &se) {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if agentMemCode(err) != errCodeMemoryBadRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Present and empty clears; absent keeps.
	lifeSave(t, c, agentMemSaveReq{Name: "m", Revision: 1, Body: "second"})
	if got := lifeStored(t, c, agentMemScopeProject, "m"); got.MemKind != "lesson" || len(got.Anchors) != 2 {
		t.Fatalf("a save that names neither lost them: %+v", got.agentMemLife)
	}
	lifeSave(t, c, agentMemSaveReq{Name: "m", Revision: 2, Body: "third", MemKind: sp(""), Anchors: &[]string{}})
	if got := lifeStored(t, c, agentMemScopeProject, "m"); got.MemKind != "" || len(got.Anchors) != 0 {
		t.Fatalf("clearing failed: %+v", got.agentMemLife)
	}
}

func asSecretErr(err error, target **agentMemSecretErr) bool {
	se, ok := err.(*agentMemSecretErr)
	*target = se
	return ok
}

// Every writer keeps the fields it does not name. One test per writer; each checks the whole set.
func TestLifeWritersKeepFields(t *testing.T) {
	setup := func(t *testing.T, name string) (agentMemCaller, agentMemLife) {
		t.Helper()
		_, _, _ = agentMemTestEnv(t)
		c := agentMemCallerT(t, "claude-main")
		lifeSave(t, c, agentMemSaveReq{Name: name, Body: "line one\nline two"})
		want := lifeFull()
		lifeSet(t, c, agentMemScopeProject, name, func(e *agentMemEntry) { e.agentMemLife = want })
		return c, want
	}
	check := func(t *testing.T, who string, c agentMemCaller, name string, want agentMemLife) {
		t.Helper()
		got := lifeStored(t, c, agentMemScopeProject, name)
		if !reflect.DeepEqual(got.agentMemLife, want) {
			t.Errorf("%s changed the lifecycle fields:\n got %+v\nwant %+v", who, got.agentMemLife, want)
		}
	}

	t.Run("save", func(t *testing.T) {
		c, want := setup(t, "m")
		// The body is left as it is, so the feedback reason stays; kind and anchors are not named.
		lifeSave(t, c, agentMemSaveReq{Name: "m", Revision: 2, Body: "line one\nline two", Description: "new description"})
		check(t, "save", c, "m", want)
	})
	t.Run("patch", func(t *testing.T) {
		c, want := setup(t, "m")
		// An agent cannot reach a superseded memory, so this one is only quiet and suspect.
		want.SupersededBy = nil
		lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) { e.SupersededBy = nil })
		if _, err := agentMemPatch(c, agentMemPatchOf(3, agentMemPatchEdit{Old: "line two", New: sp("line 2")}), lifeNow); err != nil {
			t.Fatal(err)
		}
		// A body change clears the feedback reason and nothing else.
		want.Reasons = want.Reasons[:1]
		check(t, "patch", c, "m", want)
		if _, err := agentMemPatch(c, agentMemPatchReq{Scope: agentMemScopeProject, Name: "m", Revision: 4, Description: sp("only the description")}, lifeNow); err != nil {
			t.Fatal(err)
		}
		check(t, "a description-only patch", c, "m", want)
	})
	t.Run("pin", func(t *testing.T) {
		c, want := setup(t, "m")
		if _, err := agentMemPin(agentMemPinReq{Scope: agentMemScopeProject, Project: c.Project.ID, Name: "m", Pinned: true}, lifeNow); err != nil {
			t.Fatal(err)
		}
		check(t, "pin", c, "m", want)
		if !lifeStored(t, c, agentMemScopeProject, "m").Pinned {
			t.Error("pin not set")
		}
	})
	// Both importers write through agentMemImportWriteTo, over the memory they evaluated as live.
	// The native file asks for an archived log with a reason of its own: nothing of that is taken.
	const outside = "---\nname: imp\ndescription: from outside\ntype: project\nkind: log\nstatus: archived\nstatus_by: agent\n" +
		"superseded_by: {\"memory\":\"x\"}\nreasons: [{\"by\":\"member\",\"rule\":\"x\",\"note\":\"n\",\"revision\":1}]\n---\n\nnew native body\n"
	t.Run("import update keeps the fields", func(t *testing.T) {
		c, want := setup(t, "imp")
		live := lifeStored(t, c, agentMemScopeProject, "imp")
		parsed, _ := agentMemParse([]byte(outside))
		conv := agentMemEntry{Name: "imp", Scope: agentMemScopeProject, Description: "from outside", Type: "project",
			AuthorKind: agentMemUnknown, AuthorSession: agentMemUnknown, Body: "new native body"}
		if parsed.Status != "archived" || parsed.MemKind != "log" {
			t.Fatalf("fixture: the file should parse with its fields: %+v", parsed.agentMemLife)
		}
		agentMemMu.Lock()
		memorySnapshotMu.Lock()
		p := *c.Project
		rel := "projects/" + p.ID
		_, err := agentMemImportWrite(p, rel, &agentMemImportItem{Name: "imp", entry: conv, live: &live}, lifeNow)
		memorySnapshotMu.Unlock()
		agentMemMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if got := lifeStored(t, c, agentMemScopeProject, "imp"); got.Body != "new native body" {
			t.Fatalf("not written: %+v", got)
		}
		check(t, "an import update", c, "imp", want)
	})
	t.Run("claude import never takes them from outside", func(t *testing.T) {
		_, _, _ = agentMemTestEnv(t)
		e := newClaudeImportEnv(t)
		e.raw(e.slug, "imp.md", outside)
		e.apply(lifeNow, "imp")
		got := lifeStored(t, agentMemCallerT(t, "claude-main"), agentMemScopeProject, "imp")
		if got.Body != "new native body" || !got.isZero() {
			t.Errorf("claude import took lifecycle fields from the file: %+v", got.agentMemLife)
		}
	})
	t.Run("muse import never takes them from outside", func(t *testing.T) {
		e := newMuseImportEnv(t)
		e.raw(e.clone, "imp.md", outside)
		e.apply(lifeNow, "imp")
		got := lifeStored(t, agentMemCallerT(t, "claude-main"), agentMemScopeProject, "imp")
		if got.Body != "new native body" || !got.isZero() {
			t.Errorf("muse import took lifecycle fields from the file: %+v", got.agentMemLife)
		}
	})
	t.Run("project move", func(t *testing.T) {
		home, _, _ := agentMemTestEnv(t)
		orphan, to := moveOrphan(t, home, "a")
		oc := agentMemCaller{Session: agentMemUnknown, Kind: agentMemUnknown}
		oc.Project = agentMemProjectInfo(orphan)
		want := lifeFull()
		lifeSet(t, oc, agentMemScopeProject, "a", func(e *agentMemEntry) { e.agentMemLife = want; e.Pinned = true })
		code, res, b := moveDo(t, orphan, to, "rename", "a")
		if code != http.StatusOK || len(res.Results) != 1 || res.Results[0].Result != "moved" {
			t.Fatalf("move: %d %s", code, b)
		}
		got := projectNames(t, to)["a"]
		if !reflect.DeepEqual(got.agentMemLife, want) || !got.Pinned {
			t.Errorf("project move changed the fields:\n got %+v\nwant %+v", got.agentMemLife, want)
		}
		// A copy that lacks the lifecycle fields is not the same memory, so a merge cannot drop them.
		a, c := agentMemEntry{Body: "x"}, agentMemEntry{Body: "x"}
		a.Status = "quiet"
		if agentMemMoveSame(a, c) {
			t.Error("lifecycle fields must take part in the move's comparison")
		}
	})
}

// Revert restores the earlier version whole: a lint reason and a pin that came with the reverted
// change are undone, and fields the reverted change removed come back.
func TestLifeRevertRestoresWhole(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	anchors := []string{"a.go"}
	lifeSave(t, c, agentMemSaveReq{Name: "m", MemKind: sp("lesson"), Anchors: &anchors})
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	latest := func() string {
		l, err := agentMemLatestChange(rel + "/m.md")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) {
		e.Reasons = []agentMemReason{{By: "lint", Rule: "path-missing", Note: "gone", Revision: 2}}
		e.Pinned, e.Status, e.StatusBy = true, "quiet", "member"
	})
	if res, err := agentMemRevert(agentMemRevertReq{Commit: latest()}, lifeNow); err != nil || res.Revision != 3 {
		t.Fatalf("revert: %+v %v", res, err)
	}
	got := lifeStored(t, c, agentMemScopeProject, "m")
	if len(got.Reasons) != 0 || got.Pinned || got.Status != "" || got.StatusBy != "" || got.MemKind != "lesson" || len(got.Anchors) != 1 || got.AuthorKind != "member" {
		t.Fatalf("revert did not undo the lint reason and the pin: %+v pinned=%v", got.agentMemLife, got.Pinned)
	}

	// The other direction: a change that removed the fields is undone and they are back.
	lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) { e.agentMemLife = agentMemLife{} })
	if got := lifeStored(t, c, agentMemScopeProject, "m"); got.MemKind != "" {
		t.Fatalf("fixture: %+v", got.agentMemLife)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: latest()}, lifeNow); err != nil {
		t.Fatal(err)
	}
	got = lifeStored(t, c, agentMemScopeProject, "m")
	if got.MemKind != "lesson" || len(got.Anchors) != 1 || got.Revision != 5 {
		t.Fatalf("revert did not bring the fields back: %+v rev %d", got.agentMemLife, got.Revision)
	}
}

// A feedback reason goes with any body change, a lint or member reason does not (decision 3).
func TestLifeFeedbackReasonClearedByBodyChange(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	reasons := []agentMemReason{
		{By: "lint", Rule: "path-missing", Note: "x", Revision: 1},
		{By: "feedback", Rule: "stale", Note: "y", Revision: 1},
		{By: "member", Rule: "wrong", Note: "z", Revision: 1},
	}
	lifeSave(t, c, agentMemSaveReq{Name: "m", Body: "alpha"})
	lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) { e.Reasons = reasons })
	rules := func() string {
		var out []string
		for _, r := range lifeStored(t, c, agentMemScopeProject, "m").Reasons {
			out = append(out, r.By)
		}
		return strings.Join(out, ",")
	}
	lifeSave(t, c, agentMemSaveReq{Name: "m", Revision: 2, Body: "alpha"}) // same text
	if rules() != "lint,feedback,member" {
		t.Fatalf("an unchanged body cleared a reason: %s", rules())
	}
	lifeSave(t, c, agentMemSaveReq{Name: "m", Revision: 3, Body: "alpha."}) // punctuation only
	if rules() != "lint,member" {
		t.Fatalf("save: %s", rules())
	}
	lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) { e.Reasons = reasons })
	if _, err := agentMemPatch(c, agentMemPatchOf(5, agentMemPatchEdit{Old: "alpha.", New: sp("beta")}), lifeNow); err != nil {
		t.Fatal(err)
	}
	if rules() != "lint,member" {
		t.Fatalf("patch: %s", rules())
	}
}

// The wire carries the fields in camelCase and omits them on a plain memory.
func TestLifeWireShape(t *testing.T) {
	plain, _ := json.Marshal(agentMemEntry{Name: "n"})
	if strings.Contains(string(plain), "kind\"") && strings.Contains(string(plain), "status") || strings.Contains(string(plain), "reasons") || strings.Contains(string(plain), "stub") {
		t.Errorf("a plain entry gained keys: %s", plain)
	}
	e := agentMemEntry{Name: "n"}
	e.agentMemLife = lifeFull()
	b, _ := json.Marshal(e)
	for _, k := range []string{`"kind":"procedure"`, `"anchors":[`, `"status":"quiet"`, `"statusBy":"member"`, `"supersededBy":{`, `"invalidAt":`, `"reasons":[`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("wire lacks %s: %s", k, b)
		}
	}
}

// The scan covers the new fields: a secret in a reason's note, an anchor or a document target is
// withheld on read like one in the body, and a writer's own render scan refuses it.
func TestLifeSecretInFieldsIsWithheld(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	lifeSave(t, c, agentMemSaveReq{Name: "m"})
	// The apply does not scan (each writer does, before it); the reader still must.
	lifeSet(t, c, agentMemScopeProject, "m", func(e *agentMemEntry) {
		e.Reasons = []agentMemReason{{By: "lint", Rule: "x", Note: "key " + agentMemFakeAWS(), Revision: 1}}
	})
	out, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Withheld != 1 || len(out.Entries) != 0 {
		t.Fatalf("a secret in a reason note was shown: %+v", out)
	}
	if _, err := agentMemRead(c, "", "m"); agentMemCode(err) != errCodeMemorySecretDetected {
		t.Fatalf("read = %v", err)
	}

	e := agentMemEntry{Name: "n", Description: "d", Revision: 1, Body: "b"}
	e.Anchors = []string{"deploy with " + agentMemFakeAWS()}
	if len(agentMemScanText("memory", agentMemPublished(e))) == 0 {
		t.Error("an anchor is not in the published text the scan reads")
	}
	e.Anchors, e.SupersededBy = nil, &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "r", Path: "p/" + agentMemFakeAWS(), Commit: "0123abc"}}
	if len(agentMemScanText("memory", agentMemPublished(e))) == 0 {
		t.Error("a document target is not in the published text the scan reads")
	}
}

// A hand-edited file with a value outside the schema still loads, without that value; a future key
// is skipped.
func TestLifeLenientReader(t *testing.T) {
	raw := "---\nname: \"n\"\ndescription: \"d\"\nrevision: 2\nkind: lesson\nstatus: paused\nconfidence: 3\nreasons: not json\nanchors: [\"a\"]\n---\nbody\n"
	e, problems, ok := agentMemParseStrict([]byte(raw))
	if !ok || e.MemKind != "lesson" || e.Status != "" || len(e.Reasons) != 0 || len(e.Anchors) != 1 || e.Body != "body" {
		t.Fatalf("parse = %+v %v", e, ok)
	}
	if strings.Join(problems, ",") != "status,reasons" {
		t.Errorf("problems = %v", problems)
	}
	if agentMemExcluded(e) || !agentMemDescribable(e) {
		t.Error("an unreadable status must not hide a memory")
	}
}

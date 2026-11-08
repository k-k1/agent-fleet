package memoryx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A lifecycle write (lint reasons, a status) moves the stored file and its history on, but not the
// text the import put there. The imports' change detection must not read it as the member's edit.

func importLifeMut(e *agentMemEntry) {
	e.Reasons = []agentMemReason{{By: "lint", Rule: "missing-path", Note: "docs/x.md", Revision: e.Revision}}
	e.Status, e.StatusBy = "quiet", "member"
	e.MemKind, e.Anchors = "lesson", []string{"docs/x.md"}
}

func TestClaudeImportIgnoresLifecycleWrites(t *testing.T) {
	e := newClaudeImportEnv(t)
	c := agentMemCallerT(t, "claude-main")
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, n := range []string{"keep", "edited"} {
		p := e.file(n, "d", "user", "v1")
		_ = os.Chtimes(p, t0, t0)
	}
	e.apply(t0.Add(time.Hour), "keep", "edited")
	lifeSet(t, c, agentMemScopeProject, "keep", importLifeMut)
	lifeSet(t, c, agentMemScopeProject, "edited", importLifeMut)
	// A real save by the member still counts, lifecycle fields or not.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "edited", Description: "d", Type: "user", Body: "af text", Revision: 2}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	if st := agentMemImportStateFor(rel, "keep"); st != agentMemImportIntact {
		t.Fatalf("keep after a lifecycle write: state = %v, want intact", st)
	}

	// Unchanged source, even touched: no divergence.
	later := t0.Add(5 * time.Hour)
	_ = os.Chtimes(filepath.Join(e.memDir(e.slug), "keep.md"), later, later)
	pv, err := agentMemImportBuildAuto(e.slug, true)
	if err != nil {
		t.Fatal(err)
	}
	if it := e.item(pv, "keep"); it.Status != claudeImportUnchanged {
		t.Errorf("unchanged source = %+v", it)
	}

	// A body change at the source is a plain update for the import's own copy, and the lifecycle
	// fields survive it.
	for _, n := range []string{"keep", "edited"} {
		p := e.file(n, "d", "user", "v2")
		_ = os.Chtimes(p, later, later)
	}
	pv, err = agentMemImportBuildAuto(e.slug, true)
	if err != nil {
		t.Fatal(err)
	}
	if it := e.item(pv, "keep"); it.Status != claudeImportUpdate {
		t.Errorf("keep = %+v, want update", it)
	}
	if it := e.item(pv, "edited"); it.Status != claudeImportConflict || it.Reason != "af_changed" {
		t.Errorf("edited = %+v, want conflict af_changed", it)
	}
	before := lifeStored(t, c, agentMemScopeProject, "keep").agentMemLife
	e.apply(later.Add(time.Hour), "keep")
	got := lifeStored(t, c, agentMemScopeProject, "keep")
	if got.Body != "v2" || !reflect.DeepEqual(got.agentMemLife, before) {
		t.Errorf("update dropped or changed lifecycle fields: %+v", got)
	}
}

func TestMuseImportIgnoresLifecycleWrites(t *testing.T) {
	e := newMuseImportEnv(t)
	c := agentMemCallerT(t, "claude-main")
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, n := range []string{"keep", "edited"} {
		_ = os.Chtimes(e.note(n, "d", "user", "v1"), t0, t0)
	}
	e.apply(t0.Add(time.Hour), "keep", "edited")
	lifeSet(t, c, agentMemScopeProject, "keep", importLifeMut)
	lifeSet(t, c, agentMemScopeProject, "edited", importLifeMut)
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "edited", Description: "d", Type: "user", Body: "af text", Revision: 2}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Re-import from the unchanged source: nothing diverged.
	pv := e.preview()
	for _, n := range []string{"keep", "edited"} {
		if it := e.item(pv, n); it.Status != claudeImportUnchanged {
			t.Errorf("%s with unchanged source = %+v", n, it)
		}
	}

	later := t0.Add(5 * time.Hour)
	for _, n := range []string{"keep", "edited"} {
		_ = os.Chtimes(e.note(n, "d", "user", "v2"), later, later)
	}
	pv = e.preview()
	if it := e.item(pv, "keep"); it.Status != claudeImportUpdate {
		t.Errorf("keep = %+v, want update", it)
	}
	if it := e.item(pv, "edited"); it.Status != museImportDiverged || it.Reason != "af_changed" {
		t.Errorf("edited = %+v, want diverged af_changed", it)
	}
	before := lifeStored(t, c, agentMemScopeProject, "keep").agentMemLife
	e.apply(later.Add(time.Hour), "keep")
	got := lifeStored(t, c, agentMemScopeProject, "keep")
	if got.Body != "v2" || !reflect.DeepEqual(got.agentMemLife, before) {
		t.Errorf("update dropped or changed lifecycle fields: %+v", got)
	}
}

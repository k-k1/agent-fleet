package memoryx

import (
	"strings"
	"testing"
)

// The claude write-back: one predicate decides what AF selects, which copies it wrote earlier are
// withdrawn and what MEMORY.md lists. Export, change the memory, export again.
func TestLifecycleExportWithdrawsCopiesOfIneligibleMemories(t *testing.T) {
	cases := []struct {
		name string
		mut  func(e *agentMemEntry)
	}{
		{"suspect", func(e *agentMemEntry) { e.Reasons = visReason }},
		{"quiet", func(e *agentMemEntry) { e.Status, e.StatusBy = "quiet", "member" }},
		{"log", func(e *agentMemEntry) { e.MemKind = "log" }},
		{"archived", func(e *agentMemEntry) { e.Status, e.StatusBy = "archived", "member" }},
		// The status is still active: only superseded_by and invalid_at are set.
		{"superseded", func(e *agentMemEntry) {
			e.SupersededBy, e.InvalidAt = &agentMemSuperseded{Memory: "keeper"}, "2026-11-01T00:00:00Z"
		}},
		{"superseded-doc", func(e *agentMemEntry) {
			e.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "example.invalid/o/r", Path: "docs/a.md", Commit: "0123abc"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newClaudeExportEnv(t)
			e.save("keeper", "stays", "project", "kept")
			e.save("subject", "the subject", "project", "subject text")
			out := e.apply()
			if r := exportResult(out, "subject"); r.Result != "written" {
				t.Fatalf("first export: %+v", out.Results)
			}
			if !strings.Contains(e.read("MEMORY.md"), "(subject.md)") || e.read("subject.md") == "" {
				t.Fatal("the copy should be written and listed")
			}

			lifeSet(t, e.c, agentMemScopeProject, "subject", tc.mut)
			pv := e.preview()
			if got := e.status(pv, "subject"); got != claudeExportRemove {
				t.Fatalf("an untouched copy of a %s memory should be withdrawn, got %s", tc.name, got)
			}
			if strings.Contains(pv.indexNew, "(subject.md)") || !strings.Contains(pv.indexNew, "(keeper.md)") {
				t.Errorf("the previewed MEMORY.md must drop the withdrawn memory only:\n%s", pv.indexNew)
			}
			out = e.apply()
			if r := exportResult(out, "subject"); r.Result != "removed" {
				t.Fatalf("second export: %+v", out.Results)
			}
			if e.read("subject.md") != "" || strings.Contains(e.read("MEMORY.md"), "subject") || !strings.Contains(e.read("MEMORY.md"), "(keeper.md)") {
				t.Errorf("the copy or its MEMORY.md line is still there:\n%s", e.read("MEMORY.md"))
			}
			// Stable: nothing more to do, and the memory is not written again.
			if pv := e.preview(); pv.byName["subject"] != nil {
				t.Errorf("an ineligible memory is still selected: %+v", pv.byName["subject"])
			}

			// Eligible again (the member clears it): it is written back as new.
			lifeSet(t, e.c, agentMemScopeProject, "subject", func(m *agentMemEntry) { m.agentMemLife = agentMemLife{} })
			if got := e.status(e.preview(), "subject"); got != claudeExportNew {
				t.Errorf("after clearing, status = %s", got)
			}
		})
	}
}

// A copy somebody edited on the native side is reported as a conflict and left alone, and in
// neither case is it listed in MEMORY.md.
func TestLifecycleExportEditedCopyIsAConflictAndUnlisted(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("keeper", "stays", "project", "kept")
	e.save("subject", "the subject", "project", "subject text")
	e.apply()
	e.raw(e.slug, "subject.md", strings.Replace(e.read("subject.md"), "subject text", "subject text, edited in claude", 1))
	lifeSet(t, e.c, agentMemScopeProject, "subject", func(m *agentMemEntry) { m.Reasons = visReason })

	pv := e.preview()
	if it := pv.byName["subject"]; it == nil || it.Status != claudeExportConflict || it.Reason != "withdrawn_but_changed" {
		t.Fatalf("subject = %+v", it)
	}
	if strings.Contains(pv.indexNew, "subject") {
		t.Errorf("a withdrawn conflict is listed in MEMORY.md:\n%s", pv.indexNew)
	}
	e.apply("subject") // naming it does not make AF write or delete the member's edit
	if !strings.Contains(e.read("subject.md"), "edited in claude") {
		t.Error("the edited copy was touched")
	}
	if strings.Contains(e.read("MEMORY.md"), "subject") {
		t.Errorf("MEMORY.md lists the withdrawn conflict:\n%s", e.read("MEMORY.md"))
	}
}

// A memory that is ineligible from the start is never selected, and a native file AF did not write
// is not touched.
func TestLifecycleExportNeverSelectsIneligibleAndLeavesNativeFiles(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("quiet-one", "d", "project", "b")
	lifeSet(t, e.c, agentMemScopeProject, "quiet-one", func(m *agentMemEntry) { m.Status = "quiet" })
	e.raw(e.slug, "theirs.md", "---\nname: theirs\ndescription: native\n---\nmine\n")
	pv := e.preview()
	if pv.byName["quiet-one"] != nil {
		t.Errorf("a quiet memory was selected: %+v", pv.byName["quiet-one"])
	}
	e.apply()
	if e.read("quiet-one.md") != "" || e.read("theirs.md") == "" {
		t.Error("export wrote the ineligible memory or removed a native file")
	}
}

package memoryx

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// nativeWithExtras is a claude file as claude writes it today: nested metadata that carries
// fields AF does not model.
func nativeWithExtras(name, desc, typ, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\nmetadata:\n  node_type: memory\n  type: " + typ +
		"\n  originSessionId: 11111111-2222-3333-4444-555555555555\n  modified: 2026-09-01T00:00:00Z\n---\n\n" + body + "\n"
}

func (e *claudeExportEnv) importAll(names ...string) {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	res := e.apply2(names...)
	for _, n := range names {
		if r := claudeImportResult(res, n); r.Result != "imported" && r.Result != "updated" {
			e.t.Fatalf("import %s = %+v", n, r)
		}
	}
}

func (e *claudeExportEnv) apply2(names ...string) agentMemImportApplied {
	e.t.Helper()
	return e.claudeImportEnv.apply(e.now, names...)
}

// After an import, the write-back of the same project reports nothing to do: claude's files
// are the originals. The set mirrors real data: an underscore name, a description past the old
// 300-byte limit, a body past the old 64 KiB limit and one long line, a type AF drops, and the
// extra metadata claude writes.
func TestRoundTripImportThenWriteBackHasNoConflict(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.raw(e.slug, "project_95-invent.md", nativeWithExtras("project_95-invent", strings.Repeat("あ", 900), "project", "body one"))
	e.raw(e.slug, "feedback_wide.md", nativeWithExtras("feedback_wide", "wide", "feedback", strings.Repeat("x", 100<<10)))
	e.file("odd-type", "odd", "bogus", "kept as claude wrote it")
	e.file("plain", "plain", "user", "  indented\n\nsecond paragraph")
	names := []string{"project_95-invent", "feedback_wide", "odd-type", "plain"}

	pv := e.claudeImportEnv.preview()
	for _, n := range names {
		if it := e.item(pv, n); it.Status != claudeImportNew {
			t.Fatalf("%s = %s/%s, want new", n, it.Status, it.Reason)
		}
	}
	e.importAll(names...)

	ex := e.preview()
	for _, n := range names {
		if got := e.status(ex, n); got != claudeExportUnchanged {
			t.Errorf("%s = %s (%s), want unchanged", n, got, ex.byName[n].Reason)
		}
	}
	if ex.Counts[claudeExportConflict] != 0 {
		t.Errorf("counts = %v", ex.Counts)
	}
	// The second import preview agrees: nothing to bring in.
	if pv2 := e.claudeImportEnv.preview(); pv2.Counts[claudeImportNew]+pv2.Counts[claudeImportUpdate] != 0 {
		t.Errorf("second import preview = %v", pv2.Counts)
	}
}

// The judgement by source_hash, for a memory whose text AF does not reproduce: the type
// "bogus" is dropped on import, so the content hashes differ and only the recorded hash of
// claude's file says "this is the original".
func TestRoundTripJudgesByImportOrigin(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.raw(e.slug, "odd.md", nativeWithExtras("odd", "odd", "bogus", "original body"))
	e.raw(e.slug, "later.md", nativeWithExtras("later", "later", "bogus", "original body"))
	e.raw(e.slug, "pre.md", nativeWithExtras("pre", "pre", "bogus", "original body"))
	// A file that carries an AF write-back marker is judged by the marker's own rules, import
	// origin or not: its text was changed after AF wrote it.
	marked := strings.Replace(nativeWithExtras("marked", "marked", "bogus", "edited after a write-back"),
		"  modified:", "  af_source: \""+e.pid+"/marked@1\"\n  af_hash: \"0000\"\n  modified:", 1)
	e.raw(e.slug, "marked.md", marked)
	e.importAll("odd", "later", "pre", "marked")

	pv := e.preview()
	if got := e.status(pv, "odd"); got != claudeExportUnchanged {
		t.Fatalf("odd right after the import = %s (%s)", got, pv.byName["odd"].Reason)
	}

	if got := e.status(pv, "marked"); got != claudeExportConflict || pv.byName["marked"].Reason != "changed_since_write" {
		t.Errorf("marked = %s/%s, want conflict/changed_since_write", got, pv.byName["marked"].Reason)
	}

	// Edited in AF after the import: AF's version is newer, so the write-back updates, and the
	// metadata claude recorded stays in the file.
	e.save("later", "later", "project", "edited in AF")
	pv = e.preview()
	if got := e.status(pv, "later"); got != claudeExportUpdate {
		t.Fatalf("later after an AF edit = %s (%s), want update", got, pv.byName["later"].Reason)
	}
	e.apply()
	out := e.read("later.md")
	for _, want := range []string{"edited in AF", "  node_type: memory\n", "  originSessionId: 11111111-2222-3333-4444-555555555555\n",
		"  modified: 2026-09-01T00:00:00Z\n", "  type: project\n", "  af_source: "} {
		if !strings.Contains(out, want) {
			t.Errorf("updated file lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "  type:") != 1 {
		t.Errorf("type written twice:\n%s", out)
	}
	if got := e.status(e.preview(), "later"); got != claudeExportUnchanged {
		t.Errorf("later after the write-back = %s", got)
	}

	// Edited in claude after the import: the hash differs and the file is not AF's, a conflict.
	e.raw(e.slug, "pre.md", nativeWithExtras("pre", "pre", "bogus", "member changed this in claude"))
	pv = e.preview()
	if got := e.status(pv, "pre"); got != claudeExportConflict || pv.byName["pre"].Reason != "not_written_by_af" {
		t.Errorf("pre after a claude edit = %s/%s, want conflict", got, pv.byName["pre"].Reason)
	}
	// Nobody wrote it that way: a conflict as well.
	e.raw(e.slug, "odd.md", "---\nname: odd\ndescription: someone else\n---\ntheirs\n")
	if got := e.status(e.preview(), "odd"); got != claudeExportConflict {
		t.Errorf("odd after a foreign rewrite = %s", got)
	}
}

// legacyShortenedImport stores a memory the way the import wrote it while it cut descriptions:
// the first 297 bytes and an ellipsis, the full text as the body's first paragraph. The cut is
// computed here on its own, not by the code under test.
func (e *claudeExportEnv) legacyShortenedImport(name, desc, body string) (nativeHash string) {
	e.t.Helper()
	e.raw(e.slug, name+".md", nativeWithExtras(name, desc, "project", body))
	raw := []byte(nativeWithExtras(name, desc, "project", body))
	sum := sha256.Sum256(raw)
	nativeHash = hex.EncodeToString(sum[:])
	cut := desc[:297]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	conv := agentMemEntry{
		Name: name, Scope: agentMemScopeProject, Description: strings.TrimRight(cut, " ") + "…", Type: "project",
		AuthorKind: agentMemUnknown, AuthorSession: agentMemUnknown,
		Source: "claude:projects/" + e.slug + "/memory/" + name + ".md", SourceHash: nativeHash,
		Body: desc + "\n\n" + body,
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	if _, err := agentMemImportWrite(*agentMemProjectFor(e.clone), "projects/"+e.pid, &agentMemImportItem{entry: conv}, e.now); err != nil {
		e.t.Fatal(err)
	}
	return nativeHash
}

// A memory imported shortened is refreshed from claude's file while AF's copy is still the
// import's; before the refresh, the write-back does not call it a conflict.
func TestRefreshOfLegacyShortenedImport(t *testing.T) {
	e := newClaudeExportEnv(t)
	long := strings.Repeat("あ", 150) + strings.Repeat("い", 50) // 600 bytes
	for _, n := range []string{"clean", "edited", "handedit", "pinned", "changed", "forgotten"} {
		e.now = e.now.Add(time.Minute)
		e.legacyShortenedImport(n, long, "tail of "+n)
	}

	// The file claude has is the one imported, and AF's copy is the import's: not a conflict.
	ex := e.preview()
	for _, n := range []string{"clean", "edited", "handedit", "pinned", "changed", "forgotten"} {
		if got := e.status(ex, n); got != claudeExportUnchanged {
			t.Errorf("legacy %s in the write-back = %s (%s), want unchanged", n, got, ex.byName[n].Reason)
		}
	}

	// AF edits one after the import, claude changes another, one is forgotten.
	e.save("edited", "my own short description", "project", "AF edit")
	// A pin is the member's change too, and it leaves the text exactly as imported.
	if _, err := agentMemPin(agentMemPinReq{Scope: agentMemScopeProject, Project: e.pid, Name: "pinned", Pinned: true}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A hand edit of the stored file makes no commit; the bytes still tell.
	hand := filepath.Join(agentMemDir(), "projects", e.pid, "handedit.md")
	raw, _ := os.ReadFile(hand)
	memoryWrite(t, hand, strings.Replace(string(raw), "tail of handedit", "tail of handedit, fixed by hand", 1))
	e.raw(e.slug, "changed.md", nativeWithExtras("changed", long, "project", "claude changed this later"))
	if _, err := agentMemForget(e.c, agentMemForgetReq{Name: "forgotten", Revision: 1}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	pv := e.claudeImportEnv.preview()
	if it := e.item(pv, "clean"); it.Status != claudeImportUpdate || it.Description != long {
		t.Fatalf("clean = %s, %d chars: want a refresh with the full description", it.Status, utf8.RuneCountInString(it.Description))
	}
	if it := e.item(pv, "edited"); it.Status != claudeImportUnchanged {
		t.Errorf("an AF edit must not be overwritten: edited = %s", it.Status)
	}
	if it := e.item(pv, "handedit"); it.Status != claudeImportUnchanged {
		t.Errorf("a hand-edited copy must not be overwritten: handedit = %s", it.Status)
	}
	if it := e.item(pv, "pinned"); it.Status != claudeImportUnchanged {
		t.Errorf("a pinned copy is the member's: pinned = %s", it.Status)
	}
	if it := e.item(pv, "forgotten"); it.Status != claudeImportForgotten {
		t.Errorf("forgotten must stay forgotten: %s", it.Status)
	}
	// Changed in claude after the import: the existing newer-claude rule applies, not the refresh.
	if it := e.item(pv, "changed"); it.Status != claudeImportUpdate || !strings.Contains(it.entry.Body, "claude changed this later") {
		t.Errorf("changed = %s", it.Status)
	}

	res := e.apply2("clean")
	if r := claudeImportResult(res, "clean"); r.Result != "updated" {
		t.Fatalf("refresh = %+v", r)
	}
	got, err := agentMemRead(e.c, "", "clean")
	if err != nil || got.Description != long || got.Body != "tail of clean" || got.Revision != 2 {
		t.Fatalf("refreshed = rev %d, %d chars, body %q, %v", got.Revision, utf8.RuneCountInString(got.Description), got.Body, err)
	}
	if ed, err := agentMemRead(e.c, "", "edited"); err != nil || ed.Description != "my own short description" || ed.Body != "AF edit" {
		t.Errorf("edited was touched: %+v, %v", ed, err)
	}
	if _, err := agentMemRead(e.c, "", "forgotten"); err == nil {
		t.Error("forgotten came back")
	}
	// After the refresh both sides agree, and so does a second preview.
	if st := e.status(e.preview(), "clean"); st != claudeExportUnchanged {
		t.Errorf("clean in the write-back after the refresh = %s", st)
	}
	if it := e.item(e.claudeImportEnv.preview(), "clean"); it.Status != claudeImportUnchanged {
		t.Errorf("clean in the second import preview = %s", it.Status)
	}
}

// The write-back of a legacy shortened import after an AF edit is an update, never a conflict.
func TestWriteBackOfEditedLegacyImportIsUpdate(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.legacyShortenedImport("legacy", strings.Repeat("あ", 150)+strings.Repeat("い", 50), "tail")
	e.save("legacy", "edited description", "project", "edited body")
	pv := e.preview()
	if got := e.status(pv, "legacy"); got != claudeExportUpdate {
		t.Fatalf("legacy after an AF edit = %s (%s), want update", got, pv.byName["legacy"].Reason)
	}
	e.apply()
	if out := e.read("legacy.md"); !strings.Contains(out, "edited body") || !strings.Contains(out, "originSessionId") {
		t.Errorf("update lost text or metadata:\n%s", out)
	}
}

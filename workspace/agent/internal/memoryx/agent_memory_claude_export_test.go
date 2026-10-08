package memoryx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// claudeExportEnv is the import's environment seen from the AF side: AF memories are saved
// through the real save path, and the native directory is the main clone's slug.
type claudeExportEnv struct {
	*claudeImportEnv
	c   agentMemCaller
	now time.Time
}

func newClaudeExportEnv(t *testing.T) *claudeExportEnv {
	e := &claudeExportEnv{claudeImportEnv: newClaudeImportEnv(t), now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	e.c = agentMemCallerT(t, "claude-main")
	return e
}

func (e *claudeExportEnv) save(name, desc, typ, body string) {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	req := agentMemSaveReq{Name: name, Description: desc, Type: typ, Body: body}
	if cur, err := agentMemRead(e.c, "", name); err == nil {
		req.Revision = cur.Revision
	}
	if _, err := agentMemSave(e.c, req, e.now); err != nil {
		e.t.Fatal(err)
	}
}

func (e *claudeExportEnv) preview() *agentMemExportPreview {
	e.t.Helper()
	pv, err := agentMemExportPreviewFor(e.pid)
	if err != nil {
		e.t.Fatal(err)
	}
	return pv
}

func (e *claudeExportEnv) apply(overwrite ...string) agentMemExportApplied {
	e.t.Helper()
	out, err := e.applyErr(overwrite...)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *claudeExportEnv) applyErr(overwrite ...string) (agentMemExportApplied, error) {
	pv := e.preview()
	return agentMemExportApply(agentMemExportReq{Project: e.pid, Token: pv.Token, Overwrite: overwrite}, e.now.Add(time.Hour))
}

func (e *claudeExportEnv) status(pv *agentMemExportPreview, name string) string {
	e.t.Helper()
	it := pv.byName[name]
	if it == nil {
		e.t.Fatalf("no item %q in %v", name, pv.Counts)
	}
	return it.Status
}

func (e *claudeExportEnv) read(name string) string {
	b, err := os.ReadFile(filepath.Join(e.memDir(e.slug), name))
	if err != nil {
		return ""
	}
	return string(b)
}

func exportResult(a agentMemExportApplied, name string) agentMemExportResult {
	for _, r := range a.Results {
		if r.Name == name {
			return r
		}
	}
	return agentMemExportResult{}
}

// Every status the preview names, in one directory.
func TestClaudeExportStatuses(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("fresh", "brand new", "feedback", "fresh body")
	e.save("stable", "same text", "project", "stable body")
	e.save("moving", "v1", "project", "moving v1")
	e.save("edited", "af text", "project", "edited v1")
	e.save("foreign", "af text", "project", "af body")
	e.save("doomed", "will be forgotten", "project", "doomed body")
	e.apply() // writes all six
	// Now diverge: AF updates moving and edited; the member edits edited and foreign natively;
	// doomed is forgotten in AF; a native-only file appears.
	e.save("moving", "v2", "project", "moving v2")
	e.save("edited", "af text", "project", "edited v2")
	e.raw(e.slug, "edited.md", strings.Replace(e.read("edited.md"), "edited v1", "member wrote this", 1))
	e.raw(e.slug, "foreign.md", "---\nname: foreign\ndescription: theirs\n---\nnot written by AF\n")
	if _, err := agentMemForget(e.c, agentMemForgetReq{Name: "doomed", Revision: 1}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.raw(e.slug, "native_note.md", "---\nname: native_note\ndescription: only in claude\n---\nbody\n")

	pv := e.preview()
	want := map[string]string{
		"fresh": claudeExportUnchanged, "stable": claudeExportUnchanged, "moving": claudeExportUpdate,
		"edited": claudeExportConflict, "foreign": claudeExportConflict, "doomed": claudeExportRemove,
		"native_note": claudeExportNativeOnly,
	}
	for n, s := range want {
		if got := e.status(pv, n); got != s {
			t.Errorf("%s = %s, want %s (%+v)", n, got, s, pv.byName[n])
		}
	}
	if pv.byName["edited"].Reason != "changed_since_write" || pv.byName["foreign"].Reason != "not_written_by_af" {
		t.Errorf("reasons: %q %q", pv.byName["edited"].Reason, pv.byName["foreign"].Reason)
	}
	e.save("brand-new", "new one", "user", "b")
	if got := e.status(e.preview(), "brand-new"); got != claudeExportNew {
		t.Errorf("brand-new = %s", got)
	}
}

// A conflict is skipped unless the request names it; a native-only file is never deleted.
func TestClaudeExportConflictOverrideAndNativeOnlyGuard(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("clash", "af text", "project", "af body")
	e.save("clash2", "af text", "project", "af body 2")
	e.raw(e.slug, "clash.md", "---\nname: clash\ndescription: theirs\n---\nmember body\n")
	e.raw(e.slug, "clash2.md", "---\nname: clash2\ndescription: theirs\n---\nmember body 2\n")
	e.raw(e.slug, "keep_me.md", "---\nname: keep_me\ndescription: native only\n---\nkeep\n")
	e.raw(e.slug, "odd name.md", "---\nname: odd\ndescription: spaces\n---\nkeep\n")

	out := e.apply()
	if r := exportResult(out, "clash"); r.Result != "skipped" || r.Reason != "conflict" {
		t.Errorf("clash without override = %+v", r)
	}
	if !strings.Contains(e.read("clash.md"), "member body") {
		t.Error("a conflict was overwritten without being named")
	}
	out = e.apply("clash")
	if r := exportResult(out, "clash"); r.Result != "updated" {
		t.Errorf("clash with override = %+v", r)
	}
	if !strings.Contains(e.read("clash.md"), "af body") || !strings.Contains(e.read("clash2.md"), "member body 2") {
		t.Error("only the named conflict may be replaced")
	}
	// A name that is not a conflict in the request changes nothing.
	for _, n := range []string{"keep_me.md", "odd name.md"} {
		if e.read(n) == "" {
			t.Errorf("%s was deleted", n)
		}
	}
	if !strings.Contains(e.read("MEMORY.md"), "keep_me") {
		t.Errorf("native-only file missing from the index: %q", e.read("MEMORY.md"))
	}
}

// Removal covers only a file AF wrote and nobody changed; a changed one is a conflict.
func TestClaudeExportRemoveOnlyUnchangedAFFiles(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("gone-a", "d", "project", "a")
	e.save("gone-b", "d", "project", "b")
	e.apply()
	e.raw(e.slug, "gone-b.md", strings.Replace(e.read("gone-b.md"), "\nb\n", "\nb edited\n", 1))
	e.raw(e.slug, "plain.md", "---\nname: plain\ndescription: d\n---\nb\n")
	for _, n := range []string{"gone-a", "gone-b"} {
		if _, err := agentMemForget(e.c, agentMemForgetReq{Name: n, Revision: 1}, e.now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pv := e.preview()
	if e.status(pv, "gone-a") != claudeExportRemove || e.status(pv, "gone-b") != claudeExportConflict || e.status(pv, "plain") != claudeExportNativeOnly {
		t.Fatalf("statuses: %v", pv.Counts)
	}
	e.apply()
	if e.read("gone-a.md") != "" {
		t.Error("an unchanged AF-written file of a forgotten memory should be removed")
	}
	if e.read("gone-b.md") == "" || e.read("plain.md") == "" {
		t.Error("a changed AF file and a native-only file must stay")
	}
	// Even when named, a forgotten-but-changed file is not removed (no AF text to put there).
	e.apply("gone-b")
	if e.read("gone-b.md") == "" {
		t.Error("overwrite must not delete a member-edited file")
	}
}

// The written file has claude's shape and no AF-only fields; an AF memory that is withheld
// (fails the scan) is neither written nor makes its native copy look forgotten.
func TestClaudeExportFileShapeAndWithheld(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("shape", "a: \"quoted\" desc", "feedback", "line one\n\nline two")
	e.save("tainted", "d", "project", "clean for now")
	e.apply()
	got := e.read("shape.md")
	for _, must := range []string{"---\nname: shape\n", "metadata:\n  type: feedback\n", "  af_source: \"" + e.pid + "/shape@1\"\n", "  af_hash: \"", "---\nline one\n\nline two\n"} {
		if !strings.Contains(got, must) {
			t.Errorf("file lacks %q:\n%s", must, got)
		}
	}
	for _, banned := range []string{"author", "session", "created", "revision", "updated"} {
		if strings.Contains(got, banned) {
			t.Errorf("AF-only field %q written:\n%s", banned, got)
		}
	}
	// Taint the AF file by hand with a secret-shaped value built at runtime.
	key := "ghp_" + strings.Repeat("q7Zx", 9)
	p := filepath.Join(agentMemDir(), "projects", e.pid, "tainted.md")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.Replace(b, []byte("clean for now"), []byte("token "+key), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	pv := e.preview()
	if it := pv.byName["tainted"]; pv.Withheld != 1 || it == nil || it.Status != claudeExportNativeOnly || it.Reason != "af_withheld" {
		t.Errorf("a withheld AF memory is counted and its native copy kept: withheld=%d item=%+v", pv.Withheld, it)
	}
	out := e.apply()
	if e.read("tainted.md") == "" {
		t.Error("the native copy of a withheld AF memory must not be removed")
	}
	wire, _ := json.Marshal([]any{pv, out})
	if strings.Contains(string(wire), key) || strings.Contains(logs.String(), key) || strings.Contains(e.read("MEMORY.md"), key) {
		t.Error("a secret leaked")
	}
}

// The scan before writing flags a secret in any field, and the findings are masked.
func TestClaudeExportScanFlagsSecretsMasked(t *testing.T) {
	e := newClaudeExportEnv(t)
	key := "ghp_" + strings.Repeat("q7Zx", 9)
	en := agentMemEntry{Name: "k", Description: "key " + key, Body: "b", Revision: 1}
	data, _ := agentMemExportRender(e.pid, en)
	f := agentMemExportScan(en, data)
	if len(f) == 0 {
		t.Fatal("the scan should flag the entry")
	}
	if wire, _ := json.Marshal(f); strings.Contains(string(wire), key) {
		t.Error("findings carry the secret")
	}
}

// MEMORY.md stays within claude's load limit, with a closing line for what did not fit.
func TestClaudeExportIndexLimits(t *testing.T) {
	e := newClaudeExportEnv(t)
	for i := 0; i < 260; i++ {
		e.save(fmt.Sprintf("mem-%03d", i), strings.Repeat("あ", 100), "project", "body")
	}
	e.apply()
	idx := e.read("MEMORY.md")
	lines := strings.Split(strings.TrimRight(idx, "\n"), "\n")
	if len(lines) > claudeExportIndexMaxLines || len(idx) > claudeExportIndexMaxBytes {
		t.Fatalf("index %d lines %d bytes", len(lines), len(idx))
	}
	if !utf8.ValidString(idx) {
		t.Error("index is not valid UTF-8")
	}
	last := lines[len(lines)-1]
	var more int
	if _, err := fmt.Sscanf(last, "%d more memories in this directory; search them by name", &more); err != nil || more == 0 {
		t.Fatalf("closing line = %q", last)
	}
	if listed := len(lines) - 1; listed+more != 260 {
		t.Errorf("listed %d + more %d != 260", listed, more)
	}
	if !strings.HasPrefix(lines[0], "- [") || !strings.Contains(lines[0], "](") {
		t.Errorf("first line = %q", lines[0])
	}
	// The cut is on a rune boundary.
	if l := claudeExportLine(claudeExportIndexRow{"x", strings.Repeat("あ", 400)}); !utf8.ValidString(l) || utf8.RuneCountInString(l) > 200 {
		t.Errorf("line = %q", l)
	}
	// A short set fits whole, with no closing line.
	text, listed := claudeExportBuildIndex([]claudeExportIndexRow{{"a", "d"}, {"b", ""}})
	if listed != 2 || strings.Contains(text, "more memories") || !strings.Contains(text, "- [b](b.md)\n") {
		t.Errorf("small index = %q", text)
	}
}

// A snapshot of claude's memory is taken before the first write; when it cannot be, nothing is
// written.
func TestClaudeExportSnapshotFirstAndRefusal(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.raw(e.slug, "native_note.md", "---\nname: native_note\ndescription: before\n---\nb\n")
	e.save("one", "d", "project", "b")
	e.apply()
	infos, err := memoryListSnapshots(10, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range infos {
		if s.Trigger == memoryTriggerPreExport {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pre-export snapshot in %+v", infos)
	}
	if e.read("one.md") == "" {
		t.Fatal("not written")
	}

	// Now make the snapshot fail: the staging path is a regular file.
	e.save("two", "d", "project", "b")
	if err := os.RemoveAll(memoryStagingDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryStagingDir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.applyErr(); err == nil {
		t.Fatal("expected the apply to fail when the snapshot fails")
	}
	if e.read("two.md") != "" || strings.Contains(e.read("MEMORY.md"), "two") {
		t.Error("something was written although the snapshot failed")
	}
}

// A symlink anywhere on the target path refuses the write; a symlink leaf is never replaced.
func TestClaudeExportSymlinkRefusal(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	outside := t.TempDir()
	// The memory directory itself is a symlink out of the config dir.
	memoryMkdirAll(t, filepath.Join(filepath.Dir(e.memDir(e.slug))))
	if err := os.Symlink(outside, e.memDir(e.slug)); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemExportPreviewFor(e.pid); err == nil {
		t.Error("preview through a symlinked memory directory should refuse")
	}
	if _, err := agentMemExportApply(agentMemExportReq{Project: e.pid}, e.now); err == nil {
		t.Error("apply through a symlinked memory directory should refuse")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("wrote through the symlink: %v", ents)
	}
	// A leaf symlink is a locked conflict even when named; the target is untouched.
	if err := os.Remove(e.memDir(e.slug)); err != nil {
		t.Fatal(err)
	}
	memoryMkdirAll(t, e.memDir(e.slug))
	target := filepath.Join(outside, "t.md")
	memoryWrite(t, target, "target\n")
	if err := os.Symlink(target, filepath.Join(e.memDir(e.slug), "one.md")); err != nil {
		t.Fatal(err)
	}
	out := e.apply("one")
	if r := exportResult(out, "one"); r.Result != "skipped" {
		t.Errorf("symlink leaf = %+v", r)
	}
	if b, _ := os.ReadFile(target); string(b) != "target\n" {
		t.Error("the symlink target was written")
	}
	if st, err := os.Lstat(filepath.Join(e.memDir(e.slug), "one.md")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink leaf was replaced")
	}
}

// A stale token (anything moved since the preview) is refused before anything is written.
func TestClaudeExportStaleTokenRefused(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	pv := e.preview()
	e.save("one", "d", "project", "changed")
	if _, err := agentMemExportApply(agentMemExportReq{Project: e.pid, Token: pv.Token}, e.now); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("stale token err = %v", err)
	}
	if e.read("one.md") != "" {
		t.Error("written despite a stale token")
	}
}

// Writing back and importing again changes nothing: the written file is "unchanged" to the
// import even though its mtime is newer than AF's last update.
func TestClaudeImportTreatsWrittenBackFileAsUnchanged(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("loop", "d", "project", "b")
	e.apply()
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(filepath.Join(e.memDir(e.slug), "loop.md"), future, future); err != nil {
		t.Fatal(err)
	}
	pv, err := agentMemImportPreviewFor(e.slug)
	if err != nil {
		t.Fatal(err)
	}
	if it := pv.byName["loop"]; it == nil || it.Status != claudeImportUnchanged {
		t.Fatalf("import of a written-back file = %+v", it)
	}
	// An edit after the write-back is a real claude change and still imports.
	e.raw(e.slug, "loop.md", strings.Replace(e.read("loop.md"), "\nb\n", "\nb edited\n", 1))
	if err := os.Chtimes(filepath.Join(e.memDir(e.slug), "loop.md"), future, future); err != nil {
		t.Fatal(err)
	}
	pv, _ = agentMemImportPreviewFor(e.slug)
	if it := pv.byName["loop"]; it == nil || it.Status != claudeImportUpdate {
		t.Fatalf("import of an edited written-back file = %+v", it)
	}
}

// Re-running a write-back right after one reports everything unchanged and writes nothing.
func TestClaudeExportIdempotent(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	e.save("two", "d", "user", "b")
	e.apply()
	pv := e.preview()
	if pv.Counts[claudeExportUnchanged] != 2 || pv.Index != "unchanged" {
		t.Fatalf("counts %v index %s", pv.Counts, pv.Index)
	}
	if out := e.apply(); len(out.Results) != 0 || out.Snapshot != "" {
		t.Errorf("second apply did work: %+v", out)
	}
}

// User-scope memories are counted, not written; a memory limited to other kinds is left out.
func TestClaudeExportUserScopeAndKinds(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("proj", "d", "project", "b")
	if _, err := agentMemSave(e.c, agentMemSaveReq{Name: "mine", Scope: "user", Description: "d", Body: "b"}, e.now); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemSave(e.c, agentMemSaveReq{Name: "codex-only", Description: "d", Body: "b", Kinds: []string{"codex"}}, e.now); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	if pv.UserScope != 1 || pv.NotForClaude != 1 || pv.byName["mine"] != nil || pv.byName["codex-only"] != nil {
		t.Errorf("user=%d notForClaude=%d", pv.UserScope, pv.NotForClaude)
	}
}

func (e *claudeExportEnv) setHook(f func(stage string)) {
	agentMemExportTestHook = f
	e.t.Cleanup(func() { agentMemExportTestHook = nil })
}

// A name past the cap on the directory listing is still looked up by name: an existing member
// file is a conflict, never "new", and a truncated directory is not written to at all.
func TestClaudeExportListingCapDoesNotHideFiles(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("target", "af", "project", "af body")
	for i := 0; i < agentMemImportMaxFiles+50; i++ {
		e.raw(e.slug, fmt.Sprintf("filler_%04d.md", i), "x\n")
	}
	e.raw(e.slug, "target.md", "---\nname: target\ndescription: theirs\n---\nmember\n")
	pv := e.preview()
	if !pv.Truncated || e.status(pv, "target") != claudeExportConflict {
		t.Fatalf("truncated=%v status=%s", pv.Truncated, e.status(pv, "target"))
	}
	if _, err := e.applyErr("target"); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("apply on a truncated directory = %v", err)
	}
	if !strings.Contains(e.read("target.md"), "member") {
		t.Error("member file replaced")
	}
}

// A symlink anywhere on the path, the config dir included, is refused.
func TestClaudeExportConfigDirLinkRefused(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	cfg := filepath.Dir(filepath.Dir(filepath.Dir(e.memDir(e.slug))))
	real := cfg + "-real"
	if err := os.Rename(cfg, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemExportPreviewFor(e.pid); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("preview through a linked config dir = %v", err)
	}
	if _, err := agentMemExportApply(agentMemExportReq{Project: e.pid}, e.now); err == nil {
		t.Error("apply through a linked config dir should refuse")
	}
	if _, err := os.Stat(filepath.Join(real, "projects", e.slug, "memory", "one.md")); err == nil {
		t.Error("written through the link")
	}
}

// The directory evaluated is the one written: swapping it for another after the snapshot is
// refused, and swapping a leaf is caught right before it is touched.
func TestClaudeExportDirectoryAndLeafSwapped(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	e.raw(e.slug, "keep_me.md", "---\nname: keep_me\ndescription: d\n---\nb\n")
	dir := e.memDir(e.slug)
	// Parent swapped after the snapshot.
	e.setHook(func(stage string) {
		if stage != "after-snapshot" {
			return
		}
		if err := os.Rename(dir, dir+"-moved"); err != nil {
			t.Error(err)
		}
		memoryWrite(t, filepath.Join(dir, "one.md"), "member file\n")
	})
	if _, err := e.applyErr(); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("swapped directory = %v", err)
	}
	if e.read("one.md") != "member file\n" {
		t.Error("the swapped-in directory was written to")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+"-moved", dir); err != nil {
		t.Fatal(err)
	}
	// A new file's leaf appears before the write.
	e.setHook(func(stage string) {
		if stage == "before-write" {
			memoryWrite(t, filepath.Join(dir, "one.md"), "member file\n")
		}
	})
	out := e.apply()
	if r := exportResult(out, "one"); r.Result != "skipped" || r.Reason != "changed_since_preview" {
		t.Errorf("new over an appeared file = %+v", r)
	}
	if e.read("one.md") != "member file\n" {
		t.Error("a file that appeared was replaced")
	}
	// An update's leaf becomes a symlink before the write.
	e.setHook(nil)
	_ = os.Remove(filepath.Join(dir, "one.md"))
	e.apply()
	e.save("one", "d", "project", "b2")
	target := filepath.Join(t.TempDir(), "t.md")
	memoryWrite(t, target, "target\n")
	e.setHook(func(stage string) {
		if stage == "before-write" {
			_ = os.Remove(filepath.Join(dir, "one.md"))
			_ = os.Symlink(target, filepath.Join(dir, "one.md"))
		}
	})
	out = e.apply()
	if r := exportResult(out, "one"); r.Result != "skipped" {
		t.Errorf("update over a symlink = %+v", r)
	}
	if b, _ := os.ReadFile(target); string(b) != "target\n" {
		t.Error("symlink target written")
	}
}

// A native file at a newer or equal revision is never rolled back by an older AF text.
func TestClaudeExportDoesNotRollBack(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "v1")
	e.save("one", "d", "project", "v2")
	e.apply() // native @2
	old := agentMemEntry{Name: "one", Scope: "project", Description: "d", Revision: 1, AuthorKind: "unknown",
		AuthorSession: "unknown", Created: "2026-10-08T00:00:00Z", Updated: "2026-10-08T00:00:00Z", Body: "v1"}
	if err := os.WriteFile(filepath.Join(agentMemDir(), "projects", e.pid, "one.md"), agentMemRender(old), 0o600); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	if e.status(pv, "one") != claudeExportConflict || pv.byName["one"].Reason != "af_not_newer" {
		t.Fatalf("item = %+v", pv.byName["one"])
	}
	e.apply()
	if !strings.Contains(e.read("one.md"), "v2") {
		t.Error("rolled back without being asked")
	}
}

// Leading spaces are content: indenting a written-back body is a member edit, so it is a conflict,
// blocks removal, and the import still sees it as a change.
func TestClaudeExportIndentEditIsAnEdit(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "original")
	e.apply()
	e.raw(e.slug, "one.md", strings.Replace(e.read("one.md"), "\noriginal\n", "\n    original\n", 1))
	e.save("one", "d", "project", "updated in af")
	pv := e.preview()
	if e.status(pv, "one") != claudeExportConflict || pv.byName["one"].Reason != "changed_since_write" {
		t.Fatalf("item = %+v", pv.byName["one"])
	}
	if _, err := agentMemForget(e.c, agentMemForgetReq{Name: "one", Revision: 2}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st := e.status(e.preview(), "one"); st != claudeExportConflict {
		t.Errorf("forgotten + edited = %s, want conflict (never removed)", st)
	}
}

// A MEMORY.md edited between preview and apply makes the preview stale.
func TestClaudeExportIndexEditMakesTokenStale(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	e.raw(e.slug, "MEMORY.md", "version A\n")
	pv := e.preview()
	e.raw(e.slug, "MEMORY.md", "version B, the member's\n")
	if _, err := agentMemExportApply(agentMemExportReq{Project: e.pid, Token: pv.Token}, e.now); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("stale index token = %v", err)
	}
	if e.read("MEMORY.md") != "version B, the member's\n" {
		t.Error("index overwritten")
	}
}

// A write that did not happen is not listed in MEMORY.md, and an index that could not be written
// is reported rather than passed off as success.
func TestClaudeExportPartialFailureAndIndexFailure(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("fresh", "d", "project", "b")
	e.save("blocked", "d", "project", "b")
	dir := e.memDir(e.slug)
	e.setHook(func(stage string) {
		if stage == "before-write" {
			memoryMkdirAll(t, filepath.Join(dir, "blocked.md"))
		}
	})
	out := e.apply()
	if r := exportResult(out, "blocked"); r.Result != "skipped" {
		t.Errorf("blocked = %+v", r)
	}
	idx := e.read("MEMORY.md")
	if !strings.Contains(idx, "(fresh.md)") || strings.Contains(idx, "(blocked.md)") {
		t.Errorf("index = %q", idx)
	}

	e.setHook(func(stage string) {
		if stage == "before-write" {
			memoryWrite(t, filepath.Join(dir, "MEMORY.md"), "member edit\n")
		}
	})
	e.save("another", "d", "project", "b")
	out = e.apply()
	if out.Index != "failed" || e.read("MEMORY.md") != "member edit\n" {
		t.Errorf("index = %s content %q", out.Index, e.read("MEMORY.md"))
	}
}

// When the last memory is forgotten the project is still offered, so its written-back files can
// be removed.
func TestClaudeExportLastForgottenProjectStillListed(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("only", "d", "project", "b")
	e.apply()
	if _, err := agentMemForget(e.c, agentMemForgetReq{Name: "only", Revision: 1}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	src, err := agentMemExportList()
	if err != nil || len(src.Projects) != 1 || src.Projects[0].Project.ID != e.pid || src.Projects[0].Count != 0 {
		t.Fatalf("sources = %+v %v", src, err)
	}
	e.apply()
	if e.read("only.md") != "" {
		t.Error("not removed")
	}
	if src, _ := agentMemExportList(); len(src.Projects) != 0 {
		t.Errorf("still listed after removal: %+v", src)
	}
}

// The commit itself is race-safe: a file that appears or changes between the check and the
// rename is kept, and a new file only becomes visible complete.
func TestClaudeExportCommitRaces(t *testing.T) {
	e := newClaudeExportEnv(t)
	e.save("one", "d", "project", "b")
	e.save("two", "d", "project", "b")
	dir := e.memDir(e.slug)
	e.setHook(func(stage string) {
		switch stage {
		case "after-stage:one.md":
			if e.read("one.md") != "" {
				t.Error("a half-written file is visible under the final name")
			}
			memoryWrite(t, filepath.Join(dir, "one.md"), "---\nname: one\ndescription: member one\n---\nmine\n")
		}
	})
	out := e.apply()
	if r := exportResult(out, "one"); r.Result != "skipped" || r.Reason != "changed_since_preview" {
		t.Errorf("new over an appeared file = %+v", r)
	}
	if !strings.Contains(e.read("one.md"), "mine") {
		t.Error("the member file that appeared was replaced")
	}
	// It is listed in the rewritten index with its current description, written complete.
	if !strings.Contains(e.read("MEMORY.md"), "(one.md) — member one") {
		t.Errorf("index = %q", e.read("MEMORY.md"))
	}
	e.setHook(nil)
	_ = os.Remove(filepath.Join(dir, "one.md"))
	e.apply()

	// An update whose leaf becomes a symlink, then a different member file, after staging.
	e.save("one", "d", "project", "b2")
	target := filepath.Join(t.TempDir(), "t.md")
	memoryWrite(t, target, "target\n")
	e.setHook(func(stage string) {
		if stage == "after-stage:one.md" {
			_ = os.Remove(filepath.Join(dir, "one.md"))
			_ = os.Symlink(target, filepath.Join(dir, "one.md"))
		}
	})
	out = e.apply()
	if r := exportResult(out, "one"); r.Result != "skipped" {
		t.Errorf("update raced by a symlink = %+v", r)
	}
	if st, err := os.Lstat(filepath.Join(dir, "one.md")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Error("the racing symlink was replaced")
	}
	_ = os.Remove(filepath.Join(dir, "one.md"))
	e.setHook(nil)
	e.apply()

	// An update whose leaf is edited by the member after staging: the edit survives and the
	// index shows its current description.
	e.save("one", "d", "project", "b3")
	e.setHook(func(stage string) {
		if stage == "after-stage:one.md" {
			memoryWrite(t, filepath.Join(dir, "one.md"), "---\nname: one\ndescription: edited just now\n---\nmine\n")
		}
	})
	out = e.apply()
	if !strings.Contains(e.read("one.md"), "mine") || !strings.Contains(e.read("MEMORY.md"), "edited just now") {
		t.Errorf("edit lost or index stale: %q / %q", e.read("one.md"), e.read("MEMORY.md"))
	}
	if r := exportResult(out, "one"); r.Result != "skipped" {
		t.Errorf("raced update = %+v", r)
	}
	e.setHook(nil)

	// A removal whose leaf is replaced by a member file just before the move.
	e2 := newClaudeExportEnv(t)
	e2.save("gone", "d", "project", "b")
	e2.apply()
	if _, err := agentMemForget(e2.c, agentMemForgetReq{Name: "gone", Revision: 1}, e2.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	dir2 := e2.memDir(e2.slug)
	e2.setHook(func(stage string) {
		if stage == "before-remove:gone.md" {
			memoryWrite(t, filepath.Join(dir2, "gone.md"), "---\nname: gone\ndescription: new member file\n---\nmine\n")
		}
	})
	e2.apply()
	if !strings.Contains(e2.read("gone.md"), "mine") {
		t.Error("a member file that replaced the leaf was deleted")
	}

	// The index is swapped by the member after staging: it is kept and reported.
	e3 := newClaudeExportEnv(t)
	e3.save("x", "d", "project", "b")
	e3.raw(e3.slug, "MEMORY.md", "old\n")
	dir3 := e3.memDir(e3.slug)
	e3.setHook(func(stage string) {
		if stage == "after-stage:MEMORY.md" {
			memoryWrite(t, filepath.Join(dir3, "MEMORY.md"), "member edit\n")
		}
	})
	out = e3.apply()
	if out.Index != "failed" || e3.read("MEMORY.md") != "member edit\n" {
		t.Errorf("index = %s %q", out.Index, e3.read("MEMORY.md"))
	}
}

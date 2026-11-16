package memoryx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pdir is the personal_project directory muse would use for a working copy: the slug of its
// absolute path and the FNV-1a 64 hash of it.
func pdir(wc string) string {
	return strings.Trim(strings.NewReplacer("/", "-", ".", "-").Replace(wc), "-") + "-" + museFnv(wc)
}

func (e *museImportEnv) store(parts ...string) string {
	return filepath.Join(append([]string{e.home, ".local", "share", "muse", "memory"}, parts...)...)
}

func (e *museImportEnv) previewOf(scope, dir, into string) *agentMemMusePreview {
	e.t.Helper()
	pv, err := agentMemMusePreviewFor(scope, dir, into)
	if err != nil {
		e.t.Fatal(err)
	}
	return pv
}

// museFnv reproduces the directory name muse 1.4.4 gave a probe: the hash of the absolute cwd
// (measured 2026-10-12 with a real muse run).
func TestMuseFnvMatchesMeasuredDirectory(t *testing.T) {
	if got := museFnv("/home/dev/.af-work/s3cq7ox/probe-repo"); got != "51d45085345c6000" {
		t.Fatalf("museFnv = %s", got)
	}
}

// muse writes content as given, so a note usually has no frontmatter; it must import with its
// name from the file, its description from the first non-empty line and no type.
func TestMuseImportNoFrontmatterNote(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "plain-note.md", "\n\nuse tabs here\nand a second line")
	pv := e.preview()
	it := e.item(pv, "plain-note")
	if it.Status != claudeImportNew || !it.Derived || it.Description != "use tabs here" || it.Type != "" {
		t.Fatalf("plain-note = %+v", it)
	}
	if out := e.apply(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC), "plain-note"); out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v", out)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "plain-note")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "use tabs here" || got.Body != "use tabs here\nand a second line" || got.Type != "" {
		t.Errorf("entry = %+v", got)
	}
	if pv := e.preview(); pv.Counts[claudeImportUnchanged] != 1 || len(pv.Counts) != 1 {
		t.Errorf("not idempotent: %v", pv.Counts)
	}
}

func TestMuseImportNoFrontmatterEdges(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "long.md", strings.Repeat("あ", 2500)+"\nrest")
	e.raw(e.clone, "Upper Case.md", "text")
	e.raw(e.clone, "blank.md", " \n\n")
	e.raw(e.clone, "unclosed.md", "---\nname: x\nno closing fence\n")
	pv := e.preview()
	if it := e.item(pv, "long"); it.Status != claudeImportNew || len([]rune(it.Description)) != agentMemMaxDescription ||
		!strings.HasSuffix(it.Description, "…") {
		t.Errorf("long = %.60s status=%s runes=%d", it.Description, it.Status, len([]rune(it.Description)))
	}
	if it := e.item(pv, "Upper Case"); it.Status != claudeImportInvalid || it.Reason != "bad_name" {
		t.Errorf("Upper Case = %+v", it)
	}
	if it := e.item(pv, "blank"); it.Status != claudeImportInvalid {
		t.Errorf("blank = %+v", it)
	}
	if it := e.item(pv, "unclosed"); it.Status != claudeImportInvalid || it.Reason != "no_frontmatter" {
		t.Errorf("unclosed = %+v", it)
	}
}

func TestMuseImportSkipsLockAndNonNotes(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, ".muse-memory.lock", "")
	e.raw(e.clone, "notes.txt", "x")
	e.raw(e.clone, "sub/MEMORY.md", "- idx")
	e.raw(e.clone, "sub/.muse-memory.lock", "")
	e.raw(e.clone, ".hidden/h.md", "x")
	e.raw(e.clone, "ok.md", "fine")
	pv := e.preview()
	if len(pv.Items) != 1 || pv.Items[0].Name != "ok" || pv.Withheld != 0 {
		t.Fatalf("items = %+v", pv.Items)
	}
}

func TestMuseImportNestedNotes(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "a/b.md", "nested")
	e.raw(e.clone, "a--b.md", "flat twin")
	e.raw(e.clone, "x/y/z.md", "deep")
	// A symlinked directory and a symlink in a real directory are never followed.
	outside := filepath.Join(e.home, "outside")
	memoryWrite(t, filepath.Join(outside, "secret.md"), "outside text")
	if err := os.Symlink(outside, filepath.Join(e.clone, ".agents", "memory", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(e.clone, ".agents", "memory", "x", "s.md")); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	for _, it := range pv.Items {
		switch it.Path {
		case "a/b.md", "a--b.md":
			if it.Status != claudeImportInvalid || it.Reason != "name_collision" {
				t.Errorf("%s = %+v", it.Path, it)
			}
		case "x/y/z.md":
			if it.Name != "x--y--z" || it.Status != claudeImportNew {
				t.Errorf("deep = %+v", it)
			}
		case "x/s.md":
			if it.Status != claudeImportInvalid || it.Reason != "symlink" {
				t.Errorf("symlink = %+v", it)
			}
		default:
			if it.Path == "" && it.Name == "a--b" {
				continue // the flat twin carries no Path
			}
			t.Errorf("unexpected item %+v", it)
		}
	}
	if out := e.apply(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC), "x--y--z"); out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v", out)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "x--y--z")
	if err != nil || got.Source != "muse:project/.agents/memory/x/y/z.md" {
		t.Errorf("entry = %+v, %v", got, err)
	}
}

func TestMusePersonalProjectMapping(t *testing.T) {
	e := newMuseImportEnv(t)
	d := pdir(e.clone)
	memoryWrite(t, e.store("projects", d, "af-pref.md"), "personal project note")
	memoryWrite(t, e.store("projects", d, ".muse-memory.lock"), "")
	memoryWrite(t, e.store("projects", d, "sub", "n.md"), "nested personal")

	var src agentMemMuseSource
	for _, s := range agentMemMuseList().Sources {
		if s.Scope == museScopePersonalProject {
			src = s
		}
	}
	if src.Dir != d || src.Count != 2 || src.Project == nil || src.Project.ID != e.pid {
		t.Fatalf("source = %+v", src)
	}
	pv := e.previewOf(museScopePersonalProject, d, "")
	if it := e.item(pv, "af-pref"); it.Status != claudeImportNew || !it.Derived {
		t.Fatalf("af-pref = %+v", it)
	}
	req := agentMemMuseReq{Scope: museScopePersonalProject, Project: e.pid, Dir: d, Items: []agentMemImportReqItem{
		{Name: "af-pref", SourceHash: pv.byName["af-pref"].SourceHash}}}
	if out, err := agentMemMuseApply(req, time.Now()); err != nil || out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v, %v", out, err)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "af-pref")
	if err != nil || got.Scope != agentMemScopeProject || got.Source != "muse:personal_project/"+d+"/af-pref.md" {
		t.Errorf("entry = %+v, %v", got, err)
	}
	// The worktree is the same project, so a directory for it maps to the same id.
	wd := pdir(e.wt)
	memoryWrite(t, e.store("projects", wd, "w.md"), "wt note")
	if pv := e.previewOf(museScopePersonalProject, wd, ""); pv.Project == nil || pv.Project.ID != e.pid {
		t.Errorf("worktree project = %+v (%s)", pv.Project, pv.Reason)
	}
}

func TestMusePersonalProjectUnmatchedAndAmbiguous(t *testing.T) {
	e := newMuseImportEnv(t)
	// No working copy: an unrelated path.
	memoryWrite(t, e.store("projects", "tmp-elsewhere-0123456789abcdef", "n.md"), "x")
	if pv := e.previewOf(museScopePersonalProject, "tmp-elsewhere-0123456789abcdef", ""); pv.Project != nil || pv.Reason != claudeImportNoProject {
		t.Errorf("unmatched = %+v", pv)
	}
	// A hash that matches nothing, a slug that names the clone: matched by the slug.
	slug := strings.TrimSuffix(pdir(e.clone), museFnv(e.clone)) + "ffffffffffffffff"
	memoryWrite(t, e.store("projects", slug, "n.md"), "x")
	if pv := e.previewOf(museScopePersonalProject, slug, ""); pv.Project == nil || pv.Project.ID != e.pid {
		t.Errorf("slug match = %+v", pv)
	}
	// A subdirectory of the clone is matched by prefix; once another repository's name fits as
	// well the directory is ambiguous.
	sub := strings.TrimSuffix(pdir(e.clone), museFnv(e.clone)) + "src-" + "ffffffffffffffff"
	memoryWrite(t, e.store("projects", sub, "n.md"), "x")
	if pv := e.previewOf(museScopePersonalProject, sub, ""); pv.Project == nil || pv.Project.ID != e.pid {
		t.Fatalf("prefix match = %+v", pv)
	}
	other := filepath.Join(e.home, "repos", "demo-src")
	memoryMkdirAll(t, other)
	e.git("-C", other, "init", "--quiet", "-b", "main")
	e.git("-C", other, "commit", "--quiet", "--allow-empty", "-m", "init")
	// demo-src is a longer fit, but both are candidates: two projects fit.
	if pv := e.previewOf(museScopePersonalProject, sub, ""); pv.Project != nil || pv.Reason != claudeImportAmbiguous {
		t.Errorf("ambiguous = %+v", pv)
	}
	// The exact hash wins over any slug fit.
	exact := pdir(other)
	memoryWrite(t, e.store("projects", exact, "n.md"), "x")
	if pv := e.previewOf(museScopePersonalProject, exact, ""); pv.Project == nil || pv.Project.ID == e.pid {
		t.Errorf("exact = %+v", pv)
	}
}

func TestMusePersonalNeedsExplicitUserChoice(t *testing.T) {
	e := newMuseImportEnv(t)
	memoryWrite(t, e.store("personal", "likes-go.md"), "I like Go\nvery much")
	memoryWrite(t, e.store("personal", ".muse-memory.lock"), "")

	listed := false
	for _, s := range agentMemMuseList().Sources {
		if s.Scope == museScopePersonal {
			listed = s.Count == 1 && s.Target == museIntoUser && s.Project == nil
		}
	}
	if !listed {
		t.Fatalf("personal source missing: %+v", agentMemMuseList().Sources)
	}
	// Without the choice nothing is evaluated, and an apply is refused.
	pv := e.previewOf(museScopePersonal, "", "")
	if pv.Reason != museImportChooseUser || len(pv.Items) != 0 {
		t.Fatalf("preview without into = %+v", pv)
	}
	req := agentMemMuseReq{Scope: museScopePersonal, Items: []agentMemImportReqItem{{Name: "likes-go", SourceHash: "x"}}}
	if _, err := agentMemMuseApply(req, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Fatalf("apply without into = %v", err)
	}
	if _, err := agentMemMuseApply(agentMemMuseReq{Scope: museScopeProject, Into: museIntoUser, Dir: e.dir, Items: req.Items}, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Fatalf("into=user on the project scope = %v", err)
	}

	pv = e.previewOf(museScopePersonal, "", museIntoUser)
	it := e.item(pv, "likes-go")
	if pv.Target != museIntoUser || pv.Project != nil || it.Status != claudeImportNew {
		t.Fatalf("preview = %+v", pv)
	}
	req = agentMemMuseReq{Scope: museScopePersonal, Into: museIntoUser, Items: []agentMemImportReqItem{{Name: "likes-go", SourceHash: it.SourceHash}}}
	if out, err := agentMemMuseApply(req, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)); err != nil || out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v, %v", out, err)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), agentMemScopeUser, "likes-go")
	if err != nil || got.Scope != agentMemScopeUser || got.Source != "muse:personal/likes-go.md" || got.AuthorKind != agentMemUnknown {
		t.Errorf("entry = %+v, %v", got, err)
	}
	files, _ := memoryGitRun("log", "--name-only", "--format=", memoryBranch)
	for _, f := range strings.Fields(files) {
		if !strings.HasPrefix(f, "af/user/") {
			t.Errorf("personal import wrote outside the user scope: %s", f)
		}
	}
	if pv := e.previewOf(museScopePersonal, "", museIntoUser); pv.Counts[claudeImportUnchanged] != 1 {
		t.Errorf("not idempotent: %v", pv.Counts)
	}
	// Forgotten stays forgotten.
	if _, err := agentMemForget(agentMemCallerT(t, "claude-main"), agentMemForgetReq{Scope: agentMemScopeUser, Name: "likes-go", Revision: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pv := e.previewOf(museScopePersonal, "", museIntoUser); pv.Counts[claudeImportForgotten] != 1 {
		t.Errorf("forgotten personal note came back: %v", pv.Counts)
	}
}

func TestMusePersonalSecretSkipped(t *testing.T) {
	e := newMuseImportEnv(t)
	memoryWrite(t, e.store("personal", "key.md"), "token ghp_"+strings.Repeat("a1B2c3", 6)+"\n")
	it := e.item(e.previewOf(museScopePersonal, "", museIntoUser), "key")
	if it.Status != claudeImportSecret || len(it.Findings) == 0 {
		t.Fatalf("key = %+v", it)
	}
}

// Every component from "muse" down is opened without following a symlink; only the path above it
// (a persisted home) may be one.
func TestMuseStoreSymlinksNotFollowed(t *testing.T) {
	for _, tc := range []struct{ name, link string }{
		{"muse", "muse"}, {"memory", "muse/memory"}, {"projects", "muse/memory/projects"},
		{"personal", "muse/memory/personal"}, {"project dir", "muse/memory/projects/" + "PDIR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMuseImportEnv(t)
			outside := filepath.Join(e.home, "outside")
			memoryWrite(t, filepath.Join(outside, "n.md"), "outside")
			memoryWrite(t, filepath.Join(outside, "personal", "n.md"), "outside")
			memoryWrite(t, filepath.Join(outside, "memory", "personal", "n.md"), "outside")
			memoryWrite(t, filepath.Join(outside, "memory", "projects", pdir(e.clone), "n.md"), "outside")
			memoryWrite(t, filepath.Join(outside, "projects", pdir(e.clone), "n.md"), "outside")
			link := filepath.Join(e.home, ".local", "share", strings.ReplaceAll(tc.link, "PDIR", pdir(e.clone)))
			memoryMkdirAll(t, filepath.Dir(link))
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			for _, s := range agentMemMuseList().Sources {
				if s.Scope != museScopeProject {
					t.Errorf("listed through a symlink: %+v", s)
				}
			}
			if _, err := agentMemMusePreviewFor(museScopePersonal, "", museIntoUser); agentMemCode(err) != errCodeMemoryNotFound {
				t.Errorf("personal through a symlink = %v", err)
			}
			if _, err := agentMemMusePreviewFor(museScopePersonalProject, pdir(e.clone), ""); agentMemCode(err) != errCodeMemoryNotFound {
				t.Errorf("personal project through a symlink = %v", err)
			}
		})
	}
}

// A symlink above muse (the home persisted elsewhere) is the member's own and is followed.
func TestMuseStoreHomeSymlinkAllowed(t *testing.T) {
	e := newMuseImportEnv(t)
	real := filepath.Join(e.home, "persist", "share")
	memoryWrite(t, filepath.Join(real, "muse", "memory", "personal", "n.md"), "kept")
	memoryMkdirAll(t, filepath.Join(e.home, ".local"))
	if err := os.Symlink(real, filepath.Join(e.home, ".local", "share")); err != nil {
		t.Fatal(err)
	}
	if pv := e.previewOf(museScopePersonal, "", museIntoUser); pv.Counts[claudeImportNew] != 1 {
		t.Errorf("preview = %+v", pv)
	}
}

func TestMuseStoreHonoursXDGDataHome(t *testing.T) {
	e := newMuseImportEnv(t)
	xdg := filepath.Join(e.home, "xdg-data")
	memoryWrite(t, filepath.Join(xdg, "muse", "memory", "personal", "n.md"), "from xdg")
	t.Setenv("XDG_DATA_HOME", xdg)
	if pv := e.previewOf(museScopePersonal, "", museIntoUser); pv.Counts[claudeImportNew] != 1 {
		t.Errorf("absolute XDG_DATA_HOME: %+v", pv)
	}
	// Not absolute: ignored, the default root is used (and has no personal store here).
	t.Setenv("XDG_DATA_HOME", "xdg-data")
	if _, err := agentMemMusePreviewFor(museScopePersonal, "", museIntoUser); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("relative XDG_DATA_HOME = %v", err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	memoryWrite(t, e.store("personal", "d.md"), "default root")
	if pv := e.previewOf(museScopePersonal, "", museIntoUser); pv.Counts[claudeImportNew] != 1 || e.item(pv, "d").Status != claudeImportNew {
		t.Errorf("default root: %+v", pv)
	}
}

// The same AF name from another muse scope, directory or path is not "the same import's update".
func TestMuseImportOtherOriginSameNameNotOverwritten(t *testing.T) {
	e := newMuseImportEnv(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	_ = os.Chtimes(e.raw(e.clone, "a--b.md", "from project"), t0, t0)
	e.apply(t0.Add(time.Hour), "a--b")
	later := t0.Add(5 * time.Hour)
	d := pdir(e.clone)
	p := e.store("projects", d, "a", "b.md")
	memoryWrite(t, p, "from personal project")
	_ = os.Chtimes(p, later, later)
	pv := e.previewOf(museScopePersonalProject, d, "")
	if it := e.item(pv, "a--b"); it.Status != museImportDiverged || it.Reason != "other_source" {
		t.Fatalf("a--b = %+v", it)
	}
	// Two personal_project folders (a worktree and its parent) with the same file name.
	w := pdir(e.wt)
	memoryWrite(t, e.store("projects", d, "same.md"), "parent")
	t1 := later.Add(time.Hour)
	_ = os.Chtimes(e.store("projects", d, "same.md"), t1, t1)
	req := agentMemMuseReq{Scope: museScopePersonalProject, Project: e.pid, Dir: d, Items: []agentMemImportReqItem{
		{Name: "same", SourceHash: e.previewOf(museScopePersonalProject, d, "").byName["same"].SourceHash}}}
	if _, err := agentMemMuseApply(req, t1); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, e.store("projects", w, "same.md"), "worktree")
	t2 := t1.Add(time.Hour)
	_ = os.Chtimes(e.store("projects", w, "same.md"), t2, t2)
	if it := e.item(e.previewOf(museScopePersonalProject, w, ""), "same"); it.Status != museImportDiverged || it.Reason != "other_source" {
		t.Errorf("worktree same = %+v", it)
	}
}

// Name uniqueness cannot be proven past the file limit, so such a directory is not applied.
func TestMuseImportTruncatedDirectoryRefusesApply(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "a/b.md", "nested")
	for i := 0; i < agentMemImportMaxFiles-1; i++ {
		e.raw(e.clone, fmt.Sprintf("a/c%04d.md", i), "x")
	}
	e.raw(e.clone, "a--b.md", "flat twin")
	pv := e.preview()
	if !pv.Truncate {
		t.Fatalf("not truncated: %d items", len(pv.Items))
	}
	it := e.item(pv, "a--b")
	req := agentMemMuseReq{Project: e.pid, Dir: e.dir, Items: []agentMemImportReqItem{{Name: "a--b", SourceHash: it.SourceHash}}}
	if _, err := agentMemMuseApply(req, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("apply of a truncated directory = %v", err)
	}
	if _, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "a--b"); err == nil {
		t.Error("a--b was written")
	}
}

func TestMuseImportFrontmatterLookalikes(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "dashes.md", "----\nplain note")
	e.raw(e.clone, "dash-text.md", "--- explanation\nbody")
	e.raw(e.clone, "crlf-open.md", "---\r\nname: x\r\nnever closed\r\n")
	pv := e.preview()
	for _, n := range []string{"dashes", "dash-text"} {
		if it := e.item(pv, n); it.Status != claudeImportNew || !it.Derived {
			t.Errorf("%s = %+v", n, it)
		}
	}
	if it := e.item(pv, "crlf-open"); it.Reason != "no_frontmatter" {
		t.Errorf("crlf-open = %+v", it)
	}
}

// A personal store whose only file is MEMORY.md still lists, and lands in the user scope only on
// the explicit choice.
func TestMusePersonalMemoryMdOnly(t *testing.T) {
	e := newMuseImportEnv(t)
	memoryWrite(t, e.store("personal", "MEMORY.md"), "- I prefer short answers\n")
	listed := false
	for _, s := range agentMemMuseList().Sources {
		listed = listed || s.Scope == museScopePersonal && s.Count == 1
	}
	if !listed {
		t.Fatalf("personal MEMORY.md-only store not listed: %+v", agentMemMuseList().Sources)
	}
	if pv := e.previewOf(museScopePersonal, "", ""); pv.Reason != museImportChooseUser || len(pv.Items) != 0 {
		t.Fatalf("preview without into = %+v", pv)
	}
	it := e.item(e.previewOf(museScopePersonal, "", museIntoUser), museIndexNoteName)
	if it.Status != claudeImportNew || it.Path != "MEMORY.md" {
		t.Fatalf("item = %+v", it)
	}
	req := agentMemMuseReq{Scope: museScopePersonal, Into: museIntoUser, Items: []agentMemImportReqItem{{Name: museIndexNoteName, SourceHash: it.SourceHash}}}
	if out, err := agentMemMuseApply(req, time.Now()); err != nil || out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v, %v", out, err)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), agentMemScopeUser, museIndexNoteName)
	if err != nil || got.Source != "muse:personal/MEMORY.md" {
		t.Errorf("entry = %+v, %v", got, err)
	}
}

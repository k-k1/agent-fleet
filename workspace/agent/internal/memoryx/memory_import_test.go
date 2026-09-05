package memoryx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryLiveOrEmpty returns the contents of the live memory file, or "" when there is none.
// Used where the assertion is about existence itself (memoryReadLive fails when absent).
func memoryLiveOrEmpty(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// What import is for: bring in a bundle from another environment, replace only the chosen
// projects, and be able to undo that with restore (the docs/log/39 P3 exit condition).
//
// One test plays two environments by swapping HOME / CLAUDE_CONFIG_DIR. That shape also
// confirms the assumption that the same repo name gives the same slug, because the slug is
// derived from the path (docs/log/39 item 5, slug compatibility).
func TestMemoryImportBundleRoundTrip(t *testing.T) {
	share := t.TempDir() // where files are parked to carry them across environments

	// --- env A: snapshot distinctive content and write out a bundle ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "only-a.md"), "only in A\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env A snapshot: %+v err=%v", res, err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- env B: an independent environment holding different content ---
	_, cfgB, _ := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgB, slug, "a.md"), "from env B\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env B snapshot: %+v err=%v", res, err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	if pv.Format != memoryFormatBundle || pv.Head == "" || pv.Snapshots < 1 {
		t.Fatalf("preview = %+v", pv)
	}
	if !strings.HasPrefix(pv.Ref, "refs/imports/") {
		t.Errorf("import must land on an independent lineage, got ref %q", pv.Ref)
	}
	if len(pv.Rejected) != 0 || len(pv.Unavailable) != 0 || len(pv.Secrets) != 0 {
		t.Errorf("clean bundle should have nothing to flag: %+v", pv)
	}
	var found *memoryTreeProject
	for i := range pv.Projects {
		if pv.Projects[i].Slug == slug {
			found = &pv.Projects[i]
		}
	}
	if found == nil {
		t.Fatalf("imported projects = %+v, want %s", pv.Projects, slug)
	}
	// Preparing alone must not touch live memory; applying is an explicit action.
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env B\n" {
		t.Fatalf("prepare must not touch live memory, got %q", got)
	}
	// The local history stays on main: nothing is grafted onto it.
	if before := memoryCommitCount(t); before != 1 {
		t.Fatalf("import added %d commits to the local lineage", before-1)
	}

	// --- selective apply: replace only this one claude project ---
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("import apply: %v", err)
	}
	if !res.Committed || res.PreRestore == "" {
		t.Fatalf("apply result = %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after import = %q", got)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "only-a.md")); got != "only in A\n" {
		t.Fatalf("only-a.md was not brought in: %q", got)
	}
	// Nothing outside the scope (codex) moves by a single byte.
	if got := memoryLiveOrEmpty(t, filepath.Join(os.Getenv("HOME"), ".codex", "memories", "MEMORY.md")); got != "codex index\n" {
		t.Fatalf("codex memory touched by a project-scoped import: %q", got)
	}
	// The trigger is kept in the history: the newest entry is the import.
	list, err := memoryListSnapshots(10, "")
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list[0].Trigger != memoryTriggerImport {
		t.Errorf("newest snapshot trigger = %q, want import", list[0].Trigger)
	}

	// --- an import is undoable too: going back to the pre-restore point returns env B's content ---
	back, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, res.PreRestore, "", time.Now())
	if err != nil {
		t.Fatalf("restore after import: %v", err)
	}
	if !back.Committed {
		t.Fatalf("restore after import committed nothing: %+v", back)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env B\n" {
		t.Fatalf("a.md after undo = %q", got)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "only-a.md")); got != "" {
		t.Fatalf("only-a.md should be gone after undo: %q", got)
	}
}

// An import must apply even when the destination workspace is still empty. The live root
// (<CLAUDE_CONFIG_DIR>/projects) only comes into existence once claude has started at least
// once, so in the very use case this is for — stand up a new environment and immediately
// bring the previous one's memory in — the root is not there. Writing without creating it
// first makes the apply, and only the apply, fail with ENOENT.
func TestMemoryImportAppliesWhenLiveRootMissing(t *testing.T) {
	share := t.TempDir()

	// --- env A: the exporting side ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env A snapshot: %+v err=%v", res, err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- env B: a just-started workspace, with no projects/ yet ---
	homeB := t.TempDir()
	cfgB := filepath.Join(homeB, "claude-config")
	memoryMkdirAll(t, cfgB)
	t.Setenv("HOME", homeB)
	t.Setenv("CLAUDE_CONFIG_DIR", cfgB)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(homeB, "sessions"))
	if _, err := os.Stat(filepath.Join(cfgB, "projects")); !os.IsNotExist(err) {
		t.Fatalf("precondition: projects/ must not exist yet (err=%v)", err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("import apply into an empty workspace: %v", err)
	}
	if !res.Committed || len(res.Written) == 0 {
		t.Fatalf("apply result = %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after import = %q", got)
	}
	if st, err := os.Stat(filepath.Join(cfgB, "projects")); err != nil || !st.IsDir() {
		t.Fatalf("projects/ was not created: %v", err)
	} else if st.Mode().Perm() != 0o700 {
		t.Errorf("projects/ mode = %v, want 0700", st.Mode().Perm())
	}
}

// Migration (mode=migrate): adopt the history the bundle carries, whole, as this
// environment's history. A default apply uses only the newest tree, so the other side's past
// stayed buried in refs/imports (pruned past 10). After a migration each of their snapshots
// appears in the history list, and this goes as far as checking that a point in the middle of
// it can be rolled back to. The history that was replaced survives in a stash ref.
func TestMemoryImportMigrateAdoptsLineage(t *testing.T) {
	share := t.TempDir()

	// --- env A: build two generations of history and write out a bundle ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "A1\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "A2\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	countA := memoryCommitCount(t)
	if countA < 2 {
		t.Fatalf("env A history = %d commits, want >= 2", countA)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- env B: migrate into an environment that has one snapshot of its own ---
	_, cfgB, _ := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgB, slug, "a.md"), "from env B\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	localHead, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{}, time.Now(), memoryApplyOpts{Adopt: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The lineage is swapped and the original main survives in a stash ref: history is never deleted.
	if !res.Adopted || res.Replaced != localHead || res.ReplacedRef == "" {
		t.Fatalf("migrate result = %+v (local head was %s)", res, localHead)
	}
	if got, err := memoryGitRun("rev-parse", "--verify", "--quiet", res.ReplacedRef); err != nil || got != localHead {
		t.Fatalf("replaced lineage was not stashed: %q %v", got, err)
	}
	if _, err := memoryGitRun("merge-base", "--is-ancestor", pv.Head, memoryBranch); err != nil {
		t.Fatalf("main does not descend from the imported head: %v", err)
	}
	// The pre-swap main is not part of the new lineage: the two contents are not mixed.
	if err := memoryGitRun2(t, "merge-base", "--is-ancestor", localHead, memoryBranch); err == nil {
		t.Errorf("migrate must not graft the local lineage onto main")
	}

	// Their history now lists as this environment's own (the range is fixed to everything, so
	// it spans kinds too).
	list, err := memoryListSnapshots(50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < countA {
		t.Fatalf("history after migrate = %d entries, want >= %d (the imported lineage)", len(list), countA)
	}
	if list[0].Trigger != memoryTriggerImport {
		t.Errorf("newest snapshot trigger = %q, want import", list[0].Trigger)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "A2\n" {
		t.Fatalf("live after migrate = %q, want the imported head", got)
	}

	// The point of the whole thing: roll back to a point inside their history, one generation back.
	older := list[len(list)-1].Rev // the first snapshot of the imported lineage
	if _, err := memoryRestore(memoryRestoreScope{All: true}, older, "", time.Now()); err != nil {
		t.Fatalf("restore to an imported point: %v", err)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "A1\n" {
		t.Fatalf("a.md after rolling back into the imported history = %q, want A1", got)
	}
}

// memoryGitRun2 is a thin wrapper for git calls that are expected to fail; it takes t only to
// make the caller's intent easier to read.
func memoryGitRun2(t *testing.T, args ...string) error {
	t.Helper()
	_, err := memoryGitRun(args...)
	return err
}

// tar.gz import (★3, an import is external input): traversal, entries outside the allowlist
// and anything that is not a regular file go to rejected without ever being written. Only
// permitted md files are applied.
func TestMemoryImportTarRejectsHostileEntries(t *testing.T) {
	share := t.TempDir()
	_, cfg, slug := memoryTestEnv(t)
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}

	ok := "claude/projects/" + slug + "/memory/imported.md"
	archive := filepath.Join(share, "hostile.tar.gz")
	memoryWriteTarGz(t, archive, []memoryTarEntry{
		{Name: "manifest.json", Body: `{"format":"af-memory-tar","version":1}`},
		{Name: ok, Body: "imported\n"},
		{Name: "../../../etc/passwd", Body: "root:x:0:0\n"},
		{Name: "claude/projects/" + slug + "/abcd.jsonl", Body: `{"type":"user"}`},
		{Name: "claude/projects/" + slug + "/../../.credentials.json", Body: `{"token":"SECRET"}`},
		{Name: "codex/.git/config", Body: "[core]\n"},
		{Name: "claude/projects/" + slug + "/memory/link.md", Link: "/etc/passwd"},
	})

	pv, err := memoryImportPrepare(archive, "hostile.tar.gz", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	if pv.Format != memoryFormatTar {
		t.Fatalf("format = %q", pv.Format)
	}
	for _, want := range []string{"../../../etc/passwd", "codex/.git/config"} {
		if !containsString(pv.Rejected, want) {
			t.Errorf("%q should have been rejected: %v", want, pv.Rejected)
		}
	}
	if len(pv.Rejected) != 5 {
		t.Errorf("rejected = %v, want the 5 hostile entries", pv.Rejected)
	}
	// The imported tree holds the one permitted entry and nothing else.
	files, err := memoryGitRun("ls-tree", "-r", "--name-only", pv.Head)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(files) != ok {
		t.Fatalf("imported tree = %q, want only %q", files, ok)
	}

	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Committed {
		t.Fatalf("apply committed nothing: %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfg, slug, "imported.md")); got != "imported\n" {
		t.Fatalf("imported.md = %q", got)
	}
	// No hostile entry was written anywhere.
	if _, err := os.Stat(filepath.Join(cfg, ".credentials.json")); err == nil {
		if b, _ := os.ReadFile(filepath.Join(cfg, ".credentials.json")); !strings.Contains(string(b), `{"token":"SECRET"}`) {
			t.Error("credentials file was overwritten by the import")
		}
	}
	if _, err := os.Lstat(memoryProjectMemPath(cfg, slug, "link.md")); err == nil {
		t.Error("a symlink entry was materialised into the live tree")
	}
	// Apply replaces, so existing memory the tar did not carry disappears — the consequence
	// of not doing a 3-way merge.
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfg, slug, "a.md")); got != "" {
		t.Errorf("import should replace the selected project, a.md = %q", got)
	}
}

// The format is decided by the magic bytes in the content, never by the extension. Broken
// input is a 400.
func TestMemoryImportRejectsUnknownFormat(t *testing.T) {
	share := t.TempDir()
	memoryTestEnv(t)
	junk := filepath.Join(share, "notes.bundle")
	memoryWrite(t, junk, "just some text, not a bundle\n")
	_, err := memoryImportPrepare(junk, "notes.bundle", time.Now())
	var ue *memoryUserErr
	if err == nil || !errors.As(err, &ue) || ue.Code != errCodeMemoryBadImport {
		t.Fatalf("import of junk: err=%v", err)
	}
	// apply validates its importId as well, since it reaches git as a ref name.
	if _, err := memoryImportApply("../../evil", memoryRestoreScope{All: true}, time.Now(), memoryApplyOpts{}); err == nil {
		t.Error("apply accepted a traversal-shaped importId")
	}
}

// The round trip over REST (multipart receive -> preview -> apply). The CP passes the body
// through, so once this path works the one from the Console works too.
func TestMemoryImportAPI(t *testing.T) {
	share := t.TempDir()
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatal(err)
	}

	_, cfgB, _ := memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	h := httpx.RequireToken(buildMux())
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}

	body, ctype := memoryMultipart(t, "af-memory.bundle", bundle)
	req := httptest.NewRequest("POST", "/agents/memory/import", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer smoke-token")
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var pv memoryImportPreview
	if err := json.Unmarshal(w.Body.Bytes(), &pv); err != nil {
		t.Fatalf("preview decode: %v (%s)", err, w.Body.String())
	}
	if pv.ImportID == "" || len(pv.Projects) == 0 {
		t.Fatalf("preview = %+v", pv)
	}

	apply, _ := json.Marshal(map[string]any{
		"importId": pv.ImportID,
		"scope":    map[string]any{"projects": []string{slug}},
	})
	w = smokeDo(t, h, "POST", "/agents/memory/import/apply", "smoke-token", string(apply))
	if w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after API import = %q", got)
	}
	// A malformed importId is a 400: the value becomes a ref name, so it is always validated.
	if w := smokeDo(t, h, "POST", "/agents/memory/import/apply", "smoke-token", `{"importId":"x/../y","scope":{"all":true}}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad importId: %d %s", w.Code, w.Body.String())
	}
	// A request carrying no file is a 400.
	if w := smokeDo(t, h, "POST", "/agents/memory/import", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("import without a file: %d %s", w.Code, w.Body.String())
	}
}

// memoryTarEntry is one entry of the archive being assembled; a non-empty Link means a symlink.
type memoryTarEntry struct {
	Name string
	Body string
	Link string
}

func memoryWriteTarGz(t *testing.T, path string, entries []memoryTarEntry) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o600, Size: int64(len(e.Body)), Typeflag: tar.TypeReg}
		if e.Link != "" {
			h = &tar.Header{Name: e.Name, Mode: 0o777, Linkname: e.Link, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.Link == "" {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func memoryMultipart(t *testing.T, name, path string) ([]byte, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fsResolveTree lays out a working copy the way a session actually sees one: a repository
// with a .git, a docs/ next to the root, a subfolder the session was launched in, and a
// worktree beside it whose .git is a FILE. Returns the browse root.
func fsResolveTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	t.Setenv("HOME", root)

	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("repos", "proj", ".git")
	mk("repos", "proj", "docs")
	mk("repos", "proj", "sub")
	mk("repos", "proj", "_act-parts")
	write(filepath.Join(root, "repos", "proj", "docs", "a.md"), "a")
	write(filepath.Join(root, "repos", "proj", "sub", "b.md"), "b")
	// A worktree: same shape, but .git is a file pointing at the parent's object store.
	mk("repos", "proj@wt", "docs")
	write(filepath.Join(root, "repos", "proj@wt", ".git"), "gitdir: /wherever\n")
	write(filepath.Join(root, "repos", "proj@wt", "docs", "c.md"), "c")
	// Something the browser must never answer about, however it is written.
	mk(".ssh")
	write(filepath.Join(root, ".ssh", "id_rsa"), "secret")
	return root
}

func TestFSResolveRefBases(t *testing.T) {
	root := fsResolveTree(t)
	cwd, repo := fsResolveBases("repos/proj/sub")
	if cwd != filepath.Join(root, "repos", "proj", "sub") {
		t.Fatalf("cwd = %q", cwd)
	}
	if repo != filepath.Join(root, "repos", "proj") {
		t.Fatalf("repo root = %q", repo)
	}

	cases := []struct {
		name, ref, wantPath, wantType string
	}{
		// The common case: written relative to the turn's own working directory.
		{"cwd relative", "b.md", "repos/proj/sub/b.md", "file"},
		// The fallback this endpoint exists for: written relative to the repository root
		// while the session was launched in a subfolder.
		{"repo-root fallback", "docs/a.md", "repos/proj/docs/a.md", "file"},
		{"repo-root fallback dir", "_act-parts", "repos/proj/_act-parts", "dir"},
		// Repository Markdown's own convention: a leading slash means the repo root.
		{"leading slash is repo-root relative", "/docs/a.md", "repos/proj/docs/a.md", "file"},
		// How an agent cites a file it just edited.
		{"absolute", root + "/repos/proj/docs/a.md", "repos/proj/docs/a.md", "file"},
		{"tilde", "~/repos/proj/docs/a.md", "repos/proj/docs/a.md", "file"},
	}
	for _, c := range cases {
		e, ok := fsResolveRef(c.ref, cwd, repo)
		if !ok {
			t.Errorf("%s: %q did not resolve", c.name, c.ref)
			continue
		}
		if e.Path != c.wantPath || e.Type != c.wantType {
			t.Errorf("%s: %q → (%q,%q), want (%q,%q)", c.name, c.ref, e.Path, e.Type, c.wantPath, c.wantType)
		}
	}

	// Nothing that isn't there, nothing outside the browse root, nothing denylisted —
	// each of these must stay unresolved so the Console leaves it as plain text.
	for _, ref := range []string{
		"docs/nope.md",
		"../../etc/passwd",
		"/etc/passwd",
		".ssh/id_rsa",
		"~/.ssh/id_rsa",
		root + "/.ssh/id_rsa",
		"",
		strings.Repeat("a/", 400) + "b.md",
	} {
		if e, ok := fsResolveRef(ref, cwd, repo); ok {
			t.Errorf("%q resolved to %q, want no answer", ref, e.Path)
		}
	}
}

// A worktree's .git is a file, not a directory — the root walk has to accept both, or a
// worktree session (the normal case here) gets no repository-root fallback at all.
func TestFSResolveRepoRootAcceptsWorktree(t *testing.T) {
	root := fsResolveTree(t)
	cwd, repo := fsResolveBases(root + "/repos/proj@wt/docs")
	if repo != filepath.Join(root, "repos", "proj@wt") {
		t.Fatalf("worktree repo root = %q", repo)
	}
	if e, ok := fsResolveRef("docs/c.md", cwd, repo); !ok || e.Path != "repos/proj@wt/docs/c.md" {
		t.Fatalf("worktree fallback = (%v, %v)", e, ok)
	}
}

// Without a usable cwd (a session whose transcript recorded none, a stopped workspace)
// relative references simply don't resolve — but an absolute citation still does.
func TestFSResolveWithoutCwd(t *testing.T) {
	root := fsResolveTree(t)
	cwd, repo := fsResolveBases("")
	if cwd != "" || repo != "" {
		t.Fatalf("bases = (%q,%q), want empty", cwd, repo)
	}
	if _, ok := fsResolveRef("docs/a.md", cwd, repo); ok {
		t.Error("a relative ref resolved with no cwd")
	}
	if e, ok := fsResolveRef(root+"/repos/proj/docs/a.md", cwd, repo); !ok || e.Path != "repos/proj/docs/a.md" {
		t.Fatalf("absolute ref = (%v, %v)", e, ok)
	}
}

func TestHandleFSResolve(t *testing.T) {
	fsResolveTree(t)
	post := func(body string) map[string]fsResolveEntry {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/fs/resolve", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handleFSResolve(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		var out struct {
			Resolved map[string]fsResolveEntry `json:"resolved"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Resolved
	}

	got := post(`{"cwd":"repos/proj/sub","refs":["b.md","docs/a.md","docs/nope.md","_act-parts"]}`)
	if len(got) != 3 {
		t.Fatalf("resolved = %v", got)
	}
	if got["b.md"].Path != "repos/proj/sub/b.md" || got["docs/a.md"].Path != "repos/proj/docs/a.md" {
		t.Errorf("resolved = %v", got)
	}
	if got["_act-parts"].Type != "dir" {
		t.Errorf("_act-parts type = %q", got["_act-parts"].Type)
	}
	if _, ok := got["docs/nope.md"]; ok {
		t.Error("a missing file was answered")
	}

	// Over the per-request cap the tail is dropped rather than statted: a reply that
	// cites hundreds of paths must not turn into hundreds of lookups.
	refs := make([]string, 0, fsResolveMaxRefs+1)
	for i := 0; i < fsResolveMaxRefs; i++ {
		refs = append(refs, "docs/nope.md")
	}
	refs = append(refs, "b.md")
	body, _ := json.Marshal(fsResolveRequest{Cwd: "repos/proj/sub", Refs: refs})
	if got := post(string(body)); len(got) != 0 {
		t.Errorf("over the cap: resolved = %v", got)
	}
}

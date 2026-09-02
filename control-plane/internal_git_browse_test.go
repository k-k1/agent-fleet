package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// initRepoWithTree creates a real bare + ledger row for the default tenant and
// pushes the given files (paths may include subdirs) as one commit on main.
func (e *p2Env) initRepoWithTree(t *testing.T, name string, files map[string][]byte) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	bare := filepath.Join(e.g.dataRoot, "git", "default", name+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Dir(bare), nil, "init", "--bare", "--initial-branch=main", bare)
	if err := e.st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: e.tenantID, Name: name, DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x"}
	wc := filepath.Join(t.TempDir(), "wc-"+name)
	gitRun(t, filepath.Dir(bare), env, "clone", bare, wc)
	for p, content := range files {
		fp := filepath.Join(wc, p)
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, wc, env, "add", ".")
	gitRun(t, wc, env, "commit", "-m", "seed tree")
	gitRun(t, wc, env, "push", "origin", "HEAD:main")
}

func (e *p2Env) browse(t *testing.T, name, endpoint, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/internal-git/repos/"+name+"/"+endpoint+"?"+query, nil)
	r.Header.Set("X-Forwarded-Email", "u@x")
	r.Header.Set("X-AF-Tenant", "default")
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	switch endpoint {
	case "tree":
		e.g.withMembership(e.g.tree)(w, r)
	case "blob":
		e.g.withMembership(e.g.blob)(w, r)
	case "commits":
		e.g.withMembership(e.g.commits)(w, r)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestInternalGitBrowse(t *testing.T) {
	e := newP2Env(t)
	lfsPointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111\nsize 10\n")
	e.initRepoWithTree(t, "book", map[string][]byte{
		"README.md":   []byte("# Book\nhello\n"),
		"src/main.go": []byte("package main\n"),
		"data.bin":    {0x00, 0x01, 0x02, 0x00, 0x99},
		"big.txt":     bytes.Repeat([]byte("A"), maxBlobPreview+10),
		"asset.psd":   lfsPointer,
	})

	// Root tree: trees first, then files alphabetical.
	w, body := e.browse(t, "book", "tree", "ref=main&path=")
	if w.Code != 200 {
		t.Fatalf("tree: %d %s", w.Code, w.Body.String())
	}
	entries := body["entries"].([]any)
	names := []string{}
	var firstType string
	for i, ei := range entries {
		m := ei.(map[string]any)
		names = append(names, m["name"].(string))
		if i == 0 {
			firstType = m["type"].(string)
		}
	}
	if firstType != "tree" {
		t.Fatalf("expected a tree first, got %v", names)
	}
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	for _, n := range []string{"src", "README.md", "data.bin", "big.txt", "asset.psd"} {
		if !has(n) {
			t.Fatalf("root tree missing %q: %v", n, names)
		}
	}

	// Subdir tree.
	_, sub := e.browse(t, "book", "tree", "ref=main&path=src")
	subEntries := sub["entries"].([]any)
	if len(subEntries) != 1 || subEntries[0].(map[string]any)["name"] != "main.go" {
		t.Fatalf("src tree = %v", sub["entries"])
	}

	// Text blob.
	_, rd := e.browse(t, "book", "blob", "ref=main&path=README.md")
	if rd["content"] != "# Book\nhello\n" || rd["binary"] == true {
		t.Fatalf("README blob = %v", rd)
	}
	// Binary blob → flagged, no content.
	_, bin := e.browse(t, "book", "blob", "ref=main&path=data.bin")
	if bin["binary"] != true || bin["content"] != nil {
		t.Fatalf("data.bin blob = %v", bin)
	}
	// Too-large blob → metadata only.
	_, big := e.browse(t, "book", "blob", "ref=main&path=big.txt")
	if big["too_large"] != true || big["content"] != nil {
		t.Fatalf("big.txt blob = %v", big)
	}
	// LFS pointer → flagged with oid, not raw pointer text.
	_, lfs := e.browse(t, "book", "blob", "ref=main&path=asset.psd")
	if lfs["lfs"] != true || lfs["lfs_oid"] == nil || lfs["content"] != nil {
		t.Fatalf("asset.psd blob = %v", lfs)
	}
	// Blob endpoint on a directory → 400.
	if w, _ := e.browse(t, "book", "blob", "ref=main&path=src"); w.Code != 400 {
		t.Fatalf("blob on dir: want 400 got %d", w.Code)
	}

	// Commits.
	_, cm := e.browse(t, "book", "commits", "ref=main&limit=10")
	commits := cm["commits"].([]any)
	if len(commits) < 1 || commits[0].(map[string]any)["subject"] != "seed tree" {
		t.Fatalf("commits = %v", cm["commits"])
	}
}

func TestInternalGitBrowseValidation(t *testing.T) {
	e := newP2Env(t)
	e.initRepoWithTree(t, "repo", map[string][]byte{"a.txt": []byte("x")})

	// Bad ref / path are rejected before touching git.
	if w, _ := e.browse(t, "repo", "tree", "ref=a..b&path="); w.Code != 400 {
		t.Fatalf("range ref: want 400 got %d", w.Code)
	}
	if w, _ := e.browse(t, "repo", "tree", "ref=-oops&path="); w.Code != 400 {
		t.Fatalf("dash ref: want 400 got %d", w.Code)
	}
	if w, _ := e.browse(t, "repo", "blob", "ref=main&path=../etc/passwd"); w.Code != 400 {
		t.Fatalf("traversal path: want 400 got %d", w.Code)
	}
	// Unknown repo → 404.
	if w, _ := e.browse(t, "ghost", "tree", "ref=main&path="); w.Code != 404 {
		t.Fatalf("unknown repo: want 404 got %d", w.Code)
	}
}

func TestInternalGitBrowseEmptyRepo(t *testing.T) {
	e := newP2Env(t)
	// A repo with a bare but no commits: tree returns an empty listing, not an error.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := filepath.Join(e.g.dataRoot, "git", "default", "fresh.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Dir(bare), nil, "init", "--bare", "--initial-branch=main", bare)
	if err := e.st.CreateGitRepo(context.Background(), store.GitRepo{ID: store.NewID(), TenantID: e.tenantID, Name: "fresh", DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	w, body := e.browse(t, "fresh", "tree", "ref=main&path=")
	if w.Code != 200 {
		t.Fatalf("empty tree: %d", w.Code)
	}
	if len(body["entries"].([]any)) != 0 {
		t.Fatalf("empty repo should list nothing, got %v", body["entries"])
	}
}

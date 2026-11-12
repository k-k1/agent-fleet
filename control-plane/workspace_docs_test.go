package main

import (
	"os"
	"path/filepath"
	"testing"
)

// buildDocsSrc lays out a miniature source tree mirroring the real layout.
func buildDocsSrc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		// The guide — shipped to every container, the same for everyone (ADR 0064).
		// The tree's own README is the entry point the Console opens; it ships too.
		"README.md",
		"README.ja.md",
		"user/start/without-terminal.md",
		"user/integrations/github-cli.md",
		"ref/agents.md",
		"admin/resources/overview.md",
		"operate/install.md",
		"assets/architecture.drawio",
		// The developer tree. In production these are not even baked into the CP
		// image (stage-docs.sh copies guide/ only), but a local dev CP can be pointed
		// at a wider AF_DOCS_DIR — so the staging code must refuse them on its own.
		"build/04-workspace-agent.md",
		"decisions/0011-console.md",
		"log/p3-10.md",
		"CONVENTIONS.md",
		// A shelf that no longer exists anywhere.
		"dev/04-workspace-agent.md",
	}
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func staged(t *testing.T, destRoot string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	docs := filepath.Join(destRoot, "docs")
	_ = filepath.Walk(docs, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(docs, p)
		got[rel] = true
		return nil
	})
	return got
}

// Every container receives the same tree, whatever the caller's role: role-scoped
// distribution was dropped in ADR 0064 because it was never a permission boundary and
// it made a link inside the guide resolve for some readers and not others.
//
// What must still never appear is the developer tree. That is the assertion worth
// keeping: the image is built without it, and this is the second line of defence for a
// dev CP pointed at a wider AF_DOCS_DIR.
func TestStageWorkspaceDocs_ShipsTheGuideOnly(t *testing.T) {
	src := buildDocsSrc(t)
	t.Setenv("AF_DOCS_DIR", src)

	dataDir := t.TempDir()
	if err := stageWorkspaceDocs(dataDir); err != nil {
		t.Fatalf("stage: %v", err)
	}
	got := staged(t, dataDir)

	for _, f := range []string{
		// The Console's user-guide button opens docs/README(.ja).md — the tree's own entry
		// page, which is the only one that branches by reader. Staging the shelves
		// without it leaves that button opening a file that does not exist.
		"README.md", "README.ja.md",
		"user/start/without-terminal.md", "user/integrations/github-cli.md",
		"ref/agents.md", "admin/resources/overview.md", "operate/install.md",
		"assets/architecture.drawio",
	} {
		if !got[filepath.FromSlash(f)] {
			t.Errorf("expected %s present, staged=%v", f, got)
		}
	}
	for _, f := range []string{
		"build/04-workspace-agent.md", "decisions/0011-console.md", "log/p3-10.md",
		"CONVENTIONS.md", "dev/04-workspace-agent.md",
	} {
		if got[filepath.FromSlash(f)] {
			t.Errorf("expected %s ABSENT (leak), staged=%v", f, got)
		}
	}
}

// A re-stage must fully REPLACE the previous contents, not merge into them — otherwise
// a file removed upstream would live on in every container that had already staged it.
func TestStageWorkspaceDocs_RestageReplaces(t *testing.T) {
	src := buildDocsSrc(t)
	t.Setenv("AF_DOCS_DIR", src)
	dataDir := t.TempDir()

	if err := stageWorkspaceDocs(dataDir); err != nil {
		t.Fatal(err)
	}
	// Something left over from an older image version.
	stale := filepath.Join(dataDir, "docs", "user", "99-removed.md")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stageWorkspaceDocs(dataDir); err != nil {
		t.Fatal(err)
	}
	got := staged(t, dataDir)
	if !got[filepath.FromSlash("user/start/without-terminal.md")] {
		t.Error("re-stage must keep the user shelf")
	}
	if got[filepath.FromSlash("user/99-removed.md")] {
		t.Error("re-stage must drop a file that is no longer in the source")
	}
}

// No baked source (AF_DOCS_DIR points nowhere) → no-op, no error, no docs dir.
func TestStageWorkspaceDocs_NoSource(t *testing.T) {
	t.Setenv("AF_DOCS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	dataDir := t.TempDir()
	if err := stageWorkspaceDocs(dataDir); err != nil {
		t.Fatalf("expected nil error on absent source, got %v", err)
	}
	if isDirPath(filepath.Join(dataDir, "docs")) {
		t.Error("no source → no docs dir should be created")
	}
}

// guideTopLevelExcluded names the top-level entries of the repository's guide/ that
// deliberately do not ship to containers, each with its reason. A new directory or root
// file under guide/ must land in guideShelves / guideRootFiles or here; anything else is a
// page that exists in the repository and in the baked image but is dead inside every
// workspace, which nothing else notices.
var guideTopLevelExcluded = map[string]string{
	"_redirects.tsv": "maintainer map of moved pages, served as HTTP redirects by the docs site and read by scripts/docs-check.py; no in-container reader",
}

// The shipped lists are literal on purpose (see guideShelves), which is what let the
// user/ shelf go unshipped for two releases. This derives the truth from guide/ itself.
func TestGuideShelvesCoverTheGuide(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "guide"))
	if err != nil {
		t.Fatalf("read the repository's guide/: %v", err)
	}
	shipped := map[string]bool{}
	for _, s := range guideShelves {
		shipped[s] = true
	}
	for _, f := range guideRootFiles {
		shipped[f] = true
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		seen[name] = true
		_, excluded := guideTopLevelExcluded[name]
		switch {
		case shipped[name] && excluded:
			t.Errorf("guide/%s is both shipped and excluded; drop one", name)
		case !shipped[name] && !excluded:
			kind, list := "root file", "guideRootFiles"
			if e.IsDir() {
				kind, list = "shelf", "guideShelves"
			}
			t.Errorf("guide/%s (%s) never reaches a container: add it to %s, or to guideTopLevelExcluded with the reason", name, kind, list)
		}
	}
	// A stale entry is harmless to staging (absent sources are skipped) but would let
	// this test pass against a list that no longer describes guide/.
	for name := range shipped {
		if !seen[name] {
			t.Errorf("%s is shipped but guide/ has no such entry; remove it", name)
		}
	}
	for name := range guideTopLevelExcluded {
		if !seen[name] {
			t.Errorf("guideTopLevelExcluded names %s, which guide/ no longer has; remove it", name)
		}
	}
}

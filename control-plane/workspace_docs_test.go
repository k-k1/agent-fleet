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
		"member/02-sessions.md",
		"ref/agents.md",
		"admin/02-limits.md",
		"operate/01-install.md",
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
		// The Console's 「利用ガイド」 opens docs/README(.ja).md — the tree's own entry
		// page, which is the only one that branches by reader. Staging the shelves
		// without it leaves that button opening a file that does not exist.
		"README.md", "README.ja.md",
		"member/02-sessions.md", "ref/agents.md", "admin/02-limits.md",
		"operate/01-install.md", "assets/architecture.drawio",
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
	stale := filepath.Join(dataDir, "docs", "member", "99-removed.md")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stageWorkspaceDocs(dataDir); err != nil {
		t.Fatal(err)
	}
	got := staged(t, dataDir)
	if !got[filepath.FromSlash("member/02-sessions.md")] {
		t.Error("re-stage must keep the member shelf")
	}
	if got[filepath.FromSlash("member/99-removed.md")] {
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

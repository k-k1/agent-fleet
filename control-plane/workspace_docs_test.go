package main

import (
	"os"
	"path/filepath"
	"testing"
)

// buildDocsSrc lays out a miniature docs/ tree mirroring the real layout.
func buildDocsSrc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		// Shelves, cut by reader (docs/CONVENTIONS.md).
		"use/02-sessions.md",
		"ref/agents.md",
		"admin/02-limits.md",
		"operate/01-install.md",
		"build/04-workspace-agent.md",
		// Shipped to nobody, whatever the role.
		"decisions/0011-console.md",
		"log/p3-10.md",
		// Legacy shelves, still shipped while the remaining ones are written.
		// guide/member is gone — it became use/.
		"guide/admin/02-limits.md",
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

func TestStageWorkspaceDocs_RoleScoping(t *testing.T) {
	src := buildDocsSrc(t)
	t.Setenv("AF_DOCS_DIR", src)

	cases := []struct {
		role       string
		wantHave   []string
		wantAbsent []string
	}{
		{
			role:     "member",
			wantHave: []string{"use/02-sessions.md", "ref/agents.md"},
			// A member gets the new shelves and nothing else — not even the legacy
			// dev/ docs, which used to be handed out to everyone.
			wantAbsent: []string{
				"admin/02-limits.md", "operate/01-install.md", "build/04-workspace-agent.md",
				"guide/admin/02-limits.md", "dev/04-workspace-agent.md",
				"decisions/0011-console.md", "log/p3-10.md",
			},
		},
		{
			role:     "tenant_admin",
			wantHave: []string{"use/02-sessions.md", "ref/agents.md", "admin/02-limits.md", "guide/admin/02-limits.md"},
			wantAbsent: []string{
				"operate/01-install.md", "build/04-workspace-agent.md",
				"decisions/0011-console.md", "log/p3-10.md",
			},
		},
		{
			role: "super_admin",
			wantHave: []string{
				"use/02-sessions.md", "ref/agents.md", "admin/02-limits.md",
				"operate/01-install.md", "build/04-workspace-agent.md",
			},
			// super_admin is an allowlist too — "the whole tree" is not a case any
			// more. The frozen journals and the decision records go to nobody.
			wantAbsent: []string{"decisions/0011-console.md", "log/p3-10.md"},
		},
		{
			role:     "bogus-role", // unknown → least privilege (member)
			wantHave: []string{"use/02-sessions.md", "ref/agents.md"},
			wantAbsent: []string{
				"admin/02-limits.md", "operate/01-install.md", "build/04-workspace-agent.md",
				"decisions/0011-console.md", "log/p3-10.md",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := stageWorkspaceDocs(dataDir, c.role); err != nil {
				t.Fatalf("stage: %v", err)
			}
			got := staged(t, dataDir)
			for _, f := range c.wantHave {
				if !got[filepath.FromSlash(f)] {
					t.Errorf("role %s: expected %s present, staged=%v", c.role, f, got)
				}
			}
			for _, f := range c.wantAbsent {
				if got[filepath.FromSlash(f)] {
					t.Errorf("role %s: expected %s ABSENT (leak), staged=%v", c.role, f, got)
				}
			}
		})
	}
}

// A re-stage must fully replace the previous role's subset (e.g. after a role
// downgrade), not merge into it. The member shelves remain; the shelves only an
// operator may read must be removed.
func TestStageWorkspaceDocs_RestageReplaces(t *testing.T) {
	src := buildDocsSrc(t)
	t.Setenv("AF_DOCS_DIR", src)
	dataDir := t.TempDir()

	if err := stageWorkspaceDocs(dataDir, "super_admin"); err != nil {
		t.Fatal(err)
	}
	if !staged(t, dataDir)[filepath.FromSlash("operate/01-install.md")] {
		t.Fatal("super_admin should have the operate shelf")
	}
	// Downgrade to member → the operator and admin shelves must be gone.
	if err := stageWorkspaceDocs(dataDir, "member"); err != nil {
		t.Fatal(err)
	}
	got := staged(t, dataDir)
	if !got[filepath.FromSlash("use/02-sessions.md")] {
		t.Error("re-stage as member must keep the use shelf")
	}
	if !got[filepath.FromSlash("ref/agents.md")] {
		t.Error("re-stage as member must keep the shared reference shelf")
	}
	if got[filepath.FromSlash("operate/01-install.md")] {
		t.Error("re-stage as member must drop the operate shelf")
	}
	if got[filepath.FromSlash("admin/02-limits.md")] {
		t.Error("re-stage as member must drop the admin shelf")
	}
}

// No baked source (AF_DOCS_DIR points nowhere) → no-op, no error, no docs dir.
func TestStageWorkspaceDocs_NoSource(t *testing.T) {
	t.Setenv("AF_DOCS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	dataDir := t.TempDir()
	if err := stageWorkspaceDocs(dataDir, "member"); err != nil {
		t.Fatalf("expected nil error on absent source, got %v", err)
	}
	if isDirPath(filepath.Join(dataDir, "docs")) {
		t.Error("no source → no docs dir should be created")
	}
}

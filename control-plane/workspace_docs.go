// workspace_docs.go — role-scoped provisioning of the agent-fleet docs into each
// Workspace container.
//
// Goal: the in-container agents (claude/codex/opencode) should answer questions about
// this environment (persistence, recreate vs clean-home, build limits, gh auth, …)
// from the authoritative docs rather than memory — but a tenant member must not be
// able to read the internal dev/decisions/history docs. A shared workspace image can't
// enforce that (any member could `cat` baked files), so the docs are baked into the
// CP image and the CP stages only the role-permitted subset into <dataDir>/docs at
// each start. dockerRuntime.Start then mounts that read-only into the container. Role
// separation is thus enforced at provisioning time, per container.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// bakedDocsDefault is where control-plane/Dockerfile bakes the repo's docs/ tree.
const bakedDocsDefault = "/usr/local/share/agent-fleet/docs"

// isDirPath reports whether p exists and is a directory.
func isDirPath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// docsSrcDir is the CP-side docs source. Defaults to the baked path; AF_DOCS_DIR
// overrides it for local dev where the CP runs outside its image (point it at the
// repo's docs/). An absent/empty source disables the feature (best-effort).
func docsSrcDir() string {
	if v := os.Getenv("AF_DOCS_DIR"); v != "" {
		return v
	}
	return bakedDocsDefault
}

// docsRolePrefixes maps a membership role to the docs subtrees it may see, as paths
// relative to docs/. A nil result means "the whole tree" (super_admin). Least
// privilege by default: an unknown role is treated as a plain member.
func docsRolePrefixes(role string) []string {
	switch role {
	case "super_admin":
		return nil // everything, incl. dev/decisions/history/talk
	case "tenant_admin":
		return []string{"guide", "dev"} // member + admin guides, plus public design docs
	default: // "member" and any unknown role
		return []string{filepath.Join("guide", "member"), "dev"}
	}
}

// stageWorkspaceDocs (re)builds <dataDir>/docs with only the docs the given role may
// read, copied from the CP-baked source. Rebuilt on every start so a role change
// takes effect on the next start and the copy tracks the image version. Best-effort:
// the caller ignores errors so a failure just means the container starts without a
// docs mount, never a failed start.
func stageWorkspaceDocs(dataDir, role string) error {
	src := docsSrcDir()
	if !isDirPath(src) {
		return nil // no baked docs (e.g. local dev without AF_DOCS_DIR) → skip silently
	}
	dest := filepath.Join(dataDir, "docs")
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear docs stage: %w", err)
	}
	prefixes := docsRolePrefixes(role)
	if prefixes == nil { // whole tree
		return os.CopyFS(dest, os.DirFS(src))
	}
	for _, p := range prefixes {
		s := filepath.Join(src, p)
		if !isDirPath(s) {
			continue // source subtree absent → skip (best-effort)
		}
		d := filepath.Join(dest, p)
		if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			return err
		}
		if err := os.CopyFS(d, os.DirFS(s)); err != nil {
			return err
		}
	}
	return nil
}

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
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
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
// relative to docs/. Least privilege by default: an unknown role is treated as a
// plain member.
//
// Every role is an explicit allowlist — there is no "the whole tree" case, not even
// for super_admin. That is the point: docs/ is cut by reader (docs/CONVENTIONS.md),
// and what a container receives should be the shelves that answer that reader's
// questions, nothing else. Shipping the rest is not a leak (the repository is public)
// but it is noise, and noise is expensive here: the agent in the container greps this
// tree to answer questions about its own environment, and 33k lines of frozen work
// journals used to drown the answers. So decisions/, log/ and the handoff notes go to
// nobody, and a shelf added later is invisible until someone lists it here.
//
// The listing is deliberately literal rather than clever. It is read as "who sees
// what" during security review, and a loop over a table would hide the answer.
func docsRolePrefixes(role string) []string {
	// Legacy shelves, still authoritative while the new ones are being written
	// (docs/README.md "Migration in progress"). Delete these entries — and this
	// comment — once use/ admin/ operate/ build/ are written and docs/{dev,guide}
	// are removed.
	legacyMember := []string{filepath.Join("guide", "member"), "dev"}
	legacyAll := []string{"guide", "dev"}

	switch role {
	case "super_admin":
		return append([]string{"use", "ref", "admin", "operate", "build"}, legacyAll...)
	case "tenant_admin":
		return append([]string{"use", "ref", "admin"}, legacyAll...)
	default: // "member" and any unknown role
		return append([]string{"use", "ref"}, legacyMember...)
	}
}

// roleDocsRoots resolves a role to the absolute source subtrees it may read, paired
// with their path relative to the docs root. Both provisioning paths (the bind-mount
// staging below and the tar stream in docs_bridge.go) go through this one function, so
// "what may this role see" has exactly one implementation.
func roleDocsRoots(src, role string) []struct{ Dir, Rel string } {
	var out []struct{ Dir, Rel string }
	for _, p := range docsRolePrefixes(role) {
		s := filepath.Join(src, p)
		if !isDirPath(s) {
			continue // source subtree absent → skip (best-effort)
		}
		out = append(out, struct{ Dir, Rel string }{s, p})
	}
	return out
}

// writeRoleDocsTarGz streams the role-permitted subset as a gzipped tar whose paths are
// relative to the docs root ("guide/member/README.md", …). It is the PULL half of the
// same provisioning decision stageWorkspaceDocs implements for docker/native: on ECS
// there is no host path to bind-mount into a Fargate/EC2 task, so the container asks
// for the identical subset over the internal bridge instead (docs_bridge.go).
//
// Regular files only. A symlink in the source tree could point outside it, and the
// extractor in the workspace refuses non-regular entries anyway — dropping them here
// means the archive never even describes something the other side must defend against.
func writeRoleDocsTarGz(w io.Writer, role string) (files int, err error) {
	src := docsSrcDir()
	if !isDirPath(src) {
		return 0, nil // no baked docs → a valid, empty archive
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, root := range roleDocsRoots(src, role) {
		walkErr := filepath.WalkDir(root.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg,
				Name:     filepath.ToSlash(rel),
				Mode:     0o644,
				Size:     fi.Size(),
				ModTime:  fi.ModTime(),
			}); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, cErr := io.Copy(tw, f)
			f.Close()
			if cErr != nil {
				return cErr
			}
			files++
			return nil
		})
		if walkErr != nil {
			return files, walkErr
		}
	}
	if err := tw.Close(); err != nil {
		return files, err
	}
	return files, gz.Close()
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
	for _, root := range roleDocsRoots(src, role) {
		d := filepath.Join(dest, root.Rel)
		if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			return err
		}
		if err := os.CopyFS(d, os.DirFS(root.Dir)); err != nil {
			return err
		}
	}
	return nil
}

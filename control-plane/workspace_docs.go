// workspace_docs.go — provisioning of the agent-fleet user guide into each Workspace
// container.
//
// Goal: the in-container agents (claude/codex/opencode) should answer questions about
// this environment (persistence, recreate vs clean-home, build limits, gh auth, …)
// from the authoritative documentation rather than memory — and the Console's user
// guide should have something to open.
//
// What ships is the guide/ tree and nothing else (ADR 0064). The developer tree —
// docs/build, docs/decisions, docs/log, the writing conventions — is never baked into
// the image in the first place (deploy/release/stage-docs.sh), so no container can
// receive it and no request can ask for it.
//
// ⚠️ This USED to be cut by the reader's role: a member got use/ + ref/, a tenant admin
// added admin/, a deployment admin added operate/ + build/. That was dropped because it
// was never a permission boundary — the comment justifying it said so itself ("not a
// leak (the repository is public) but it is noise") — and the noise it was actually
// removing was docs/build, which now lives outside the shipped tree entirely. Cutting
// by role also made a link inside the guide resolve for some readers and not others,
// which is how the guide accumulated links that were live in the repository and dead in
// the reader's hands. Everyone now receives the same tree, so a link either reaches
// everybody or nobody — and `check_closure` in scripts/docs-check.py can decide which.
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

// guideShelves are the subtrees of the baked source that are staged into a container.
//
// The baked tree is already exactly the guide (stage-docs.sh applies the allowlist when
// the image is built), so this list is defence in depth rather than the primary gate —
// it matters when AF_DOCS_DIR points a local dev CP at something wider. Keeping it
// literal also keeps "what does a container receive" answerable by reading one line,
// which is how it gets read during a security review.
var guideShelves = []string{"member", "admin", "operate", "ref", "assets"}

// guideRootFiles are the files at the TOP of the guide tree that ship next to the
// shelves. The tree's own README is the entry point — the page that branches by reader
// — and it is what the Console's 「利用ガイド」 opens (console/src/app/TopBar.tsx). Ship
// only the shelves and that button opens nothing at all, with the pane's
// "(target is not an existing regular file)" as the only explanation on screen.
//
// Enumerated one by one for the same reason guideShelves is: with AF_DOCS_DIR pointed
// at a wider tree by a local dev CP, "every file at the root" would sweep up whatever
// else happens to sit there.
var guideRootFiles = []string{"README.md", "README.ja.md"}

// isFilePath reports whether p exists and is a regular file.
func isFilePath(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// guideRootFilePaths resolves the root files that are actually present in src, paired
// with their path relative to the docs root (absent ones are skipped: best-effort, same
// as guideRoots).
func guideRootFilePaths(src string) []struct{ Path, Rel string } {
	var out []struct{ Path, Rel string }
	for _, f := range guideRootFiles {
		p := filepath.Join(src, f)
		if !isFilePath(p) {
			continue
		}
		out = append(out, struct{ Path, Rel string }{p, f})
	}
	return out
}

// guideRoots resolves the absolute source subtrees to ship, paired with their path
// relative to the docs root. Both provisioning paths (the bind-mount staging below and
// the tar stream in docs_bridge.go) go through this one function, so "what does a
// container receive" has exactly one implementation.
func guideRoots(src string) []struct{ Dir, Rel string } {
	var out []struct{ Dir, Rel string }
	for _, p := range guideShelves {
		s := filepath.Join(src, p)
		if !isDirPath(s) {
			continue // source subtree absent → skip (best-effort)
		}
		out = append(out, struct{ Dir, Rel string }{s, p})
	}
	return out
}

// writeGuideTarGz streams the guide as a gzipped tar whose paths are relative to the
// docs root ("member/README.md", …). It is the PULL half of the same provisioning
// decision stageWorkspaceDocs implements for docker/native: on ECS there is no host
// path to bind-mount into a Fargate/EC2 task, so the container asks for the identical
// set over the internal bridge instead (docs_bridge.go).
//
// Regular files only. A symlink in the source tree could point outside it, and the
// extractor in the workspace refuses non-regular entries anyway — dropping them here
// means the archive never even describes something the other side must defend against.
func writeGuideTarGz(w io.Writer) (files int, err error) {
	src := docsSrcDir()
	if !isDirPath(src) {
		return 0, nil // no baked docs → a valid, empty archive
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	add := func(path, rel string, fi fs.FileInfo) error {
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
	}
	for _, rf := range guideRootFilePaths(src) {
		fi, err := os.Stat(rf.Path)
		if err != nil {
			return files, err
		}
		if err := add(rf.Path, rf.Rel, fi); err != nil {
			return files, err
		}
	}
	for _, root := range guideRoots(src) {
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
			return add(path, rel, fi)
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

// stageWorkspaceDocs (re)builds <dataDir>/docs from the CP-baked source. Rebuilt on
// every start so the copy tracks the image version. Best-effort: the caller ignores
// errors so a failure just means the container starts without a docs mount, never a
// failed start.
func stageWorkspaceDocs(dataDir string) error {
	src := docsSrcDir()
	if !isDirPath(src) {
		return nil // no baked docs (e.g. local dev without AF_DOCS_DIR) → skip silently
	}
	dest := filepath.Join(dataDir, "docs")
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear docs stage: %w", err)
	}
	for _, root := range guideRoots(src) {
		d := filepath.Join(dest, root.Rel)
		if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			return err
		}
		if err := os.CopyFS(d, os.DirFS(root.Dir)); err != nil {
			return err
		}
	}
	for _, rf := range guideRootFilePaths(src) {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(rf.Path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, rf.Rel), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

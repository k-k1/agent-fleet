package bootinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
)

// cursorNeedMiB is the free space a cursor install asks of home: the ~100 MB tarball and its
// unpacked tree.
const cursorNeedMiB = 400

// cursorTool is the binary's name (~/.local/bin entry); the status name is "cursor". A constant,
// not an agent kind to branch on, to keep these checks out of the kind-literal ceiling.
const cursorTool = "cursor-agent"

const cursorStatusName = "cursor"

func cursorAsset(arch string) (string, error) {
	switch arch {
	case "amd64", "x86_64":
		return "x64", nil
	case "arm64", "aarch64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported arch: %s", arch)
}

// cursorRuns: the bundle answers --version. A tree unpacked only in part keeps its execute bit
// and still fails here. Slow (it starts Node), so it is asked only of a tree about to be trusted.
func (e Env) cursorRuns(p string) bool {
	_, _, err := e.probeArgs(p, []string{"--disable-auto-update", "--version"}, nil)
	return err == nil
}

func (e Env) cursorShare() string {
	return filepath.Join(e.Home, ".local", "share", "cursor-agent", "versions")
}

var cursorVersionInLink = regexp.MustCompile(`/versions/([^/]+)/`)

// Cursor installs the pinned cursor bundle (versions.json's cursor + cursor_sha256, a
// per-architecture value written at image build) under ~/.local/share/cursor-agent/versions/<v>/
// and links ~/.local/bin/cursor-agent to it, the upstream install.sh layout. It installs when the
// CLI is missing or its bundle is silent, and with Repin when the link points at another version.
func Cursor(ctx context.Context, e Env) int {
	ver, sum := e.Pins["cursor"], e.Pins["cursor_sha256"]
	link := filepath.Join(e.localBin(), cursorTool)
	need := false
	if ver != "" && sum != "" {
		switch {
		case !e.cliPresent(cursorTool):
			need = true
		case !e.cursorRuns(link):
			need = true // present but the bundle does not answer
		case e.Repin:
			// Decide from the link target, not --version: starting Node takes seconds.
			if t, err := os.Readlink(link); err == nil {
				if m := cursorVersionInLink.FindStringSubmatch(t); m != nil && m[1] != ver {
					need = true
				}
			}
		}
	}
	dir := filepath.Join(e.cursorShare(), ver)
	if !need {
		if e.cliPresent(cursorTool) {
			e.note(cursorStatusName, statusSkip, "")
			fmt.Fprintln(e.Stdout, "[entrypoint] boot-install: cursor already present (skip)")
		}
		return ExitOK
	}
	// Space is needed only when the pinned tree has to be downloaded: relinking to a healthy one
	// (a repin after the self-update moved ahead) writes nothing.
	if !e.cursorRuns(filepath.Join(dir, cursorTool)) && !e.room(cursorNeedMiB, []string{cursorStatusName}, cursorStatusName) {
		return ExitNoSpace
	}
	if err := e.installCursor(ctx, ver, sum, dir, link); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor boot-install failed (retrying next start)")
		e.note(cursorStatusName, statusFailed, classify(err))
		return ExitFailed
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] boot-install cursor %s\n", ver)
	e.note(cursorStatusName, statusOK, "")
	return ExitOK
}

func (e Env) installCursor(ctx context.Context, ver, sum, dir, link string) error {
	// The self-update only adds the new version in another directory, so on a repin the pinned
	// tree usually still exists: skip the re-download and repoint the link.
	if !e.cursorRuns(filepath.Join(dir, cursorTool)) {
		if err := e.fetchCursorTree(ctx, ver, sum, dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, cursorTool)
	if err := relink(link, target); err != nil {
		return err
	}
	// If the self-update's install.sh left an `agent` alias, it follows the same version.
	alias := filepath.Join(filepath.Dir(link), "agent")
	if st, err := os.Lstat(alias); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return relink(alias, target)
	}
	return nil
}

// relink points the symlink at target through a sibling name and a rename (ln -sf, but never a
// moment without a link).
func relink(link, target string) error {
	tmp := fmt.Sprintf("%s.af-new.%d", link, os.Getpid())
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (e Env) fetchCursorTree(ctx context.Context, ver, sum, dir string) error {
	asset, err := cursorAsset(e.Arch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "af-cursor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "cursor.tgz")
	if err := e.get(ctx, fmt.Sprintf("%s/%s/linux/%s/agent-cli-package.tar.gz", e.CursorURL, ver, asset), archive); err != nil {
		return err
	}
	if err := installx.VerifySha256(archive, strings.ToLower(sum)); err != nil {
		return fmt.Errorf("%w: %v", ErrChecksum, err)
	}
	// Unpack beside the final name and rename: a tar that dies part-way (ENOSPC) must not leave
	// a tree at dir whose cursor-agent is already executable.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	pid := os.Getpid()
	stage, old := fmt.Sprintf("%s.af-new.%d", dir, pid), fmt.Sprintf("%s.af-old.%d", dir, pid)
	os.RemoveAll(stage)
	os.RemoveAll(old)
	if err := untarStrip1(archive, stage); err != nil {
		os.RemoveAll(stage)
		return err
	}
	// The tree being replaced is moved aside, not deleted: the candidate must run at its final
	// path, and when it does not (or the old one only timed out under load) the working copy is
	// put back.
	if _, err := os.Lstat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			os.RemoveAll(stage)
			return err
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		os.RemoveAll(stage)
		restoreTree(old, dir)
		return err
	}
	if !e.cursorRuns(filepath.Join(dir, cursorTool)) {
		os.RemoveAll(dir)
		restoreTree(old, dir)
		return fmt.Errorf("cursor %s does not run in this environment, keeping the current copy", ver)
	}
	os.RemoveAll(old)
	return nil
}

func restoreTree(old, dir string) {
	if _, err := os.Lstat(old); err == nil {
		os.Rename(old, dir)
	}
}

// untarStrip1 unpacks a .tar.gz into dest dropping the first path component (tar
// --strip-components=1). Members that would land outside dest are refused.
func untarStrip1(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		_, rest, ok := strings.Cut(name, "/")
		if !ok || rest == "" || rest == "." {
			continue
		}
		if !filepath.IsLocal(rest) {
			return fmt.Errorf("unsafe path in archive: %s", h.Name)
		}
		out := filepath.Join(dest, rest)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, h.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// A link whose target leaves the tree is refused too.
			if filepath.IsAbs(h.Linkname) || !filepath.IsLocal(filepath.Join(path.Dir(rest), h.Linkname)) {
				return fmt.Errorf("unsafe link in archive: %s -> %s", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, out); err != nil {
				return err
			}
		}
	}
}

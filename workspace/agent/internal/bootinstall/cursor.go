package bootinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

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

// cursorVersionInLink reads the version the way the shell's greedy sed did: the last
// /versions/<v>/ of the target, whatever HOME's own path contains.
var cursorVersionInLink = regexp.MustCompile(`^.*/versions/([^/]*)/.*$`)

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
			// The agent alias counts too: a repair that left it on another version is retried.
			for _, l := range []string{link, filepath.Join(e.localBin(), "agent")} {
				if t, err := os.Readlink(l); err == nil {
					if m := cursorVersionInLink.FindStringSubmatch(t); m != nil && m[1] != "" && m[1] != ver {
						need = true
					}
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
	pinnedRuns := e.cursorRuns(filepath.Join(dir, cursorTool)) // asked once: it decides the space check and the download alike
	if !pinnedRuns && !e.room(cursorNeedMiB, []string{cursorStatusName}, cursorStatusName) {
		return ExitNoSpace
	}
	if err := e.installCursor(ctx, ver, sum, dir, link, pinnedRuns); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor boot-install failed (retrying next start)")
		e.note(cursorStatusName, statusFailed, classify(err))
		return ExitFailed
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] boot-install cursor %s\n", ver)
	e.note(cursorStatusName, statusOK, "")
	return ExitOK
}

func (e Env) installCursor(ctx context.Context, ver, sum, dir, link string, pinnedRuns bool) error {
	// The self-update only adds the new version in another directory, so on a repin the pinned
	// tree usually still exists: skip the re-download and repoint the link.
	if !pinnedRuns {
		if err := e.fetchCursorTree(ctx, ver, sum, dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, cursorTool)
	// Both links are prepared first and the alias switches before the main link, so a failure
	// part-way leaves the main link on the old version and the next repin start tries again.
	links := []string{}
	alias := filepath.Join(filepath.Dir(link), "agent")
	// If the self-update's install.sh left an `agent` alias, it follows the same version.
	if st, err := os.Lstat(alias); err == nil && st.Mode()&os.ModeSymlink != 0 {
		links = append(links, alias)
	}
	links = append(links, link)
	return relinkAll(links, target)
}

// relinkAll points every link at target (ln -sf) through sibling names and renames, in order.
// Nothing is renamed unless every replacement could be created first.
func relinkAll(links []string, target string) error {
	tmps := make([]string, len(links))
	cleanup := func() {
		for _, t := range tmps {
			if t != "" {
				os.Remove(t)
			}
		}
	}
	for i, l := range links {
		tmp := fmt.Sprintf("%s.af-new.%d", l, os.Getpid())
		os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			cleanup()
			return err
		}
		tmps[i] = tmp
	}
	type prev struct{ link, target string }
	var done []prev
	for i, l := range links {
		old, _ := os.Readlink(l)
		if err := relinkRename(tmps[i], l); err != nil {
			cleanup()
			// Put the links already switched back: a main link left behind an alias that moved
			// would read as healthy and the repair would never be retried.
			for _, d := range done {
				if d.target == "" {
					continue
				}
				back := d.link + ".af-back." + fmt.Sprint(os.Getpid())
				os.Remove(back)
				if os.Symlink(d.target, back) == nil && os.Rename(back, d.link) != nil {
					os.Remove(back)
				}
			}
			return err
		}
		tmps[i] = ""
		done = append(done, prev{l, old})
	}
	return nil
}

// relinkRename is a seam for tests: the rename that switches one link.
var relinkRename = os.Rename

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
	// A fresh private stage: a predictable name could be a leftover (or a link) that mixes old
	// files into the tree that gets published.
	stage, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".af-new.")
	if err != nil {
		return err
	}
	old := fmt.Sprintf("%s.af-old.%d", dir, os.Getpid())
	removeTree(old)
	if err := untarStrip1(archive, stage); err != nil {
		removeTree(stage)
		return err
	}
	if err := os.Chmod(stage, 0o755&^curUmask()); err != nil {
		removeTree(stage)
		return err
	}
	// The tree being replaced is moved aside, not deleted: the candidate must run at its final
	// path, and when it does not (or the old one only timed out under load) the working copy is
	// put back.
	if _, err := os.Lstat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			removeTree(stage)
			return err
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		removeTree(stage)
		if rerr := restoreTree(old, dir); rerr != nil {
			return fmt.Errorf("%w; the previous tree is left at %s: %v", err, old, rerr)
		}
		return err
	}
	if !e.cursorRuns(filepath.Join(dir, cursorTool)) {
		// The candidate is moved aside whole before the old tree goes back: removing it in place
		// can fail on a read-only subtree and would leave it at the final path.
		bad := fmt.Sprintf("%s.af-bad.%d", dir, os.Getpid())
		removeTree(bad)
		if err := os.Rename(dir, bad); err != nil {
			return fmt.Errorf("cursor %s does not run in this environment and cannot be moved aside: %v", ver, err)
		}
		if rerr := restoreTree(old, dir); rerr != nil {
			return fmt.Errorf("cursor %s does not run in this environment; the previous tree is left at %s: %v", ver, old, rerr)
		}
		removeTree(bad)
		return fmt.Errorf("cursor %s does not run in this environment, keeping the current copy", ver)
	}
	removeTree(old)
	return nil
}

// removeTree deletes a tree best-effort, making directories writable first (an archive may
// carry read-only ones).
func removeTree(p string) {
	filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(q, 0o700)
		}
		return nil
	})
	os.RemoveAll(p)
}

func curUmask() os.FileMode {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return os.FileMode(m)
}

// restoreTree puts the aside tree back; nil when there was none.
func restoreTree(old, dir string) error {
	if _, err := os.Lstat(old); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Rename(old, dir)
}

// stripFirst drops the first path component of a member name the way tar --strip-components=1
// does on the raw name (a leading "./" is a component), then requires what is left to stay
// inside the tree. "" means the member is the stripped directory itself.
func stripFirst(name string) (string, error) {
	var parts []string
	for _, p := range strings.Split(name, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) <= 1 {
		return "", nil
	}
	rest := strings.Join(parts[1:], "/")
	if !filepath.IsLocal(rest) {
		return "", fmt.Errorf("unsafe path in archive: %s", name)
	}
	return rest, nil
}

// untarStrip1 unpacks a .tar.gz into dest dropping the first path component (tar
// --strip-components=1). Every filesystem operation goes through an os.Root on dest, so a
// symlink member can never lead a later member outside the tree; links are validated as well.
// Member types other than directories, files, symlinks and hard links fail the unpack, and the
// gzip stream is read to its end so a cut-off footer is an error.
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
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	umask := curUmask()
	dirModes := map[string]os.FileMode{} // the last header for a path wins, as in tar
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, err := stripFirst(h.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		mode := h.FileInfo().Mode().Perm()
		if dir := filepath.Dir(rel); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
			dirModes[filepath.Clean(rel)] = mode &^ umask
		case tar.TypeReg:
			// A new inode renamed into place: an earlier hard link to this name keeps its content.
			tmpName := rel + ".af-w"
			root.Remove(tmpName)
			w, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
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
			if err := root.Rename(tmpName, rel); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// The target must be relative and, resolved from the link's directory, stay inside.
			if filepath.IsAbs(h.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), h.Linkname)) {
				return fmt.Errorf("unsafe link in archive: %s -> %s", h.Name, h.Linkname)
			}
			if err := root.Symlink(h.Linkname, rel); err != nil {
				return err
			}
		case tar.TypeLink:
			// A hard link names another member by its archive path, stripped the same way.
			target, err := stripFirst(h.Linkname)
			if err != nil || target == "" {
				return fmt.Errorf("unsafe hard link in archive: %s -> %s", h.Name, h.Linkname)
			}
			if err := root.Link(target, rel); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported member type %q in archive: %s", h.Typeflag, h.Name)
		}
	}
	// tar stops at its end blocks; the gzip footer (length and CRC) is checked by reading on.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return err
	}
	// Directory modes last, deepest first: a restrictive parent must not block its children.
	names := make([]string, 0, len(dirModes))
	for n := range dirModes {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.Count(names[i], "/") > strings.Count(names[j], "/")
	})
	for _, n := range names {
		if err := root.Chmod(n, dirModes[n]); err != nil {
			return err
		}
	}
	return checkLinksInside(dest)
}

// checkLinksInside requires every symlink of the unpacked tree to resolve, through any chain,
// to a place inside it. The unpack checks each link lexically; this is what the finished tree
// does when run, where no os.Root confines anything.
func checkLinksInside(dest string) error {
	base, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}
	return filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil && os.IsPermission(err) {
			return filepath.SkipDir // a directory nobody can enter holds nothing that can be followed
		}
		if err != nil || d.Type()&os.ModeSymlink == 0 {
			return err
		}
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil // dangling: it reaches nothing
		}
		if r != base && !strings.HasPrefix(r, base+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe link in archive: %s resolves outside the tree", strings.TrimPrefix(p, dest+"/"))
		}
		return nil
	})
}

package bootinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

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

// resolveReal resolves p (absolute) to a real path the way the OS would, component by component:
// ".." applies to the already-resolved directory, never to a link's name. A component that does
// not exist is kept as written (it cannot be a link), so a path through a missing old version
// directory still resolves under its real, existing ancestor. Any other failed look is an error
// and is never hidden behind the textual path.
func resolveReal(p string) (string, error) {
	rest := strings.Split(p, "/")
	res := "/"
	missing := false
	for hops := 0; len(rest) > 0; {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if missing {
				return "", errNotDirPath // the OS fails here; it never steps back onto an existing path
			}
			res = filepath.Dir(res)
			continue
		}
		next := filepath.Join(res, c)
		st, err := os.Lstat(next)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			res, missing = next, true
		case err != nil:
			return "", err
		case st.Mode()&os.ModeSymlink != 0:
			if hops++; hops > 40 {
				return "", fmt.Errorf("too many links resolving %s", p)
			}
			t, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(t) {
				res = "/"
			}
			rest = append(strings.Split(t, "/"), rest...)
		case !st.IsDir():
			return "", errNotDirPath
		default:
			res = next
		}
	}
	return res, nil
}

// errNotDirPath: the path cannot be walked as a chain of directories (a file in the middle, or
// ".." after a missing component), so the OS would refuse it and it names nothing of ours.
var errNotDirPath = errors.New("not a directory path")

// aliasVersion is the version an `agent` alias at path points at when it is cursor's own: the
// target, resolved from the alias's directory, must be exactly <share>/<version>/cursor-agent.
// Another tool's `agent` (even one whose path contains cursor-agent/versions/) is not. The
// directories on both sides are resolved as real paths, in OS order: a ~/.local/bin that is itself
// a link into another tree must not make that tree's relative alias read as this install's, and a
// versions root that is a link must not disown an alias whose old version directory is gone.
func (e Env) aliasVersion(alias, target string) (string, bool, error) {
	if !filepath.IsAbs(target) {
		target = filepath.Dir(alias) + "/" + target
	}
	i := strings.LastIndex(target, "/")
	if target[i+1:] != cursorTool {
		return "", false, nil
	}
	// Repeated separators and "." are no-ops for the OS and are dropped; ".." is kept for resolveReal.
	var comps []string
	for _, c := range strings.Split(target[:i], "/") {
		if c != "" && c != "." {
			comps = append(comps, c)
		}
	}
	share, err := resolveReal(e.cursorShare())
	if errors.Is(err, errNotDirPath) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(comps) == 0 {
		return "", false, nil
	}
	verDir, err := resolveReal("/" + strings.Join(comps, "/"))
	if err == nil {
		if filepath.Dir(verDir) != share {
			return "", false, nil
		}
		return filepath.Base(verDir), true, nil
	}
	if !errors.Is(err, errNotDirPath) {
		return "", false, err
	}
	// The version slot itself is not a directory: a plain file that replaced a version tree still
	// names that version, so the alias stays cursor's own. A link there is followed above and is
	// never read as ours.
	ver := comps[len(comps)-1]
	if ver == ".." {
		return "", false, nil
	}
	parent, err := resolveReal("/" + strings.Join(comps[:len(comps)-1], "/"))
	if errors.Is(err, errNotDirPath) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if parent != share {
		return "", false, nil
	}
	// Only "not there" means "not ours"; any other failed look is an error, never a verdict.
	st, lerr := lstatSlot(filepath.Join(parent, ver))
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return "", false, nil
		}
		return "", false, lerr
	}
	if !st.Mode().IsRegular() {
		return "", false, nil
	}
	return ver, true, nil
}

// cursorAliasVersion is aliasVersion for the alias file on disk. Only "not there" and "not a
// link" mean "not cursor's"; any other failed look is an error, so a one-off I/O error is never
// read as "nothing to do".
func (e Env) cursorAliasVersion(alias string) (string, bool, error) {
	st, err := lstatAlias(alias)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if st.Mode()&os.ModeSymlink == 0 {
		return "", false, nil
	}
	t, err := readlinkAlias(alias)
	if err != nil {
		return "", false, err
	}
	return e.aliasVersion(alias, t)
}

// cursorVersionInLink reads the version the way the shell's greedy sed did: the last
// /versions/<v>/ of the target, whatever HOME's own path contains.
var cursorVersionInLink = regexp.MustCompile(`^.*/versions/([^/]*)/.*$`)

// Cursor installs the pinned cursor bundle (versions.json's cursor + cursor_sha256, a
// per-architecture value written at image build) under ~/.local/share/cursor-agent/versions/<v>/
// and links ~/.local/bin/cursor-agent to it, the upstream install.sh layout. It installs when the
// CLI is missing or its bundle is silent, and with Repin when the link points at another version.
func Cursor(ctx context.Context, e Env) int {
	// One writer per HOME: a recovery or a pin racing a self-update would read its marker
	// mid-flight and undo a tree that is about to be accepted.
	release, ok, lockErr := e.lockCursor()
	if !ok && lockErr == nil {
		// No status change: the copy on disk is not known to be bad, only busy.
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor boot-install skipped (another cursor update is running; retrying next start)")
		return ExitFailed
	}
	defer release()
	refuseUnlocked := func() int {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: cursor boot-install skipped (cannot open %s: %v; no destructive step runs without the lock; retrying next start)\n", e.cursorLockPath(), lockErr)
		return ExitFailed
	}
	if lockErr != nil {
		// Recovery rewrites links and trees: with a journal left, it cannot run unlocked.
		switch _, err := os.Lstat(e.cursorMarkerPath()); {
		case err == nil:
			return refuseUnlocked()
		case !errors.Is(err, fs.ErrNotExist):
			// Cannot even tell whether a journal is left: as main, an unreadable journal fails.
			fmt.Fprintln(e.Stderr, err)
			return refuseUnlocked()
		}
	}
	// What a killed self-update changed goes back before the pin is judged: a half-switched link
	// pair would otherwise read as healthy.
	if lockErr != nil {
		// Nothing to recover (checked above); the decision below is read-only.
	} else if err := e.cursorRecover(); err != nil {
		fmt.Fprintln(e.Stderr, err)
		fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor boot-install skipped (an earlier update could not be rolled back; retrying next start)")
		e.note(cursorStatusName, statusFailed, ReasonOther)
		return ExitFailed
	}
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
			if t, err := os.Readlink(link); err == nil {
				if m := cursorVersionInLink.FindStringSubmatch(t); m != nil && m[1] != "" && m[1] != ver {
					need = true
				}
			}
			alias := filepath.Join(e.localBin(), "agent")
			v, ok, err := e.cursorAliasVersion(alias)
			if err != nil {
				fmt.Fprintln(e.Stderr, err)
				fmt.Fprintln(e.Stdout, "[entrypoint] WARN: cursor boot-install failed (the agent alias could not be checked; retrying next start)")
				e.note(cursorStatusName, statusFailed, ReasonOther)
				return ExitFailed
			}
			if ok && v != ver {
				need = true
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
	if lockErr != nil {
		if !pinnedRuns && !e.room(cursorNeedMiB, []string{cursorStatusName}, cursorStatusName) {
			return ExitNoSpace // answerable without writing
		}
		return refuseUnlocked() // relinking or downloading both write
	}
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
	links, err := e.cursorLinks(link)
	if err != nil {
		return err
	}
	return relinkAll(links, target)
}

// cursorLinks is what follows a cursor version: the main link and, when the install script left
// cursor's own `agent` alias, that too. The alias is listed first so it switches before the main
// link: a failure part-way leaves the main link on the old version and the next repin start
// tries again. Only cursor's own alias is touched, and a failed look at it stops before any link
// moves.
func (e Env) cursorLinks(link string) ([]string, error) {
	var links []string
	alias := filepath.Join(filepath.Dir(link), "agent")
	if _, ok, err := e.cursorAliasVersion(alias); err != nil {
		return nil, err
	} else if ok {
		links = append(links, alias)
	}
	return append(links, link), nil
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
			var stuck []string
			for _, d := range done {
				if d.target == "" {
					continue
				}
				back := d.link + ".af-back." + fmt.Sprint(os.Getpid())
				os.Remove(back)
				serr := os.Symlink(d.target, back)
				if serr == nil {
					serr = os.Rename(back, d.link)
				}
				if serr != nil {
					os.Remove(back)
					stuck = append(stuck, fmt.Sprintf("%s (was %s): %v", d.link, d.target, serr))
				}
			}
			if len(stuck) > 0 {
				return fmt.Errorf("%w; could not put back: %s", err, strings.Join(stuck, ", "))
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
			if _, lerr := os.Lstat(old); lerr == nil {
				return fmt.Errorf("cursor %s does not run in this environment and cannot be moved aside: %v; the previous tree is left at %s", ver, err, old)
			}
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
			// The temporary name is created exclusively and never replaces anything: the archive
			// may hold a member of that very name.
			var tmpName string
			var w *os.File
			for n := 0; ; n++ {
				name, err := unpackTmpName()
				if err != nil {
					return err
				}
				tmpName = filepath.Join(filepath.Dir(rel), name)
				w, err = root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
				if err == nil {
					break
				}
				if !os.IsExist(err) || n >= 16 {
					return err
				}
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
	if err := checkLinksInside(dest, root); err != nil {
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
	return nil
}

// checkLinksInside requires every symlink of the unpacked tree to resolve inside it, as the OS
// would follow it, through any chain. os.Root does the resolving (it refuses any hop that
// leaves the tree), so this is not a second, lexical opinion. A link whose final element does
// not exist yet passes only when the directory that would hold it resolved inside. It runs before the
// directory modes are applied, so every directory can still be read.
func checkLinksInside(dest string, root *os.Root) error {
	return filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		_, err = root.Stat(rel)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unsafe link in archive: %s: %v", rel, err)
		}
		// Dangling: the directory that would hold the final element must resolve inside the
		// tree, or a missing middle component could hide a path that leaves it. The path is
		// built without cleaning, so ".." is resolved by os.Root through any links on the way.
		t, err := root.Readlink(rel)
		if err != nil {
			return fmt.Errorf("unsafe link in archive: %s: %v", rel, err)
		}
		// The raw path is split, not cleaned: Clean would drop ".." before the OS resolves the
		// links in front of it. A trailing "." or ".." cannot be a missing final element.
		full := strings.TrimRight(filepath.Dir(rel)+"/"+t, "/")
		i := strings.LastIndex(full, "/")
		if last := full[i+1:]; last == "." || last == ".." {
			return fmt.Errorf("unsafe link in archive: %s -> %s", rel, t)
		}
		if st, err := root.Stat(full[:i]); err != nil || !st.IsDir() {
			return fmt.Errorf("unsafe link in archive: %s -> %s: %v", rel, t, err)
		}
		return nil
	})
}

// unpackTmpName is a seam for tests: the candidate name of a file's temporary sibling.
var unpackTmpName = func() (string, error) {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return ".af-w" + hex.EncodeToString(rnd[:]), nil
}

// lstatAlias is a seam for tests: the look at the agent alias.
var lstatAlias = os.Lstat

// lstatSlot is a seam for tests: the look at a version slot that is not a directory.
var lstatSlot = os.Lstat

// readlinkAlias is a seam for tests: reading where the alias points.
var readlinkAlias = os.Readlink

// cursorAnswers is cursorRuns that also requires a version on stdout: a bundle that exits 0
// and prints nothing is not a working one.
func (e Env) cursorAnswers(p string) bool {
	out, _, err := e.probeArgs(p, []string{"--disable-auto-update", "--version"}, nil)
	return err == nil && out != ""
}

// cursorSweepStale removes what a killed self-update left in the versions directory: a stage,
// and the aside copy of a tree that was being replaced (put back when its name is free).
func (e Env) cursorSweepStale() {
	versions := e.cursorShare()
	// The directory is opened and its entries matched by name: a glob would read brackets in a
	// HOME path as a pattern and walk a sibling.
	ents, err := os.ReadDir(versions)
	if err != nil {
		return
	}
	// Trees the launchers point at now are never swept, whatever their names look like.
	live := map[string]bool{}
	realVersions, rerr := filepath.EvalSymlinks(versions)
	for _, l := range []string{filepath.Join(e.localBin(), cursorTool), filepath.Join(e.localBin(), "agent")} {
		t, err := os.Readlink(l)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(l), t)
		}
		// By the link's own spelling, and by where the chain really ends (versions may itself be
		// a link to another volume).
		cands := []string{filepath.Clean(t)}
		if r, err := filepath.EvalSymlinks(l); err == nil {
			cands = append(cands, r)
		}
		for _, c := range cands {
			d := filepath.Dir(c)
			if pd := filepath.Dir(d); pd == filepath.Clean(versions) || (rerr == nil && pd == realVersions) {
				live[filepath.Base(d)] = true
			}
		}
	}
	for _, de := range ents {
		base := de.Name()
		p := filepath.Join(versions, base)
		if live[base] {
			continue
		}
		if strings.HasPrefix(base, ".af-up-") {
			removeTree(p)
			continue
		}
		// Only the form this code makes: <name>.af-old.<pid>.
		i := strings.LastIndex(base, ".af-old.")
		if i <= 0 || !allDigits(base[i+len(".af-old."):]) {
			continue
		}
		dir := filepath.Join(versions, base[:i])
		if _, err := os.Lstat(dir); err != nil {
			_ = restoreTree(p, dir)
			continue
		}
		removeTree(p)
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// installCursorLatest runs the upstream install script against a private HOME inside the real
// versions directory (so publishing is a rename on one filesystem) and publishes the version it
// unpacked only after that bundle answers --version. The script never touches ~/.local: run
// against the real home, a timeout after it removed the old link, or a bundle that only has the
// execute bit, left no working copy. A healthy tree already published under the same version is
// kept and only relinked. Every failure after the first change goes through one rollback.
func (e Env) installCursorLatest(ctx context.Context, script string) (err error) {
	versions := e.cursorShare()
	if err := os.MkdirAll(versions, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(e.localBin(), 0o755); err != nil {
		return err
	}
	if err := e.cursorRecover(); err != nil {
		return err
	}
	e.cursorSweepStale()
	stage, err := os.MkdirTemp(versions, ".af-up-")
	if err != nil {
		return err
	}
	defer removeTree(stage)
	if err := e.runScriptHome(ctx, stage, script); err != nil {
		return err
	}
	// The script's own layout: bin/cursor-agent -> share/cursor-agent/versions/<v>/cursor-agent.
	real, err := filepath.EvalSymlinks(stage)
	if err != nil {
		return err
	}
	cand, err := filepath.EvalSymlinks(filepath.Join(stage, ".local", "bin", cursorTool))
	if err != nil {
		return fmt.Errorf("the install script left no cursor-agent: %w", err)
	}
	verDir := filepath.Dir(cand)
	stageVersions := filepath.Join(real, ".local", "share", "cursor-agent", "versions")
	ver := filepath.Base(verDir)
	if filepath.Base(cand) != cursorTool || filepath.Dir(verDir) != stageVersions || ver == "" || ver == "." || ver == ".." || strings.Contains(ver, ".af-") {
		return fmt.Errorf("the install script put cursor-agent at an unexpected path: %s", cand)
	}
	if !e.cursorAnswers(cand) {
		return fmt.Errorf("cursor %s does not report a version in this environment, keeping the current copy", ver)
	}
	link := filepath.Join(e.localBin(), cursorTool)
	links, err := e.cursorLinks(link)
	if err != nil {
		return err
	}
	// The script's own `agent` alias follows too when the real home has none yet (a first install);
	// another tool's alias is never touched.
	alias := filepath.Join(e.localBin(), "agent")
	if _, aerr := lstatAlias(alias); os.IsNotExist(aerr) {
		if t, terr := filepath.EvalSymlinks(filepath.Join(stage, ".local", "bin", "agent")); terr == nil && t == cand {
			links = append([]string{alias}, links...)
		}
	}

	// The previous state is persisted before anything changes: a kill at any later instant is
	// repaired by cursorRecover at the next start, and so is every failure below (one rollback
	// path, not an in-memory undo list a kill would take along).
	dir := filepath.Join(versions, ver)
	target := filepath.Join(dir, cursorTool)
	pid := os.Getpid()
	m := cursorMarker{Dir: dir}
	if !e.cursorAnswers(target) {
		m.Tree = true
		m.Old = fmt.Sprintf("%s.af-old.%d", dir, pid)
		_, lerr := os.Lstat(dir)
		if lerr != nil && !errors.Is(lerr, fs.ErrNotExist) {
			return lerr // not knowing whether a tree is there must not read as "none"
		}
		had := lerr == nil
		m.HadTree = &had
		if dev, ino, ok := stagedID(verDir); ok {
			m.Dev, m.Ino = dev, ino
		}
	}
	for _, l := range links {
		st := cursorLinkState{Path: l, Kind: "none"}
		if fi, lerr := os.Lstat(l); lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				t, rerr := os.Readlink(l)
				if rerr != nil {
					return rerr
				}
				st.Kind, st.Target = "symlink", []byte(t)
			} else {
				st.Kind, st.Keep = "file", fmt.Sprintf("%s.af-prev.%d", l, pid)
				if _, kerr := os.Lstat(st.Keep); !errors.Is(kerr, fs.ErrNotExist) {
					return fmt.Errorf("%s already exists; not overwriting it", st.Keep)
				}
			}
		} else if !os.IsNotExist(lerr) {
			return lerr
		}
		m.Links = append(m.Links, st)
	}
	if m.Tree {
		removeTree(m.Old)
	}
	if err := e.cursorWriteMarker(m); err != nil {
		return err
	}
	defer func() {
		if err == nil || errors.Is(err, errCursorUnsettled) {
			return
		}
		if rerr := e.cursorRecover(); rerr != nil {
			err = fmt.Errorf("%w; %v", err, rerr)
		}
	}()

	if m.Tree {
		if *m.HadTree {
			if err := os.Rename(dir, m.Old); err != nil {
				return err
			}
		}
		if err := os.Rename(verDir, dir); err != nil {
			return err
		}
		if !e.cursorAnswers(target) {
			return fmt.Errorf("cursor %s does not report a version at its final path, keeping the current copy", ver)
		}
	}
	// A regular file in a link's place is moved aside whole; the marker names where.
	for _, st := range m.Links {
		if st.Kind == "file" {
			if err := os.Rename(st.Path, st.Keep); err != nil {
				return err
			}
		}
	}
	if err := relinkAll(links, target); err != nil {
		return err
	}
	if !e.cursorAnswers(link) {
		return fmt.Errorf("cursor %s does not report a version through ~/.local/bin", ver)
	}
	// Accepted: the marker goes first, so a kill from here on cannot roll back a verified update.
	if err := os.Remove(e.cursorMarkerPath()); err != nil && !os.IsNotExist(err) {
		// The marker stays, so the next start rolls back: the backups it needs stay too.
		fmt.Fprintf(e.Stderr, "cursor: cannot remove %s: %v (previous copies kept)\n", e.cursorMarkerPath(), err)
		return errCursorUnsettled
	}
	if m.Tree {
		removeTree(m.Old)
	}
	for _, st := range m.Links {
		if st.Keep != "" {
			os.Remove(st.Keep)
		}
	}
	return nil
}

func (e Env) cursorLockPath() string { return filepath.Join(e.Home, ".local", ".af-cursor.lock") }

// lockCursor takes the cursor lock without waiting. ok is false when another process holds it
// (err nil) or when the lock file cannot be made or opened (err set). The latter is not a free
// lock: a holder that opened the file earlier may be writing, so destructive steps refuse;
// callers that can answer without writing check err.
func (e Env) lockCursor() (release func(), ok bool, err error) {
	f, busy, err := flockPath(e.cursorLockPath())
	if busy {
		return func() {}, false, nil
	}
	if err != nil {
		return func() {}, false, err
	}
	return func() { f.Close() }, true, nil
}

// errCursorUnsettled: the update verified but its marker could not be removed, so the next start
// rolls it back from the kept backups. It is not reported as an update.
var errCursorUnsettled = errors.New("cursor update verified but not settled: the marker could not be removed, the next start will roll it back")

// cursorLinkState is what one launcher link was before a self-update touched it.
type cursorLinkState struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`   // "symlink", "file" (moved to Keep) or "none"
	Target []byte `json:"target"` // the symlink's previous target, raw bytes (a target need not be UTF-8)
	Keep   string `json:"keep"`   // where a regular file was moved aside
}

// cursorMarker records what installCursorLatest is about to change, written before the first
// change and removed when the update is verified or rolled back. Tree says the version
// directory is replaced (Old is where the previous one goes, HadTree whether there was one).
type cursorMarker struct {
	Dir     string            `json:"dir"`
	Old     string            `json:"old"`
	Tree    bool              `json:"tree"`
	HadTree *bool             `json:"hadTree"` // nil only in a marker this code did not write
	Links   []cursorLinkState `json:"links"`
	// Dev and Ino identify the staged tree this update publishes at Dir (a rename keeps them).
	// Recovery removes or moves aside only a Dir that still carries them, so a tree the user
	// installed there by hand after an interruption is never touched. Zero in a marker written
	// before this field existed (then Dir is acted on as before).
	Dev uint64 `json:"dev,omitempty"`
	Ino uint64 `json:"ino,omitempty"`
}

// stagedID is the (device, inode) of p without following a symlink; ok is false when unknown.
func stagedID(p string) (dev, ino uint64, ok bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, 0, false
	}
	st, isSys := fi.Sys().(*syscall.Stat_t)
	if !isSys {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}

// dirIsOurs: Dir at p is the tree this update staged. A marker without an identity cannot tell,
// so it answers true (the behaviour before the identity existed).
func (m cursorMarker) dirIsOurs(p string) bool {
	if m.Ino == 0 {
		return true
	}
	dev, ino, ok := stagedID(p)
	return ok && dev == m.Dev && ino == m.Ino
}

func (e Env) cursorMarkerPath() string {
	return filepath.Join(e.Home, ".local", ".af-cursor-update")
}

func (e Env) cursorWriteMarker(m cursorMarker) error {
	// What this writes must pass the reader: a marker the next start refuses would block the pin
	// and every later update. JSON also turns non-UTF-8 strings into U+FFFD.
	if !e.cursorMarkerValid(m) || !utf8.ValidString(m.Dir) {
		return fmt.Errorf("cursor: not updating, the version directory name %q cannot be journaled", filepath.Base(m.Dir))
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(e.cursorMarkerPath()), ".af-cursor-update.tmp.")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, e.cursorMarkerPath())
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return werr
}

// withSuffixDigits reports whether p is base + sep + digits.
func withSuffixDigits(p, base, sep string) bool {
	rest, ok := strings.CutPrefix(p, base+sep)
	return ok && allDigits(rest)
}

// cursorMarkerValid: the marker is acted on only when every path in it is one this code builds
// under this home, so a corrupt or planted marker cannot point a rename or a delete elsewhere.
func (e Env) cursorMarkerValid(m cursorMarker) bool {
	versions := filepath.Clean(e.cursorShare())
	// Exactly the path the writer builds, not one that merely cleans to it: the OS gets the
	// string as written.
	if filepath.Dir(m.Dir) != versions || strings.HasPrefix(filepath.Base(m.Dir), ".") || m.Dir != filepath.Join(versions, filepath.Base(m.Dir)) {
		return false
	}
	if m.Old != "" && !withSuffixDigits(m.Old, m.Dir, ".af-old.") {
		return false
	}
	if m.Tree && (m.Old == "" || m.HadTree == nil) {
		return false
	}
	// The writer always records the main link's state: a marker without it is not ours, and
	// acting on it could delete a tree no update made.
	hasMain := false
	for _, l := range m.Links {
		hasMain = hasMain || l.Path == filepath.Join(e.localBin(), cursorTool)
	}
	if !hasMain {
		return false
	}
	own := map[string]bool{
		filepath.Join(e.localBin(), cursorTool): true,
		filepath.Join(e.localBin(), "agent"):    true,
	}
	seen := map[string]bool{}
	for _, l := range m.Links {
		if !own[l.Path] || seen[l.Path] {
			return false
		}
		seen[l.Path] = true
		isAlias := l.Path == filepath.Join(e.localBin(), "agent")
		switch l.Kind {
		case "symlink":
			if len(l.Target) == 0 {
				return false
			}
			// The alias is only ever restored to a state that was cursor's own.
			if isAlias {
				// An unresolvable path is not provably ours: the marker is left for a retry.
				if _, ok, err := e.aliasVersion(l.Path, string(l.Target)); err != nil || !ok {
					return false
				}
			}
		case "file":
			if isAlias || !withSuffixDigits(l.Keep, l.Path, ".af-prev.") {
				return false
			}
		case "none":
		default:
			return false
		}
	}
	return true
}

// cursorRecover puts back what a killed or failed self-update changed, from its marker. The
// marker stays while anything could not be put back, and the error says where the previous
// tree and files are. nil means nothing is pending.
func (e Env) cursorRecover() error {
	b, err := os.ReadFile(e.cursorMarkerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cursor: cannot read %s: %v", e.cursorMarkerPath(), err)
	}
	var m cursorMarker
	if json.Unmarshal(b, &m) != nil || !e.cursorMarkerValid(m) {
		return fmt.Errorf("cursor: ignoring an unreadable %s (backups, if any, stay as *.af-old.* / *.af-prev.*)", e.cursorMarkerPath())
	}
	var errs []error
	if m.Tree {
		if _, derr := os.Lstat(m.Dir); derr == nil && !m.dirIsOurs(m.Dir) {
			// Someone installed this version by hand after the interruption: neither remove it
			// nor put the previous tree over it. A kept previous tree stays where it is.
			fmt.Fprintf(e.Stderr, "cursor: %s is not the tree this update staged; leaving it (any previous tree stays at %s)\n", m.Dir, m.Old)
		} else if _, lerr := os.Lstat(m.Old); lerr == nil {
			// The candidate is moved aside whole before the old tree goes back: removing it in
			// place can fail on a read-only subtree and would leave it at the final path.
			bad := fmt.Sprintf("%s.af-bad.%d", m.Dir, os.Getpid())
			removeTree(bad)
			moved := true
			if _, derr := os.Lstat(m.Dir); derr == nil {
				if rerr := os.Rename(m.Dir, bad); rerr != nil {
					moved = false
					errs = append(errs, fmt.Errorf("cursor: cannot move %s aside; the previous tree is left at %s: %v", m.Dir, m.Old, rerr))
				}
			}
			if moved {
				if rerr := restoreTree(m.Old, m.Dir); rerr != nil {
					errs = append(errs, fmt.Errorf("cursor: the previous tree is left at %s: %v", m.Old, rerr))
				} else {
					removeTree(bad)
				}
			}
		} else if !*m.HadTree && errors.Is(lerr, fs.ErrNotExist) {
			removeTree(m.Dir) // only the candidate this update published was ever there
			if _, derr := os.Lstat(m.Dir); !errors.Is(derr, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("cursor: cannot remove the unverified tree %s", m.Dir))
			}
		} else if !errors.Is(lerr, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("cursor: cannot look at %s: %v", m.Old, lerr))
		}
	}
	for _, l := range m.Links {
		switch l.Kind {
		case "symlink":
			if cur, rerr := os.Readlink(l.Path); rerr != nil || cur != string(l.Target) {
				if err := relinkAll([]string{l.Path}, string(l.Target)); err != nil {
					errs = append(errs, fmt.Errorf("cursor: cannot point %s back at %s: %v", l.Path, l.Target, err))
				}
			}
		case "file":
			if _, lerr := os.Lstat(l.Keep); lerr == nil {
				os.Remove(l.Path)
				if err := os.Rename(l.Keep, l.Path); err != nil {
					errs = append(errs, fmt.Errorf("cursor: the previous %s is left at %s: %v", l.Path, l.Keep, err))
				}
			} else if !errors.Is(lerr, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("cursor: cannot look at %s: %v", l.Keep, lerr))
			}
		case "none":
			// Only a link this update published: one at a path that was empty before and now
			// points at the tree it made. Anything else is someone else's.
			cur, rerr := os.Readlink(l.Path)
			switch {
			case rerr == nil && cur == filepath.Join(m.Dir, cursorTool):
				if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("cursor: cannot remove %s: %v", l.Path, err))
				}
			case rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && !errors.Is(rerr, syscall.EINVAL):
				errs = append(errs, fmt.Errorf("cursor: cannot look at %s: %v", l.Path, rerr))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if err := os.Remove(e.cursorMarkerPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

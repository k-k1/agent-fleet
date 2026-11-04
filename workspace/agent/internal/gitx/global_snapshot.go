package gitx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// globalSnapshot is a local copy of the user's global git config (the XDG file and
// ~/.gitconfig), handed to the list poll's `git status` as GIT_CONFIG_GLOBAL.
//
// Why: in a production Workspace ~/.gitconfig lives on EFS, and every git process opens it
// over NFS. One GET /repos runs `git status` in every working copy plus a child per
// submodule (measured: 176 git processes per poll on a box with 101 copies, ~1,900 opens of
// ~/.gitconfig a minute). The snapshot turns that into the one read globalConfigPaths
// already does per request.
//
// Each source is copied to its own file and GIT_CONFIG_GLOBAL names a wrapper that includes
// them by absolute path, in git's own order (XDG, then ~/.gitconfig). Concatenating them is
// not equivalent: a BOM at the start of ~/.gitconfig becomes a fatal mid-file byte, and a
// trailing backslash in the XDG file continues into the next file's first line.
//
// A generation is named by its content hash and never rewritten, so a git still reading an
// older one, or a slower request writing an older one, cannot change what a newer request
// hands out.
//
// Only read-only callers may use it: a `git config --global` write through it would land in
// the snapshot and be lost. A config with an include is never snapshotted, because a
// relative include.path resolves against the including file's directory.
type globalSnapshot struct {
	mu   sync.Mutex
	dir  string // private 0700 directory under the temp dir, made on first use
	cur  string // directory of the generation handed out last
	prev string // the one before it, kept for gits that may still be reading it
}

var listGlobalSnapshot = &globalSnapshot{}

// snapshotAllowed reports whether xdg and home can be read through a snapshot unchanged.
func snapshotAllowed(xdg, home []byte) bool {
	for _, b := range [][]byte{xdg, home} {
		if strings.Contains(strings.ToLower(string(b)), "[include") {
			return false
		}
	}
	return true
}

// privateDir reports whether dir is still the directory we made: a real directory (not a
// symlink), owned by us, mode 0700. A temp cleaner can remove it, and another user can then
// create the same name and plant a config whose core.fsmonitor runs as the Agent.
func privateDir(dir string) bool {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o700 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// env returns the environment that points git at a snapshot of xdg and home, or nil when
// one cannot be made (git then reads the real files, as before).
func (s *globalSnapshot) env(xdg, home []byte) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" && !privateDir(s.dir) {
		s.dir, s.cur, s.prev = "", "", "" // never reuse a path we no longer own
	}
	if s.dir == "" {
		d, err := os.MkdirTemp("", "af-git-global-")
		if err != nil {
			return nil
		}
		s.dir = d
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%d\x00", len(xdg))
	sum.Write(xdg)
	sum.Write(home)
	gen := filepath.Join(s.dir, hex.EncodeToString(sum.Sum(nil))[:32])
	if strings.ContainsAny(gen, "\"\\\n") {
		return nil // not expressible as a quoted include.path
	}
	if _, err := os.Lstat(filepath.Join(gen, "config")); err != nil {
		if err := writeGeneration(s.dir, gen, xdg, home); err != nil {
			return nil
		}
	}
	if gen != s.cur {
		if s.prev != "" && s.prev != gen {
			_ = os.RemoveAll(s.prev)
		}
		s.prev, s.cur = s.cur, gen
	}
	return []string{"GIT_CONFIG_GLOBAL=" + filepath.Join(gen, "config")}
}

// writeGeneration builds a generation in a fresh temp directory inside root and renames it
// into place, so a reader never sees a half-written one.
func writeGeneration(root, gen string, xdg, home []byte) error {
	tmp, err := os.MkdirTemp(root, "tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	wrapper := ""
	for _, f := range []struct {
		name string
		b    []byte
	}{{"xdg", xdg}, {"home", home}} {
		if err := os.WriteFile(filepath.Join(tmp, f.name), f.b, 0o600); err != nil {
			return err
		}
		wrapper += "[include]\n\tpath = \"" + filepath.Join(gen, f.name) + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(tmp, "config"), []byte(wrapper), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, gen); err != nil {
		if _, serr := os.Lstat(filepath.Join(gen, "config")); serr == nil {
			return nil // the same content is already in place
		}
		return err
	}
	return nil
}

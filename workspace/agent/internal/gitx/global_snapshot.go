package gitx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// globalSnapshot is a local copy of the user's global git config (the XDG file, then
// ~/.gitconfig), handed to the list poll's `git status` as GIT_CONFIG_GLOBAL.
//
// Why: in a production Workspace ~/.gitconfig lives on EFS, and every git process opens it
// over NFS. One GET /repos runs `git status` in every working copy plus a child per
// submodule (measured: 176 git processes per poll on a box with 101 copies, ~1,900 opens of
// ~/.gitconfig a minute). The snapshot turns that into the one read globalConfigPaths
// already does per request.
//
// Only read-only callers may use it: a `git config --global` write through it would land in
// the snapshot and be lost. A config with an include is never snapshotted, because a
// relative include.path resolves against the including file's directory.
type globalSnapshot struct {
	mu   sync.Mutex
	dir  string // private 0700 directory, made on first use
	last []byte // what the file currently holds
}

var listGlobalSnapshot = &globalSnapshot{}

// snapshotContent is what GIT_CONFIG_GLOBAL must hold to read like the two default files.
// git reads the XDG file before ~/.gitconfig, and a later value wins, so concatenating them
// in that order keeps the result. ok=false means the config cannot be snapshotted faithfully.
func snapshotContent(xdg, home []byte) ([]byte, bool) {
	for _, b := range [][]byte{xdg, home} {
		if strings.Contains(strings.ToLower(string(b)), "[include") {
			return nil, false
		}
	}
	var buf bytes.Buffer
	buf.Write(xdg)
	if len(xdg) > 0 && xdg[len(xdg)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.Write(home)
	return buf.Bytes(), true
}

// env returns the environment that points git at a snapshot holding content, or nil when
// one cannot be written (git then reads the real files, as before).
func (s *globalSnapshot) env(content []byte) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		d, err := os.MkdirTemp("", "af-git-global-")
		if err != nil {
			return nil
		}
		s.dir = d
	}
	path := filepath.Join(s.dir, "config")
	if _, err := os.Stat(path); err != nil || !bytes.Equal(s.last, content) {
		// Write and rename so a git already reading the old snapshot never sees a partial file.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, content, 0o600); err != nil {
			s.dir = "" // the directory may have been cleaned away; make a new one next time
			return nil
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil
		}
		s.last = append([]byte(nil), content...)
	}
	return []string{"GIT_CONFIG_GLOBAL=" + path}
}

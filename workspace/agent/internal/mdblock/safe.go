package mdblock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrDamaged means a block's markers cannot be paired unambiguously (a missing end marker, a
// stray end, a nested start). Strip would delete from the start marker to the end of the file or
// to the wrong end marker, taking the member's text with it, so SetSafe refuses instead.
var ErrDamaged = errors.New("mdblock: damaged markers")

// checkPaired reports whether every start marker of the named block has its own end marker
// before the next start, with no end marker outside a pair.
func checkPaired(s, name string) error {
	start, end := Markers(name)
	for pos := 0; ; {
		i := strings.Index(s[pos:], start)
		if i < 0 {
			if strings.Contains(s[pos:], end) {
				return ErrDamaged
			}
			return nil
		}
		i += pos
		if strings.Contains(s[pos:i], end) {
			return ErrDamaged
		}
		k := strings.Index(s[i+len(start):], end)
		if k < 0 || strings.Contains(s[i+len(start):i+len(start)+k], start) {
			return ErrDamaged
		}
		pos = i + len(start) + k + len(end)
	}
}

// SetSafe is Set for a block AF adds to a file the member also edits: it removes EVERY
// well-formed copy of the block (Set removes only the first), appends one when body is
// non-empty, and returns ErrDamaged, leaving s alone, when the markers are not cleanly paired.
func SetSafe(s, name, body string) (string, error) {
	if err := checkPaired(s, name); err != nil {
		return s, err
	}
	for Has(s, name) {
		s = Strip(s, name)
	}
	return Set(s, name, body), nil
}

// EditFile is the read-modify-write for a markdown file shared with the member. A symlink is
// followed: the target is rewritten (keeping its mode) and the link stays, so a link into
// persistent storage is never replaced by a copy. A dangling link is an error. When the edit
// leaves nothing, a regular file is removed if removeEmpty, and a link's target is emptied
// rather than the link deleted. A missing parent directory is created with dirPerm (a private
// config home must not come out world-searchable). An edit that changes nothing writes nothing.
func EditFile(path string, perm, dirPerm os.FileMode, removeEmpty bool, edit func(string) (string, error)) error {
	real := path
	isLink := false
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		r, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		real, isLink = r, true
	}
	orig := ""
	if b, err := os.ReadFile(real); err == nil {
		orig = string(b)
		if fi, err := os.Stat(real); err == nil {
			perm = fi.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	out, err := edit(orig)
	if err != nil {
		return err
	}
	if out == orig {
		return nil
	}
	if out == "" && removeEmpty && !isLink {
		if err := os.Remove(real); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(real), dirPerm); err != nil {
		return err
	}
	tmp := real + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(out), perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, real)
}

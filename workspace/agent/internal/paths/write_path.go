package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveFileWritePath follows symlinks, including ones whose target or target
// directory has yet to be created, so an atomic write does not replace a link.
func ResolveFileWritePath(path string) (string, error) {
	return resolveFileWritePath(path, 0)
}

func resolveFileWritePath(path string, links int) (string, error) {
	if links >= 40 {
		return "", errors.New("too many symlinks in file write path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return resolved, err
	}
	sep := string(os.PathSeparator)
	dir, leaf := filepath.Split(strings.TrimRight(path, sep))
	if leaf == ".." {
		// A missing directory followed by '..' cannot be skipped by the kernel.
		return "", err
	}
	parent := strings.TrimRight(dir, sep)
	if parent == "" || parent == filepath.VolumeName(dir) {
		parent = dir
	} else {
		parent, err = resolveFileWritePath(parent, links)
		if err != nil {
			return "", err
		}
	}
	resolved = filepath.Join(parent, leaf)
	info, err := os.Lstat(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return resolved, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return resolved, nil
	}
	target, err := os.Readlink(resolved)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		// Keep any '..' until the target's own symlinks have been resolved.
		target = filepath.Dir(resolved) + sep + target
	}
	return resolveFileWritePath(target, links+1)
}

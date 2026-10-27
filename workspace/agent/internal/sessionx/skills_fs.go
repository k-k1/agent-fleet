package sessionx

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skillFS is how one catalog root is read. For a fenced root (the repository view) every
// listing, stat and read goes through an os.Root, so a link anywhere below cannot lead out of the
// fence and nothing is resolved by path a second time. An unfenced root (the personal view) reads
// the real filesystem.
type skillFS struct {
	fsys   fs.FS
	base   string // the fence directory; "" when unfenced
	closer func()
}

// Test hooks: swap something for a link between a check and the next open.
var (
	beforeFenceOpen   func()
	beforeFolderFiles func()
)

// openFS opens the root for reading. A pre-opened fenceRoot (the working copy, held open by the
// request) is used as is; any other fence is opened here and closed by close.
func (r catalogRoot) openFS() (*skillFS, error) {
	if r.fence == "" {
		return &skillFS{fsys: os.DirFS("/"), closer: func() {}}, nil
	}
	if r.fenceRoot != nil {
		return &skillFS{fsys: r.fenceRoot.FS(), base: r.fence, closer: func() {}}, nil
	}
	root, err := os.OpenRoot(r.fence)
	if err != nil {
		return nil, err
	}
	return &skillFS{fsys: root.FS(), base: r.fence, closer: func() { _ = root.Close() }}, nil
}

func (s *skillFS) close() { s.closer() }

func (s *skillFS) fenced() bool { return s.base != "" }

// name maps an absolute path to the fs.FS name; "" when the path is not below the fence.
func (s *skillFS) name(p string) string {
	if !s.fenced() {
		p = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
		if p == "" {
			return "."
		}
		return p
	}
	rel, err := filepath.Rel(s.base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (s *skillFS) open(p string) (fs.File, error) {
	n := s.name(p)
	if n == "" {
		return nil, fs.ErrInvalid
	}
	if s.fenced() && beforeFenceOpen != nil {
		beforeFenceOpen()
	}
	return s.fsys.Open(n)
}

// read returns up to 1 MiB of a file.
func (s *skillFS) read(p string) ([]byte, error) {
	f, err := s.open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

func (s *skillFS) readDir(p string) ([]fs.DirEntry, error) {
	n := s.name(p)
	if n == "" {
		return nil, fs.ErrInvalid
	}
	return fs.ReadDir(s.fsys, n)
}

func (s *skillFS) isDir(p string) bool {
	n := s.name(p)
	if n == "" {
		return false
	}
	fi, err := fs.Stat(s.fsys, n)
	return err == nil && fi.IsDir()
}

// present reports whether something (a link included) sits at p.
func (s *skillFS) present(p string) bool {
	n := s.name(p)
	if n == "" {
		return false
	}
	_, err := fs.Lstat(s.fsys, n)
	return err == nil
}

// folderFiles lists a skill folder's regular files other than SKILL.md, relative to it, hidden
// entries skipped (a skill checked out with its .git would otherwise list hundreds).
func (s *skillFS) folderFiles(dir string) []string {
	base := s.name(dir)
	if base == "" {
		return nil
	}
	if s.fenced() && beforeFolderFiles != nil {
		beforeFolderFiles()
	}
	out := []string{}
	_ = fs.WalkDir(s.fsys, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != base && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel := p
		if base != "." {
			rel = strings.TrimPrefix(p, base+"/")
		}
		if rel == "SKILL.md" {
			return nil
		}
		out = append(out, rel)
		if len(out) >= maxCatalogFiles {
			return fs.SkipAll
		}
		return nil
	})
	return out
}

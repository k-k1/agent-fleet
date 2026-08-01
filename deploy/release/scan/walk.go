package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxDepth bounds archive nesting. The deepest real path today is
// images.tar.gz -> docker save tar -> layer blob (gzip) -> layer tar -> file = 4.
const maxDepth = 8

// Walker expands every shipped artifact down to leaf bytes and folds each leaf
// through a Matcher.
//
// Dispatch is by content, not by file name: `docker save` writes an OCI layout
// whose layers are `blobs/sha256/<digest>` with no extension at all, so a
// suffix-driven walker would scan the compressed bytes and find nothing.
//
// Known limit: only a whole stream is recognised as compressed. A compressed
// blob sitting inside a larger file — a go:embed'ed .gz, an asset appended to a
// binary — is folded as the opaque bytes it is, so text inside it is invisible
// to the gate. Everything the Console and the Go binaries actually put in front
// of a user is uncompressed on disk, so this has no bearing on our own output.
type Walker struct {
	entries []Entry
	allow   *AllowList
	verbose bool

	Hits     []Hit
	Files    int64
	Bytes    int64
	Skipped  []string // archive members that could not be expanded
	seenHits map[string]bool
}

// Hit is one confirmed forbidden token in one artifact path.
type Hit struct {
	Path string // nested path, e.g. images.tar.gz!blobs/sha256/ab12!app/console/assets/index.js
	Off  int64  // byte offset of the match within the leaf file
	ID   string
}

func NewWalker(entries []Entry, allow *AllowList, verbose bool) *Walker {
	return &Walker{entries: entries, allow: allow, verbose: verbose, seenHits: map[string]bool{}}
}

// WalkPath scans a file or, recursively, every file under a directory.
func (w *Walker) WalkPath(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return w.stream(filepath.Base(p), f, 0)
	}
	return filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(p, path)
		if rerr != nil {
			rel = path
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return w.stream(rel, f, 0)
	})
}

// stream sniffs r and either expands it as an archive or folds it as a leaf.
func (w *Walker) stream(name string, r io.Reader, depth int) error {
	if depth > maxDepth {
		w.Skipped = append(w.Skipped, name+" (nesting deeper than "+fmt.Sprint(maxDepth)+")")
		return nil
	}
	br := bufio.NewReaderSize(r, 1<<16)
	head, _ := br.Peek(512)
	if len(head) == 0 {
		return nil
	}

	switch sniff(head) {
	case fmtGzip:
		zr, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("%s: gzip: %w", name, err)
		}
		defer zr.Close()
		return w.stream(name+"!", zr, depth+1)
	case fmtZstd:
		return w.viaExec(name, br, depth, "zstd", "-dc")
	case fmtXz:
		return w.viaExec(name, br, depth, "xz", "-dc")
	case fmtBzip2:
		return w.stream(name+"!", bzip2.NewReader(br), depth+1)
	case fmtTar:
		return w.walkTar(name, br, depth)
	case fmtZip:
		return w.walkZip(name, br, depth)
	}
	return w.leaf(name, br)
}

// viaExec pipes through an external decompressor. A missing tool is an error,
// never a silent skip: an unexpanded archive would read as "clean".
func (w *Walker) viaExec(name string, r io.Reader, depth int, tool string, args ...string) error {
	if _, err := exec.LookPath(tool); err != nil {
		return fmt.Errorf("%s: need %q on PATH to expand this artifact (refusing to skip it)", name, tool)
	}
	cmd := exec.Command(tool, args...)
	cmd.Stdin = r
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	serr := w.stream(name+"!", out, depth+1)
	io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil && serr == nil {
		return fmt.Errorf("%s: %s: %w", name, tool, err)
	}
	return serr
}

func (w *Walker) walkTar(name string, r io.Reader, depth int) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: tar: %w", name, err)
		}
		if h.Typeflag != tar.TypeReg {
			// Symlink/hardlink targets and xattr values are names, not payload;
			// fold them too so a forbidden token cannot hide in a link target.
			if h.Linkname != "" {
				w.foldString(joinPath(name, h.Name)+" (linkname)", h.Linkname)
			}
			continue
		}
		if err := w.stream(joinPath(name, h.Name), tr, depth+1); err != nil {
			return err
		}
	}
}

// walkZip needs random access, so the member is spilled to a temp file first.
func (w *Walker) walkZip(name string, r io.Reader, depth int) error {
	tmp, err := os.CreateTemp("", "af-scan-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, r)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return fmt.Errorf("%s: zip: %w", name, err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s: zip: %w", name, err)
		}
		serr := w.stream(joinPath(name, f.Name), rc, depth+1)
		rc.Close()
		if serr != nil {
			return serr
		}
	}
	return nil
}

// leaf folds one file's bytes. File names are folded as well — a forbidden
// token in a path (an `accrete.css` that survived a rename) must not slip by.
func (w *Walker) leaf(name string, r io.Reader) error {
	w.foldString(name+" (path)", name)
	m := w.matcher(name)
	n, err := io.Copy(m, r)
	w.Files++
	w.Bytes += n
	if w.verbose {
		fmt.Fprintf(os.Stderr, "  scanned %10d  %s\n", n, name)
	}
	return err
}

func (w *Walker) foldString(label, s string) {
	m := w.matcher(label)
	m.Write([]byte(s))
}

func (w *Walker) matcher(name string) *Matcher {
	return NewMatcher(w.entries, func(off int64, e Entry) {
		if w.allow.Allows(e.ID, name) {
			return
		}
		key := name + "\x00" + e.ID
		if w.seenHits[key] {
			return // one line per (file, term); offsets beyond the first add nothing
		}
		w.seenHits[key] = true
		w.Hits = append(w.Hits, Hit{Path: name, Off: off, ID: e.ID})
	})
}

func joinPath(archive, member string) string {
	archive = strings.TrimSuffix(archive, "!")
	return archive + "!" + member
}

type fileFormat int

const (
	fmtRaw fileFormat = iota
	fmtGzip
	fmtZstd
	fmtXz
	fmtBzip2
	fmtTar
	fmtZip
)

func sniff(head []byte) fileFormat {
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return fmtGzip
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return fmtZstd
	case len(head) >= 6 && bytes.Equal(head[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return fmtXz
	case len(head) >= 3 && head[0] == 'B' && head[1] == 'Z' && head[2] == 'h':
		return fmtBzip2
	case len(head) >= 4 && head[0] == 'P' && head[1] == 'K' && (head[2] == 3 || head[2] == 5 || head[2] == 7):
		return fmtZip
	case len(head) >= 265 && bytes.Equal(head[257:262], []byte("ustar")):
		return fmtTar
	}
	return fmtRaw
}

package main

// A folder as one zip (GET /fs/download-zip, ADR 0111).
//
// The archive is built into an unlinked temp file under the agent's own state dir and then
// served with http.ServeContent. Every failure — a limit, a hostile name, a file that changed
// under the walk — is therefore a plain JSON error BEFORE the first body byte, and a success
// has a Content-Length. Streaming would have had to choose between a truncated zip that looks
// finished and a connection cut after a 200; the cost here is temp space, bounded below.
//
// Everything is opened relative to a trusted root fd with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS,
// the start folder and each child alike, so a path swapped for a symlink after the walk fails
// the open instead of reading through it. A symlink anywhere in the START path is refused; one
// INSIDE the folder is left out (counted, and reported by the check). Unlike the single-file
// download this does not inherit the .codex/generated_images exception: that one is a reader
// for one announced image, not a licence to enumerate a private state tree.
//
// What a successful zip promises: every regular file the walk selected, read once at the moment
// its turn came, plus the empty folders. It is NOT a point-in-time snapshot — a file may grow,
// shrink or be replaced by another regular file while the build runs and the bytes read are
// the ones packed. What it never does is finish quietly short: a selected entry that vanished,
// turned into a link or a special file, or cannot be read fails the whole request.
//
// Left out on purpose, and reported by the check: .git and node_modules below the start folder
// (the start itself is exported whatever it is called), anything the browse denylist names,
// symlinks and special files.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

const (
	// Folder names the check lists back as left out; a longer list is cut, not an error.
	zipExcludedListMax  = 20
	zipExportTempPrefix = "zip-"
)

// The limits, as variables so a test can hit one without writing tens of thousands of files.
// They bound the WHOLE request: the walk, the bytes read from disk, the bytes written to the
// temp file, and the time until the first byte goes out (the ALB in front of the Control Plane
// drops a connection that is silent for 60 s, so the build must end well before that).
var (
	zipMaxFiles            = 20000
	zipMaxDirs             = 4000
	zipMaxDepth            = 32
	zipMaxNameBytes        = 4 << 20 // summed length of every entry name: the manifest's volume
	zipMaxLooked           = 100000  // directory entries inspected, including the ones left out
	zipMaxBytes      int64 = 512 << 20
	zipBuildLimit          = 45 * time.Second
	zipTransferLimit       = 15 * time.Minute // a stalled client cannot hold the slot longer
	// Directory entries read per batch, as handleFSImages does, so a million-entry folder is
	// never in memory whole.
	zipReadBatch = 256
	// One build at a time, Agent-wide, held from the walk until the temp file is gone: the
	// temp file is the scarce resource (a tmpfs on some runtimes, a small home volume on
	// others) and a slow client downloading it is exactly when a second build would pile on.
	zipSlots    = make(chan struct{}, 1)
	zipSlotWait = 2 * time.Second
	// Seams for tests.
	zipReadDir   = func(f *os.File, n int) ([]os.DirEntry, error) { return f.ReadDir(n) }
	zipAfterPlan func()
	// zipSkipNames are the folders left out below the start folder.
	zipSkipNames = map[string]bool{".git": true, "node_modules": true}
)

// zipOutputCap is how many bytes the temp file may reach: the payload cap plus the per-entry
// overhead (local header, data descriptor, central record — names appear twice) that the name
// and entry caps bound.
func zipOutputCap() int64 {
	return zipMaxBytes + 2*int64(zipMaxNameBytes) + int64(zipMaxFiles+zipMaxDirs+1)*160 + 1024
}

type zipRoot struct {
	root     string // trusted absolute root the fds hang off
	relative string // the folder, relative to root
	browse   bool   // under the browse root, so the denylist applies below it
}

// resolveZipRoot admits a query path. Browse-relative and absolute-under-a-read-root are the
// same two spellings /fs/download takes, minus its generated-images exception, and a root
// itself is refused: "download my home folder" is never what a context-menu press meant, and
// it is the one request that would always hit a limit.
func resolveZipRoot(input string) (zipRoot, *fsAPIError) {
	if aerr := validateCanonicalPOSIXPath(input, true); aerr != nil {
		return zipRoot{}, aerr
	}
	if !strings.HasPrefix(input, "/") {
		browse, err := absoluteTrustedRoot(browseRoot())
		if err != nil {
			return zipRoot{}, fsErr(500, errCodeFSReadFailed, "cannot resolve browse root")
		}
		if isDenied(input) {
			return zipRoot{}, fsErr(403, errCodeFSDenied, "folder path is denied")
		}
		return zipRoot{root: browse, relative: input, browse: true}, nil
	}
	for i, raw := range allowedReadRoots() {
		root, err := absoluteTrustedRoot(raw)
		if err != nil {
			continue
		}
		rel, ok := pathUnderRoot(input, root)
		if !ok {
			continue
		}
		if rel == "" {
			return zipRoot{}, fsErr(400, errCodeFSBadPath, "choose a folder inside the root, not the root itself")
		}
		if i == 0 && isDenied(rel) {
			return zipRoot{}, fsErr(403, errCodeFSDenied, "folder path is denied")
		}
		return zipRoot{root: root, relative: rel, browse: i == 0}, nil
	}
	return zipRoot{}, fsErr(400, errCodeFSBadPath, "absolute path is outside allowed read roots")
}

// zipNameProblem says why an entry name cannot go into an archive, or "" when it can. The name
// is never rewritten into another one: a name that is not portable fails the export, because a
// silently renamed or dropped file is the failure the caller cannot see.
func zipNameProblem(name string) string {
	switch {
	case name == "":
		return "empty name"
	case !utf8.ValidString(name):
		return "not valid UTF-8"
	case strings.HasPrefix(name, "/"):
		return "absolute path"
	case strings.Contains(name, "\\"):
		return "contains a backslash"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "contains a control character"
		}
	}
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	for i, p := range parts {
		switch {
		case p == "" || p == "." || p == "..":
			return "has an empty, \".\" or \"..\" component"
		case i == 0 && len(p) >= 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z'):
			return "starts with a drive prefix"
		}
	}
	return ""
}

type zipEntry struct {
	name string // the archive name, base-prefixed, no trailing slash
	rel  string // relative to the start folder, "" for the start itself
	dir  bool
	size int64 // listing-time size; advisory, the bytes read are what count
	mode fs.FileMode
	mod  time.Time
}

type zipPlan struct {
	entries  []zipEntry
	files    int
	dirs     int
	bytes    int64
	excluded map[string]bool // distinct folder names left out
	skipped  int             // symlinks and special files
	names    int             // summed entry-name bytes
	looked   int             // entries inspected, kept or not
}

func tooLarge(format string, a ...any) *fsAPIError {
	return fsErr(http.StatusRequestEntityTooLarge, errCodeZipTooLarge, fmt.Sprintf(format, a...))
}

// ctxLimitErr turns the walk's context ending into the answer: the time budget is a size
// limit like any other, a vanished client is nobody to answer.
func ctxLimitErr(ctx context.Context) *fsAPIError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return tooLarge("the folder took longer than the %d second time limit to read", int(zipBuildLimit/time.Second))
	}
	return nil
}

var errZipGone = errors.New("client gone")

// planZip walks the folder breadth first and returns what would be packed, or the reason it
// cannot be. It is the early-rejection pass; the build re-checks everything against the bytes
// it actually reads.
func planZip(ctx context.Context, startFD int, zr zipRoot, base string, startMod time.Time) (*zipPlan, *fsAPIError) {
	if p := zipNameProblem(base); p != "" {
		return nil, fsErr(422, errCodeZipUnsafe, fmt.Sprintf("folder name %q cannot be zipped: %s", base, p))
	}
	plan := &zipPlan{excluded: map[string]bool{}}
	seen := map[string]struct{}{base: {}}
	plan.entries = append(plan.entries, zipEntry{name: base, dir: true, mode: 0o755, mod: startMod})
	plan.dirs, plan.names = 1, len(base)

	type dir struct {
		rel   string
		level int
	}
	queue := []dir{{}}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if ctx.Err() != nil {
			return nil, planCtxErr(ctx)
		}
		open := d.rel
		if open == "" {
			open = "."
		}
		dfd, err := openat2NoSymlinks(startFD, open, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return nil, zipOpenError(err, d.rel, "folder")
		}
		f := os.NewFile(uintptr(dfd), "dir")
		for {
			ents, rerr := zipReadDir(f, zipReadBatch)
			for _, e := range ents {
				if ctx.Err() != nil {
					f.Close()
					return nil, planCtxErr(ctx)
				}
				if plan.looked++; plan.looked > zipMaxLooked {
					f.Close()
					return nil, tooLarge("the folder has more than %d entries to look through", zipMaxLooked)
				}
				name := e.Name()
				rel := name
				if d.rel != "" {
					rel = d.rel + "/" + name
				}
				var st unix.Stat_t
				if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					f.Close()
					return nil, zipOpenError(err, rel, "entry")
				}
				kind := st.Mode & unix.S_IFMT
				if kind == unix.S_IFLNK || (kind != unix.S_IFDIR && kind != unix.S_IFREG) {
					plan.skipped++
					continue
				}
				if zr.browse && isDenied(path.Join(zr.relative, rel)) {
					plan.excluded[name] = true
					continue
				}
				if kind == unix.S_IFDIR && zipSkipNames[name] {
					plan.excluded[name] = true
					continue
				}
				zname := base + "/" + rel
				if p := zipNameProblem(zname); p != "" {
					f.Close()
					return nil, fsErr(422, errCodeZipUnsafe, fmt.Sprintf("%q cannot be zipped: %s", rel, p))
				}
				if _, dup := seen[zname]; dup {
					f.Close()
					return nil, fsErr(422, errCodeZipUnsafe, fmt.Sprintf("%q appears twice in the archive", rel))
				}
				seen[zname] = struct{}{}
				if plan.names += len(zname); plan.names > zipMaxNameBytes {
					f.Close()
					return nil, tooLarge("the entry names add up to more than %d bytes", zipMaxNameBytes)
				}
				ent := zipEntry{name: zname, rel: rel, mode: fs.FileMode(st.Mode & 0o777), mod: time.Unix(st.Mtim.Sec, st.Mtim.Nsec)}
				if kind == unix.S_IFDIR {
					if plan.dirs++; plan.dirs > zipMaxDirs {
						f.Close()
						return nil, tooLarge("the folder has more than %d subfolders", zipMaxDirs)
					}
					if d.level+1 > zipMaxDepth {
						f.Close()
						return nil, tooLarge("the folder is nested deeper than %d levels", zipMaxDepth)
					}
					ent.dir = true
					queue = append(queue, dir{rel: rel, level: d.level + 1})
				} else {
					if plan.files++; plan.files > zipMaxFiles {
						f.Close()
						return nil, tooLarge("the folder has more than %d files", zipMaxFiles)
					}
					ent.size = st.Size
					if plan.bytes += st.Size; plan.bytes > zipMaxBytes {
						f.Close()
						return nil, tooLarge("the folder holds more than %d MB (limit %d MB)", plan.bytes>>20, zipMaxBytes>>20)
					}
				}
				plan.entries = append(plan.entries, ent)
			}
			if rerr != nil || len(ents) == 0 {
				if rerr != nil && !errors.Is(rerr, io.EOF) {
					f.Close()
					return nil, fsErr(500, errCodeFSReadFailed, "cannot read folder "+fmt.Sprintf("%q", d.rel))
				}
				break
			}
		}
		f.Close()
	}
	return plan, nil
}

func planCtxErr(ctx context.Context) *fsAPIError {
	if aerr := ctxLimitErr(ctx); aerr != nil {
		return aerr
	}
	return &fsAPIError{code: "gone"} // status 0: the caller sees a vanished client and writes nothing
}

// zipOpenError maps a failed open or stat below the start folder. A target that was there a
// moment ago and is gone, or is now a link, is the folder changing under the walk — the
// answer is "try again", not "not found".
func zipOpenError(err error, rel, what string) *fsAPIError {
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		if rel == "" {
			if errors.Is(err, unix.ELOOP) {
				return fsErr(400, errCodeFSSymlinkNotAllowed, "symlinks are not allowed")
			}
			return fsErr(404, errCodeZipNotDir, "not a folder")
		}
		return fsErr(409, errCodeZipChanged, fmt.Sprintf("%s %q changed while the archive was being made; try again", what, rel))
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return fsErr(403, errCodeFSDenied, fmt.Sprintf("cannot read %s %q", what, rel))
	}
	return fsErr(500, errCodeFSReadFailed, fmt.Sprintf("cannot read %s %q: %v", what, rel, err))
}

type zipWriteErr struct{ err error }

func (e zipWriteErr) Error() string { return e.err.Error() }
func (e zipWriteErr) Unwrap() error { return e.err }

var errZipOutputCap = errors.New("archive output cap")
var errZipBytesCap = errors.New("bytes read cap")

// zipCapWriter fails a write that would take the temp file past its cap, so a build whose
// bytes grew past what the walk saw stops instead of filling the disk.
type zipCapWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (c *zipCapWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, zipWriteErr{errZipOutputCap}
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		err = zipWriteErr{err}
	}
	return n, err
}

// buildZip writes the plan into out. read counts the bytes taken from files, and is what the
// payload cap is checked against — never the listing sizes.
func buildZip(ctx context.Context, startFD int, plan *zipPlan, out io.Writer) *fsAPIError {
	cw := &zipCapWriter{w: out, limit: zipOutputCap()}
	zw := zip.NewWriter(cw)
	buf := make([]byte, 64<<10)
	var read int64
	for _, e := range plan.entries {
		if ctx.Err() != nil {
			return planCtxErr(ctx)
		}
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Store, Modified: e.mod}
		if e.dir {
			hdr.Name += "/"
			hdr.SetMode(fs.ModeDir | e.mode.Perm())
			if _, err := zw.CreateHeader(hdr); err != nil {
				return zipBuildError(ctx, err, e.rel)
			}
			continue
		}
		hdr.SetMode(e.mode.Perm())
		// O_NONBLOCK: a file replaced by a FIFO after the walk must fail the fstat below, not
		// block this open forever while holding the slot.
		ffd, err := openat2NoSymlinks(startFD, e.rel, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK)
		if err != nil {
			return zipOpenError(err, e.rel, "file")
		}
		f := os.NewFile(uintptr(ffd), "file")
		var st unix.Stat_t
		if err := unix.Fstat(ffd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			f.Close()
			return fsErr(409, errCodeZipChanged, fmt.Sprintf("file %q changed while the archive was being made; try again", e.rel))
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			f.Close()
			return zipBuildError(ctx, err, e.rel)
		}
		err = copyZipFile(ctx, w, f, buf, &read)
		f.Close()
		if err != nil {
			return zipBuildError(ctx, err, e.rel)
		}
	}
	if err := zw.Close(); err != nil {
		return zipBuildError(ctx, err, "")
	}
	return nil
}

func copyZipFile(ctx context.Context, dst io.Writer, src *os.File, buf []byte, read *int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if *read += int64(n); *read > zipMaxBytes {
				return errZipBytesCap
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

func zipBuildError(ctx context.Context, err error, rel string) *fsAPIError {
	switch {
	case ctx.Err() != nil:
		return planCtxErr(ctx)
	case errors.Is(err, errZipBytesCap):
		return tooLarge("the files hold more than %d MB when read (limit %d MB); they grew while the archive was being made", zipMaxBytes>>20, zipMaxBytes>>20)
	case errors.Is(err, errZipOutputCap):
		return tooLarge("the archive would be larger than %d MB", zipOutputCap()>>20)
	case errors.Is(err, unix.ENOSPC):
		return fsErr(507, errCodeZipNoSpace, "no space left for the archive")
	}
	var we zipWriteErr
	if errors.As(err, &we) {
		return fsErr(500, errCodeFSWriteFailed, "cannot write the archive")
	}
	return zipOpenError(err, rel, "file")
}

// zipTempDir is where archives are built: the agent's state dir lives on the home volume on
// every runtime (not /tmp, which is a tmpfs on ecs-ec2 and size-limited on kubernetes), and
// it is under the browse denylist, so an archive in progress cannot itself be downloaded.
func zipTempDir() string { return filepath.Join(paths.AgentStateDir(), "zip-export") }

// newZipTemp returns an already-unlinked temp file: it is reclaimed when closed on every path,
// including a crash after this call. The sweep removes what a crash BEFORE the unlink left; it
// runs under the slot, so no live build can lose its file to it.
func newZipTemp() (*os.File, error) {
	dir := zipTempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), zipExportTempPrefix) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	f, err := os.CreateTemp(dir, zipExportTempPrefix+"*.tmp")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// zipDisposition is the attachment header: an ASCII fallback for clients that ignore
// filename*, and the UTF-8 form for the rest. An empty base falls back to "folder".
func zipDisposition(base string) string {
	if base == "" {
		base = "folder"
	}
	name := base + ".zip"
	var ascii strings.Builder
	for _, r := range name {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' || r == '%' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// fsZipCheck is the answer to `check=1`: what a download of the same query would pack, so the
// Console can show the numbers (and the exclusions) before any byte is built, and show a
// refusal as a message instead of a failed download.
type fsZipCheck struct {
	Name     string   `json:"name"`
	Files    int      `json:"files"`
	Dirs     int      `json:"dirs"`
	Bytes    int64    `json:"bytes"`
	Excluded []string `json:"excluded"`
	Skipped  int      `json:"skipped"`
}

func handleFSDownloadZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// ServeMux sends HEAD to a GET pattern; answering it would mean building the archive
		// for nothing.
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	zr, aerr := resolveZipRoot(r.URL.Query().Get("path"))
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	check := r.URL.Query().Get("check") == "1"

	select {
	case zipSlots <- struct{}{}:
		defer func() { <-zipSlots }()
	case <-r.Context().Done():
		return
	case <-time.After(zipSlotWait):
		httpx.WriteErr(w, http.StatusServiceUnavailable, errCodeZipBusy, "another folder is being zipped; try again in a moment")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), zipBuildLimit)
	defer cancel()
	rootFD, err := unix.Open(zr.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		writeFSError(w, mapFDOpenError(err, "open root"))
		return
	}
	defer unix.Close(rootFD)
	startFD, err := openat2NoSymlinks(rootFD, zr.relative, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		writeFSError(w, zipOpenError(err, "", "folder"))
		return
	}
	defer unix.Close(startFD)
	var startStat unix.Stat_t
	if err := unix.Fstat(startFD, &startStat); err != nil {
		writeFSError(w, fsErr(500, errCodeFSReadFailed, "cannot stat folder"))
		return
	}
	base := path.Base(zr.relative)
	plan, aerr := planZip(ctx, startFD, zr, base, time.Unix(startStat.Mtim.Sec, startStat.Mtim.Nsec))
	if aerr != nil {
		writeZipErr(w, aerr)
		return
	}
	if check {
		excluded := make([]string, 0, len(plan.excluded))
		for n := range plan.excluded {
			excluded = append(excluded, n)
		}
		sort.Strings(excluded)
		if len(excluded) > zipExcludedListMax {
			excluded = excluded[:zipExcludedListMax]
		}
		httpx.WriteJSON(w, http.StatusOK, fsZipCheck{
			Name: base + ".zip", Files: plan.files, Dirs: plan.dirs, Bytes: plan.bytes,
			Excluded: excluded, Skipped: plan.skipped,
		})
		return
	}
	if zipAfterPlan != nil {
		zipAfterPlan()
	}

	tmp, err := newZipTemp()
	if err != nil {
		writeFSError(w, fsErr(500, errCodeFSWriteFailed, "cannot create the archive's temp file"))
		return
	}
	defer tmp.Close()
	if aerr := buildZip(ctx, startFD, plan, tmp); aerr != nil {
		writeZipErr(w, aerr)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeFSError(w, fsErr(500, errCodeFSReadFailed, "cannot read the archive back"))
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", zipDisposition(base))
	h.Set("Cache-Control", "private, no-store")
	// Each request builds its own archive, so a byte range of one is not a range of the next:
	// ServeContent must not honour a resumed download against freshly built bytes.
	r.Header.Del("Range")
	r.Header.Del("If-Range")
	// The transfer is bounded too: the slot is held until the client has the last byte.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(zipTransferLimit))
	http.ServeContent(w, r, "", time.Time{}, tmp)
}

// writeZipErr answers like writeFSError, except that a vanished client (status 0) is not
// answered at all.
func writeZipErr(w http.ResponseWriter, aerr *fsAPIError) {
	if aerr.status == 0 {
		return
	}
	writeFSError(w, aerr)
}

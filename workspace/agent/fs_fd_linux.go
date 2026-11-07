//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

const (
	maxEditorFileBytes = 2 << 20
	maxFSPathBytes     = 4096
	maxFSNameBytes     = 255
)

type fsAPIError struct {
	status  int
	code    string
	message string
}

func fsErr(status int, code, message string) *fsAPIError {
	return &fsAPIError{status: status, code: code, message: message}
}

type fdReadPath struct {
	root       string
	relative   string
	display    string
	readOnly   bool
	browseRoot bool
}

// validateCanonicalPOSIXPath performs lexical validation only. It never cleans
// an input into another spelling: aliases are rejected so the accepted relative
// string is also the PUT mutex key.
func validateCanonicalPOSIXPath(input string, allowAbsolute bool) *fsAPIError {
	if input == "" || len(input) > maxFSPathBytes || strings.IndexByte(input, 0) >= 0 ||
		strings.Contains(input, "\\") {
		return fsErr(400, errCodeFSBadPath, "invalid file path")
	}
	if len(input) >= 2 && ((input[0] >= 'a' && input[0] <= 'z') || (input[0] >= 'A' && input[0] <= 'Z')) && input[1] == ':' {
		return fsErr(400, errCodeFSBadPath, "Windows drive paths are not allowed")
	}
	absolute := strings.HasPrefix(input, "/")
	if absolute && !allowAbsolute {
		return fsErr(400, errCodeFSBadPath, "absolute paths are not writable")
	}

	parts := strings.Split(input, "/")
	start := 0
	if absolute {
		start = 1 // the leading slash is the absolute-path marker, not a component
		if len(parts) == 2 && parts[1] == "" {
			// "/" names the allowed root itself. It is lexically canonical and
			// will later be classified as not_file.
			return nil
		}
	}
	for _, part := range parts[start:] {
		if part == "" || part == "." || part == ".." || len(part) > maxFSNameBytes {
			return fsErr(400, errCodeFSBadPath, "file path is not canonical")
		}
	}
	return nil
}

func absoluteTrustedRoot(root string) (string, error) {
	clean := filepath.Clean(root)
	if filepath.IsAbs(clean) {
		return clean, nil
	}
	return filepath.Abs(clean)
}

func pathUnderRoot(input, root string) (relative string, ok bool) {
	if input == root {
		return "", true
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if !strings.HasPrefix(input, prefix) {
		return "", false
	}
	return strings.TrimPrefix(input, prefix), true
}

func resolveFDReadPath(input string) (fdReadPath, *fsAPIError) {
	if aerr := validateCanonicalPOSIXPath(input, true); aerr != nil {
		return fdReadPath{}, aerr
	}
	browse, err := absoluteTrustedRoot(browseRoot())
	if err != nil {
		return fdReadPath{}, fsErr(500, errCodeFSReadFailed, "cannot resolve browse root")
	}
	// A session's changed-files list links to the agent memory it wrote, which sits outside
	// the browse root ($CLAUDE_CONFIG_DIR) or under a denylisted directory (~/.codex). Only
	// what the memory snapshot allowlist versions is served, read-only and opened beneath
	// its memory root, so the transcripts and credentials next to it stay unreachable.
	// It applies only where the browse root would refuse the path: memory under a
	// non-denied part of home (a custom CLAUDE_CONFIG_DIR) stays an ordinary browse path,
	// so GET and PUT agree on its editability and the GET still waits on an in-flight PUT.
	// A browse path that crosses a symlink is refused too (and so is its PUT), which is
	// how a CLAUDE_CONFIG_DIR linked out of home reaches the strip.
	memoryCandidate, browsable := input, false
	if strings.HasPrefix(input, "/") {
		if relative, ok := pathUnderRoot(input, browse); ok {
			browsable = !isDenied(relative)
		}
	} else {
		memoryCandidate, browsable = filepath.Join(browse, input), !isDenied(input)
	}
	if _, root, relative, ok := memoryx.MemoryFile(memoryCandidate); ok &&
		(!browsable || browseReadCrossesSymlink(browse, memoryCandidate)) {
		return fdReadPath{root: root, relative: relative, display: input, readOnly: true}, nil
	}
	generatedRoot, err := absoluteTrustedRoot(codexGeneratedImagesRoot())
	if err == nil {
		// Transcript userfile paths below the browse root are deliberately
		// rendered relative to it. Re-identify the generated-images exception
		// before treating that spelling as an ordinary .codex path, otherwise
		// the browse-root denylist rejects the image after it was shared.
		if !strings.HasPrefix(input, "/") {
			if generatedRelative, relErr := filepath.Rel(browse, generatedRoot); relErr == nil {
				generatedRelative = filepath.ToSlash(generatedRelative)
				if relative, ok := pathUnderRoot(input, generatedRelative); ok {
					switch strings.ToLower(filepath.Ext(relative)) {
					case ".png", ".jpg", ".jpeg", ".webp", ".gif":
						return fdReadPath{root: generatedRoot, relative: relative, display: input, readOnly: true}, nil
					default:
						return fdReadPath{}, fsErr(403, errCodeFSDenied, "only generated image files may be read from CODEX_HOME")
					}
				}
			}
			return fdReadPath{
				root: browse, relative: input, display: input, browseRoot: true,
			}, nil
		}
		if relative, ok := pathUnderRoot(input, generatedRoot); ok {
			// image_gen announces only these raster outputs. Do not serve SVG
			// or arbitrary files from Codex's otherwise-private state tree.
			switch strings.ToLower(filepath.Ext(relative)) {
			case ".png", ".jpg", ".jpeg", ".webp", ".gif":
				return fdReadPath{root: generatedRoot, relative: relative, display: input, readOnly: true}, nil
			default:
				return fdReadPath{}, fsErr(403, errCodeFSDenied, "only generated image files may be read from CODEX_HOME")
			}
		}
	}

	roots := allowedReadRoots()
	for i, rawRoot := range roots {
		root, err := absoluteTrustedRoot(rawRoot)
		if err != nil {
			continue
		}
		relative, ok := pathUnderRoot(input, root)
		if !ok {
			continue
		}
		if i == 0 {
			return fdReadPath{
				root: root, relative: relative, display: relative, browseRoot: true,
			}, nil
		}
		return fdReadPath{
			root: root, relative: relative, display: input, readOnly: true,
		}, nil
	}
	return fdReadPath{}, fsErr(400, errCodeFSBadPath, "absolute path is outside allowed read roots")
}

// browseReadCrossesSymlink reports whether full's directory is reached from the browse root
// through a symlink, which the ordinary browse read (openat2 RESOLVE_NO_SYMLINKS) refuses.
func browseReadCrossesSymlink(browse, full string) bool {
	dir := filepath.Dir(full)
	rel, err := filepath.Rel(browse, dir)
	if err != nil {
		return false
	}
	resolvedBrowse, err := filepath.EvalSymlinks(browse)
	if err != nil {
		return false
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	return err == nil && resolvedDir != filepath.Join(resolvedBrowse, rel)
}

func resolveFDWritePath(input string) (string, *fsAPIError) {
	if aerr := validateCanonicalPOSIXPath(input, false); aerr != nil {
		return "", aerr
	}
	return input, nil
}

type openedFDFile struct {
	rootFD   int
	parentFD int
	file     *os.File
	base     string
}

func (o *openedFDFile) close() {
	if o.file != nil {
		_ = o.file.Close()
	}
	if o.parentFD >= 0 {
		_ = unix.Close(o.parentFD)
	}
	if o.rootFD >= 0 {
		_ = unix.Close(o.rootFD)
	}
}

func splitParent(relative string) (parent, base string) {
	if i := strings.LastIndexByte(relative, '/'); i >= 0 {
		return relative[:i], relative[i+1:]
	}
	return "", relative
}

func openat2NoSymlinks(dirFD int, relative string, flags int) (int, error) {
	if relative == "" {
		relative = "."
	}
	return unix.Openat2(dirFD, relative, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
}

func mapFDOpenError(err error, operation string) *fsAPIError {
	switch {
	case errors.Is(err, unix.ELOOP):
		return fsErr(400, errCodeFSSymlinkNotAllowed, "symlinks are not allowed")
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return fsErr(404, errCodeFSNotFile, "target is not an existing regular file")
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return fsErr(403, errCodeFSDenied, "file access is denied")
	default:
		return fsErr(500, errCodeFSReadFailed, operation+": "+err.Error())
	}
}

// openFDFile fixes the trusted root and resolved parent directory as fds, then
// opens the final regular file without following any symlink component. O_NONBLOCK
// keeps a FIFO swapped in after the lstat from blocking the open; the fstat below
// then refuses it as not regular. Hard links are NOT refused here (docs/build/07-security.md 7.2.2).
func openFDFile(path fdReadPath) (*openedFDFile, *fsAPIError) {
	if path.browseRoot && isDenied(path.relative) {
		return nil, fsErr(403, errCodeFSDenied, "file path is denied")
	}
	rootFD, err := unix.Open(path.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, mapFDOpenError(err, "open root")
	}
	out := &openedFDFile{rootFD: rootFD, parentFD: -1}
	parent, base := splitParent(path.relative)
	out.base = base
	parentFD, err := openat2NoSymlinks(rootFD, parent, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		out.close()
		return nil, mapFDOpenError(err, "open parent directory")
	}
	out.parentFD = parentFD

	var lst unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &lst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		out.close()
		return nil, mapFDOpenError(err, "inspect target")
	}
	if lst.Mode&unix.S_IFMT == unix.S_IFLNK {
		out.close()
		return nil, fsErr(400, errCodeFSSymlinkNotAllowed, "symlinks are not allowed")
	}
	if lst.Mode&unix.S_IFMT != unix.S_IFREG {
		out.close()
		return nil, fsErr(404, errCodeFSNotFile, "target is not a regular file")
	}

	fileFD, err := openat2NoSymlinks(parentFD, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK)
	if err != nil {
		out.close()
		return nil, mapFDOpenError(err, "open target")
	}
	file := os.NewFile(uintptr(fileFD), base)
	if file == nil {
		_ = unix.Close(fileFD)
		out.close()
		return nil, fsErr(500, errCodeFSReadFailed, "open target file")
	}
	out.file = file
	var openedStat unix.Stat_t
	if err := unix.Fstat(fileFD, &openedStat); err != nil {
		out.close()
		return nil, fsErr(500, errCodeFSReadFailed, "inspect opened target")
	}
	if openedStat.Mode&unix.S_IFMT != unix.S_IFREG {
		out.close()
		return nil, fsErr(404, errCodeFSNotFile, "target is not a regular file")
	}
	return out, nil
}

type fileStatSnapshot struct {
	dev, ino, mode, nlink, rdev uint64
	uid, gid                    uint32
	size, blockSize, blocks     int64
	mtimeSec, mtimeNsec         int64
	ctimeSec, ctimeNsec         int64
}

// statSnapshot widens every field to a fixed width, because unix.Stat_t is NOT the
// same struct on every architecture.
//
// ⚠️ The conversions are not decoration. On linux/amd64 Blksize is int64 and Nlink is
// uint64; on linux/arm64 Blksize is int32 and Nlink is uint32. Assigning either
// directly compiles on one architecture and fails on the other — which is exactly how
// this was found: the first attempt to build the workspace image for arm64 stopped
// here (docs/log/70 §70.9). `make arm64-build` / the ci.yml cross-compile step exists so
// the next one is caught without an EC2 instance.
func statSnapshot(st *unix.Stat_t) fileStatSnapshot {
	return fileStatSnapshot{
		dev: uint64(st.Dev), ino: st.Ino, mode: uint64(st.Mode), nlink: uint64(st.Nlink),
		rdev: uint64(st.Rdev), uid: st.Uid, gid: st.Gid,
		size: st.Size, blockSize: int64(st.Blksize), blocks: st.Blocks,
		mtimeSec: st.Mtim.Sec, mtimeNsec: st.Mtim.Nsec,
		ctimeSec: st.Ctim.Sec, ctimeNsec: st.Ctim.Nsec,
	}
}

type stableFileSnapshot struct {
	bytes []byte
	size  int64
	mode  uint32
}

type snapshotHooks struct {
	afterBeforeStat func(attempt int)
}

// readStableFileSnapshot performs at most two fstat/read/fstat attempts. Atime
// is deliberately excluded from stability comparison because the read itself
// may update it; inode/mode/link count/size/mtime/ctime must remain unchanged.
func readStableFileSnapshot(file *os.File, hooks snapshotHooks) (stableFileSnapshot, *fsAPIError) {
	fd := int(file.Fd())
	for attempt := 0; attempt < 2; attempt++ {
		var before, after unix.Stat_t
		if err := unix.Fstat(fd, &before); err != nil {
			if attempt == 1 {
				return stableFileSnapshot{}, fsErr(500, errCodeFSReadFailed, "cannot stat current file")
			}
			continue
		}
		if hooks.afterBeforeStat != nil {
			hooks.afterBeforeStat(attempt)
		}
		buf := make([]byte, maxEditorFileBytes+1)
		n, err := file.ReadAt(buf, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			if attempt == 1 {
				return stableFileSnapshot{}, fsErr(500, errCodeFSReadFailed, "cannot read current file")
			}
			continue
		}
		buf = buf[:n]
		if err := unix.Fstat(fd, &after); err != nil {
			if attempt == 1 {
				return stableFileSnapshot{}, fsErr(500, errCodeFSReadFailed, "cannot restat current file")
			}
			continue
		}
		if statSnapshot(&before) != statSnapshot(&after) {
			continue
		}
		if after.Size <= maxEditorFileBytes && int64(n) == after.Size {
			return stableFileSnapshot{bytes: buf, size: after.Size, mode: after.Mode}, nil
		}
		if after.Size > maxEditorFileBytes && n == maxEditorFileBytes+1 {
			return stableFileSnapshot{bytes: buf, size: after.Size, mode: after.Mode}, nil
		}
	}
	return stableFileSnapshot{}, fsErr(500, errCodeFSReadFailed, "file changed while it was being read")
}

type fsAtomicWriteOps struct {
	fchmod   func(int, uint32) error
	fsync    func(int) error
	renameat func(int, string, int, string) error
	unlinkat func(int, string, int) error
}

var defaultFSAtomicWriteOps = fsAtomicWriteOps{
	fchmod:   unix.Fchmod,
	fsync:    unix.Fsync,
	renameat: unix.Renameat,
	unlinkat: unix.Unlinkat,
}

func createTempAt(parentFD int) (*os.File, string, error) {
	var random [12]byte
	for attempt := 0; attempt < 128; attempt++ {
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := ".af-edit-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			return os.NewFile(uintptr(fd), name), name, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("cannot allocate temporary filename")
}

type atomicWriteResult struct {
	renamed bool
	err     *fsAPIError
}

func atomicReplace(opened *openedFDFile, content []byte, mode uint32, ops fsAtomicWriteOps) atomicWriteResult {
	temp, tempName, err := createTempAt(opened.parentFD)
	if err != nil {
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EROFS) {
			return atomicWriteResult{err: fsErr(403, errCodeFSDenied, "parent directory is not writable")}
		}
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot create temporary file")}
	}
	tempFD := int(temp.Fd())
	cleanup := func() {
		_ = temp.Close()
		_ = ops.unlinkat(opened.parentFD, tempName, 0)
	}
	if n, err := temp.Write(content); err != nil || n != len(content) {
		cleanup()
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot write temporary file")}
	}
	if err := ops.fchmod(tempFD, mode&0o777); err != nil {
		cleanup()
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot preserve file permissions")}
	}
	if err := ops.fsync(tempFD); err != nil {
		cleanup()
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot sync temporary file")}
	}
	if err := temp.Close(); err != nil {
		_ = ops.unlinkat(opened.parentFD, tempName, 0)
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot close temporary file")}
	}
	if err := ops.renameat(opened.parentFD, tempName, opened.parentFD, opened.base); err != nil {
		_ = ops.unlinkat(opened.parentFD, tempName, 0)
		return atomicWriteResult{err: fsErr(500, errCodeFSWriteFailed, "cannot replace target file")}
	}
	if err := ops.fsync(opened.parentFD); err != nil {
		return atomicWriteResult{
			renamed: true,
			err:     fsErr(500, errCodeFSWriteStateUnknown, "file was replaced but directory sync failed"),
		}
	}
	return atomicWriteResult{renamed: true}
}

// uploadDir is a destination folder held as an fd, opened beneath the browse root with no
// symlink anywhere on the way. Every name written through it lands in the folder the
// denylist was judged against; a path string re-resolved per file could be pointed elsewhere
// in between.
type uploadDir struct {
	rootFD int
	dirFD  int
}

// openUploadDir opens rel (browse-root-relative, "" for the root itself) as a directory fd.
// A symlinked component yields ELOOP, which the caller reports as a refusal. With create, a
// missing folder is made one component at a time with mkdirat on the fd of its parent, so no
// path string is ever re-resolved and a parent swapped for a link yields ELOOP before anything
// is created.
func openUploadDir(rel string, create bool) (*uploadDir, error) {
	root, err := absoluteTrustedRoot(browseRoot())
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	rel = strings.Trim(rel, "/")
	if !create || rel == "" {
		dirFD, err := openat2NoSymlinks(rootFD, rel, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			_ = unix.Close(rootFD)
			return nil, err
		}
		return &uploadDir{rootFD: rootFD, dirFD: dirFD}, nil
	}
	cur, owned := rootFD, false
	for _, comp := range strings.Split(rel, "/") {
		next, err := openat2NoSymlinks(cur, comp, unix.O_RDONLY|unix.O_DIRECTORY)
		if errors.Is(err, unix.ENOENT) {
			if merr := unix.Mkdirat(cur, comp, 0o700); merr != nil && !errors.Is(merr, unix.EEXIST) {
				err = merr
			} else {
				next, err = openat2NoSymlinks(cur, comp, unix.O_RDONLY|unix.O_DIRECTORY)
			}
		}
		if owned {
			_ = unix.Close(cur)
		}
		if err != nil {
			_ = unix.Close(rootFD)
			return nil, err
		}
		cur, owned = next, true
	}
	return &uploadDir{rootFD: rootFD, dirFD: cur}, nil
}

func (d *uploadDir) close() {
	_ = unix.Close(d.dirFD)
	_ = unix.Close(d.rootFD)
}

// exists reports whether name is present in the folder, without following a symlink.
func (d *uploadDir) exists(name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(d.dirFD, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

// closeTemp is a seam so a test can make the close of a finished temp file fail.
var closeTemp = func(f *os.File) error { return f.Close() }

var errUploadTooLarge = errors.New("upload exceeds the size cap")
var errUploadExists = errors.New("destination exists")

// store copies src (at most max bytes) into a temp file in the folder and renames it to name.
// Without overwrite the rename refuses to replace anything (RENAME_NOREPLACE), so a file that
// appeared after the caller's existence check is reported as errUploadExists, not clobbered.
func (d *uploadDir) store(name string, src io.Reader, max int64, overwrite bool) error {
	tmp, tmpName, err := createTempAt(d.dirFD)
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(src, max+1))
	// A copy failure wins; otherwise a failed close (a delayed write error) is a failed store.
	if cerr := closeTemp(tmp); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = errUploadTooLarge
	}
	if err != nil {
		_ = unix.Unlinkat(d.dirFD, tmpName, 0)
		return err
	}
	if overwrite {
		err = unix.Renameat(d.dirFD, tmpName, d.dirFD, name)
	} else {
		err = unix.Renameat2(d.dirFD, tmpName, d.dirFD, name, unix.RENAME_NOREPLACE)
	}
	if err != nil {
		_ = unix.Unlinkat(d.dirFD, tmpName, 0)
		if errors.Is(err, unix.EEXIST) {
			return errUploadExists
		}
		return err
	}
	return nil
}

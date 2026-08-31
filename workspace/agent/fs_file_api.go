package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sync"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const maxFSFilePUTBodyBytes = 16 << 20

var revisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func fileRevision(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type keyedFileMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedFileMutexEntry
}

type keyedFileMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (m *keyedFileMutex) lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedFileMutexEntry)
	}
	entry := m.locks[key]
	if entry == nil {
		entry = &keyedFileMutexEntry{}
		m.locks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

type fsFileService struct {
	locks         *keyedFileMutex
	writeOps      fsAtomicWriteOps
	snapshotHooks snapshotHooks
}

var defaultFSFileService = fsFileService{
	locks:    &keyedFileMutex{},
	writeOps: defaultFSAtomicWriteOps,
}

type fsFilePUTWire struct {
	Path             *string `json:"path"`
	Content          *string `json:"content"`
	BaseDiskRevision *string `json:"baseDiskRevision"`
}

type fsFilePUTRequest struct {
	path             string
	content          []byte
	baseDiskRevision string
}

func decodeFSFilePUT(r *http.Request) (fsFilePUTRequest, *fsAPIError) {
	var wire fsFilePUTWire
	if derr := httpx.DecodeStrictJSON(r, &wire, maxFSFilePUTBodyBytes); derr != nil {
		return fsFilePUTRequest{}, fsErr(derr.Status, derr.Code, derr.Message)
	}
	if wire.Path == nil || wire.Content == nil || wire.BaseDiskRevision == nil {
		return fsFilePUTRequest{}, fsErr(400, errCodeFSBadRequest, "path, content, and baseDiskRevision are required")
	}
	if !revisionPattern.MatchString(*wire.BaseDiskRevision) {
		return fsFilePUTRequest{}, fsErr(400, errCodeFSBadRequest, "baseDiskRevision is invalid")
	}
	content := []byte(*wire.Content)
	switch {
	case len(content) > maxEditorFileBytes:
		return fsFilePUTRequest{}, fsErr(413, errCodeFSTooLarge, "decoded content exceeds 2 MiB")
	case bytes.IndexByte(content, 0) >= 0:
		return fsFilePUTRequest{}, fsErr(415, errCodeFSBinaryNotSupported, "NUL bytes are not supported")
	case bytes.IndexByte(content, '\r') >= 0:
		return fsFilePUTRequest{}, fsErr(415, errCodeFSUnsupportedNewline, "only LF newlines are supported")
	}
	path, aerr := resolveFDWritePath(*wire.Path)
	if aerr != nil {
		return fsFilePUTRequest{}, aerr
	}
	return fsFilePUTRequest{
		path: path, content: content, baseDiskRevision: *wire.BaseDiskRevision,
	}, nil
}

type fsFilePUTResponse struct {
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Revision string `json:"revision"`
}

func putFSFile(ctx context.Context, req fsFilePUTRequest, service fsFileService) (fsFilePUTResponse, *fsAPIError) {
	if service.locks == nil {
		service.locks = &keyedFileMutex{}
	}
	unlock := service.locks.lock(req.path)
	defer unlock()

	// The mutex alone cannot serialize a PUT whose goroutine was still parked
	// before this lock when the client timed out: a recovery GET can win the
	// lock, observe the old base, and hand the client a discard target that a
	// late CAS-and-rename would then invalidate. Checking the request context
	// here makes the abandonment boundary two-valued: cancelled before the
	// lock → abort with the disk untouched (this check); cancelled after → the
	// write runs to its rename/error decision and a queued GET waits for that
	// outcome. No cancellation check happens mid-write. The 499 response
	// normally reaches no one — the client is already gone — its purpose is
	// guaranteeing the disk stayed unchanged.
	if ctx.Err() != nil {
		return fsFilePUTResponse{}, fsErr(499, errCodeFSWriteCancelled, "request was cancelled before the write began")
	}

	if isDenied(req.path) {
		return fsFilePUTResponse{}, fsErr(403, errCodeFSDenied, "file path is denied")
	}
	root, err := absoluteTrustedRoot(browseRoot())
	if err != nil {
		return fsFilePUTResponse{}, fsErr(500, errCodeFSReadFailed, "cannot resolve browse root")
	}
	opened, aerr := openFDFile(fdReadPath{
		root: root, relative: req.path, display: req.path, browseRoot: true,
	})
	if aerr != nil {
		return fsFilePUTResponse{}, aerr
	}
	defer opened.close()

	current, aerr := readStableFileSnapshot(opened.file, service.snapshotHooks)
	if aerr != nil {
		return fsFilePUTResponse{}, aerr
	}
	switch {
	case current.size > maxEditorFileBytes:
		return fsFilePUTResponse{}, fsErr(413, errCodeFSTooLarge, "current file exceeds 2 MiB")
	case bytes.IndexByte(current.bytes, 0) >= 0 || !utf8.Valid(current.bytes):
		return fsFilePUTResponse{}, fsErr(415, errCodeFSBinaryNotSupported, "current file is not supported text")
	case bytes.IndexByte(current.bytes, '\r') >= 0:
		return fsFilePUTResponse{}, fsErr(415, errCodeFSUnsupportedNewline, "current file does not use LF-only newlines")
	}
	if fileRevision(current.bytes) != req.baseDiskRevision {
		return fsFilePUTResponse{}, fsErr(409, errCodeFSRevisionConflict, "file changed since it was read")
	}

	result := atomicReplace(opened, req.content, current.mode, service.writeOps)
	if result.err != nil {
		return fsFilePUTResponse{}, result.err
	}
	return fsFilePUTResponse{
		Path: req.path, Size: len(req.content), Revision: fileRevision(req.content),
	}, nil
}

func writeFSError(w http.ResponseWriter, aerr *fsAPIError) {
	httpx.WriteErr(w, aerr.status, aerr.code, aerr.message)
}

func handleFSFilePut(w http.ResponseWriter, r *http.Request) {
	req, aerr := decodeFSFilePUT(r)
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	resp, aerr := putFSFile(r.Context(), req, defaultFSFileService)
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func readFSFile(input string, service fsFileService) (map[string]any, *fsAPIError) {
	path, aerr := resolveFDReadPath(input)
	if aerr != nil {
		return nil, aerr
	}
	if path.browseRoot {
		// An editor recovery GET must observe the outcome of any in-flight PUT
		// on the same path. A client-side PUT timeout does not stop the Agent's
		// atomic write, so an unserialized read could return the pre-rename base
		// and let the client discard to a snapshot the pending rename is about
		// to replace. path.relative is the same canonical spelling as the PUT
		// mutex key (aliases are rejected at validation). Read-only roots are
		// not PUT targets and stay unlocked: their relative spelling could
		// collide with an unrelated browse-root key.
		if service.locks == nil {
			service.locks = &keyedFileMutex{}
		}
		unlock := service.locks.lock(path.relative)
		defer unlock()
	}
	opened, aerr := openFDFile(path)
	if aerr != nil {
		return nil, aerr
	}
	defer opened.close()
	snapshot, aerr := readStableFileSnapshot(opened.file, snapshotHooks{})
	if aerr != nil {
		return nil, aerr
	}

	resp := map[string]any{
		"path": path.display, "size": snapshot.size,
		"binary": false, "truncated": false,
		"editable": false, "editabilityReason": nil,
	}
	tooLarge := snapshot.size > maxEditorFileBytes
	hasNUL := bytes.IndexByte(snapshot.bytes, 0) >= 0
	validUTF8 := utf8.Valid(snapshot.bytes)
	hasCR := bytes.IndexByte(snapshot.bytes, '\r') >= 0

	switch {
	case tooLarge:
		resp["truncated"] = true
		resp["content"] = "(file too large to preview)"
	case hasNUL:
		resp["binary"] = true
	case !validUTF8:
		resp["binary"] = true
	default:
		resp["content"] = string(snapshot.bytes)
		if isLFSPointer(snapshot.bytes) {
			resp["lfs"] = true
		}
	}

	switch {
	case path.readOnly:
		resp["editabilityReason"] = "read_only_root"
	case tooLarge:
		resp["editabilityReason"] = "too_large"
	case hasNUL:
		resp["editabilityReason"] = "binary"
	case !validUTF8:
		resp["editabilityReason"] = "invalid_utf8"
	case hasCR:
		resp["editabilityReason"] = "unsupported_newline"
	default:
		resp["editable"] = true
		resp["revision"] = fileRevision(snapshot.bytes)
	}
	return resp, nil
}

func handleFSFile(w http.ResponseWriter, r *http.Request) {
	resp, aerr := readFSFile(r.URL.Query().Get("path"), defaultFSFileService)
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	// meta=1 (docs/log/44 §3.2): the external-change probe's metadata-only answer.
	// It is the ordinary GET minus `content` — same path resolution, denylist,
	// symlink rejection, path mutex wait on in-flight PUTs, editability order,
	// and error contract, because the flag only strips the field after the read.
	if r.URL.Query().Get("meta") == "1" {
		delete(resp, "content")
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func handleFSDownload(w http.ResponseWriter, r *http.Request) {
	path, aerr := resolveFDReadPath(r.URL.Query().Get("path"))
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	opened, aerr := openFDFile(path)
	if aerr != nil {
		writeFSError(w, aerr)
		return
	}
	defer opened.close()
	fi, err := opened.file.Stat()
	if err != nil {
		writeFSError(w, fsErr(500, errCodeFSReadFailed, "cannot stat opened file"))
		return
	}
	name := filepath.Base(path.display)
	ct := "application/octet-stream"
	if it := imageContentType(name); it != "" {
		ct = it
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, fi.ModTime(), opened.file)
}

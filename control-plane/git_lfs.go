package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Git LFS server for the internal git provider (docs/reference/internal-git-provider,
// P3). git-http-backend does NOT speak LFS, so the CP implements the Batch API and
// the "basic" transfer itself, reusing the exact auth + confinement of the smart-HTTP
// face (authorizeGitRepo). Objects are content-addressed by their sha256 (oid) under
// the repo's own .git tree, so they move/delete with the repo automatically. A
// per-tenant capacity quota (max_lfs_bytes) is enforced at both batch and upload time.
//
// The workspace already ships git-lfs wired to the unified cred helper, so the same
// injected git token authenticates LFS transparently — no client-side change.

const lfsContentType = "application/vnd.git-lfs+json"

var errQuotaExceeded = errors.New("lfs quota exceeded")

// validOID accepts a lowercase-hex sha256 (64 chars) — also the path-safety check
// for the transfer endpoints (no separators or traversal possible).
func validOID(oid string) bool {
	if len(oid) != 64 {
		return false
	}
	for i := 0; i < len(oid); i++ {
		c := oid[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// lfsObjectPath is the on-disk location of an object, sharded by the oid prefix and
// contained within the repo's .git tree (so delete/rename of the repo carries it).
func (c config) lfsObjectPath(slug, repo, oid string) string {
	return filepath.Join(c.mgr.dataRoot, "git", filepath.Base(slug), repo+".git",
		"lfs", "objects", oid[0:2], oid[2:4], oid)
}

// lfsHref is the absolute transfer URL returned in a batch action; it points back
// to the CP (public base = Caddy TLS terminus), which the LFS client reaches with
// the same Basic git token via the cred helper.
func (c config) lfsHref(slug, repo, oid string) string {
	return strings.TrimRight(c.publicBaseURL, "/") + "/git/" + slug + "/" + repo + ".git/info/lfs/objects/" + oid
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func writeLFSErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", lfsContentType)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="agent-fleet internal git"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (e *gitAuthErr) writeLFS(w http.ResponseWriter) { writeLFSErr(w, e.status, e.msg) }

// handleLFSBatch answers POST .../info/lfs/objects/batch: for each requested object
// it returns a transfer action (upload/download) or an inline error (missing on
// download, over-quota on upload). Quota is projected across the batch so a single
// request cannot slip past the cap.
func (c config) handleLFSBatch(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg := r.PathValue("slug"), r.PathValue("repo")
	name, mv, _, aerr := c.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	if c.publicBaseURL == "" {
		writeLFSErr(w, http.StatusServiceUnavailable, "internal git not configured (PUBLIC_BASE_URL)")
		return
	}
	var req struct {
		Operation string `json:"operation"`
		Objects   []struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeLFSErr(w, http.StatusBadRequest, "invalid batch request")
		return
	}
	upload := req.Operation == "upload"
	if upload && !canPush(mv.Role) {
		writeLFSErr(w, http.StatusForbidden, "read-only: push not permitted for this role")
		return
	}

	// Quota context (upload only): remaining = max - already-stored; incoming
	// accumulates the not-yet-present objects in THIS batch.
	var remaining int64 = -1 // -1 = unlimited
	if upload {
		t, err := c.mgr.store.GetTenant(r.Context(), mv.TenantID)
		if err != nil {
			writeLFSErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if max := parseLimits(t.Limits).MaxLFSBytes; max > 0 {
			used, err := c.mgr.store.TenantLFSBytes(r.Context(), mv.TenantID)
			if err != nil {
				writeLFSErr(w, http.StatusInternalServerError, "store error")
				return
			}
			remaining = max - used
		}
	}

	out := make([]map[string]any, 0, len(req.Objects))
	for _, o := range req.Objects {
		obj := map[string]any{"oid": o.OID, "size": o.Size}
		if !validOID(o.OID) {
			obj["error"] = map[string]any{"code": http.StatusUnprocessableEntity, "message": "invalid oid"}
			out = append(out, obj)
			continue
		}
		exists := fileExists(c.lfsObjectPath(slug, name, o.OID))
		href := c.lfsHref(slug, name, o.OID)
		switch {
		case upload && exists:
			// Already stored → no action; the client skips the transfer.
		case upload && remaining >= 0 && o.Size > remaining:
			obj["error"] = map[string]any{"code": http.StatusInsufficientStorage, "message": "tenant LFS quota exceeded"}
		case upload:
			obj["actions"] = map[string]any{"upload": map[string]any{"href": href}}
			if remaining >= 0 {
				remaining -= o.Size
			}
		case exists: // download
			obj["actions"] = map[string]any{"download": map[string]any{"href": href}}
		default: // download, missing
			obj["error"] = map[string]any{"code": http.StatusNotFound, "message": "object does not exist"}
		}
		out = append(out, obj)
	}
	w.Header().Set("Content-Type", lfsContentType)
	writeJSON(w, http.StatusOK, map[string]any{"transfer": "basic", "objects": out})
}

// handleLFSUpload stores an object (PUT .../info/lfs/objects/{oid}). It streams the
// body to a temp file while hashing, enforces the tenant capacity cap mid-stream,
// verifies the sha256 matches the oid, then atomically publishes it and records the
// ledger row. A re-upload of a present object is a no-op 200 (dedup).
func (c config) handleLFSUpload(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg, oid := r.PathValue("slug"), r.PathValue("repo"), r.PathValue("oid")
	name, mv, _, aerr := c.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	if !canPush(mv.Role) {
		writeLFSErr(w, http.StatusForbidden, "read-only: push not permitted for this role")
		return
	}
	if !validOID(oid) {
		writeLFSErr(w, http.StatusUnprocessableEntity, "invalid oid")
		return
	}
	dest := c.lfsObjectPath(slug, name, oid)
	if fileExists(dest) {
		w.WriteHeader(http.StatusOK) // already have it
		return
	}

	// Remaining quota (-1 = unlimited). Checked upfront against Content-Length for a
	// fast reject, and enforced hard mid-stream below.
	remaining := int64(-1)
	t, err := c.mgr.store.GetTenant(r.Context(), mv.TenantID)
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if max := parseLimits(t.Limits).MaxLFSBytes; max > 0 {
		used, err := c.mgr.store.TenantLFSBytes(r.Context(), mv.TenantID)
		if err != nil {
			writeLFSErr(w, http.StatusInternalServerError, "store error")
			return
		}
		remaining = max - used
		if r.ContentLength > 0 && r.ContentLength > remaining {
			writeLFSErr(w, http.StatusInsufficientStorage, "tenant LFS quota exceeded")
			return
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".upload-*")
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "storage error")
		return
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }

	h := sha256.New()
	written, cerr := copyCapped(io.MultiWriter(tmp, h), r.Body, remaining)
	if cerr == errQuotaExceeded {
		cleanup()
		writeLFSErr(w, http.StatusInsufficientStorage, "tenant LFS quota exceeded")
		return
	}
	if cerr != nil {
		cleanup()
		writeLFSErr(w, http.StatusInternalServerError, "write failed")
		return
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != oid {
		cleanup()
		writeLFSErr(w, http.StatusUnprocessableEntity, "oid mismatch (content hash differs)")
		return
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		writeLFSErr(w, http.StatusInternalServerError, "write failed")
		return
	}
	tmp.Close()
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		writeLFSErr(w, http.StatusInternalServerError, "publish failed")
		return
	}
	if err := c.mgr.store.PutLFSObject(r.Context(), mv.TenantID, name, oid, written); err != nil {
		// The object is stored; a ledger miss only under-counts the quota. Log-worthy
		// but not client-facing.
		_ = err
	}
	w.WriteHeader(http.StatusOK)
}

// handleLFSDownload serves an object (GET .../info/lfs/objects/{oid}) with range
// support via http.ServeContent.
func (c config) handleLFSDownload(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg, oid := r.PathValue("slug"), r.PathValue("repo"), r.PathValue("oid")
	name, _, _, aerr := c.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	if !validOID(oid) {
		writeLFSErr(w, http.StatusUnprocessableEntity, "invalid oid")
		return
	}
	f, err := os.Open(c.lfsObjectPath(slug, name, oid))
	if err != nil {
		writeLFSErr(w, http.StatusNotFound, "object does not exist")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "stat error")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, oid, fi.ModTime(), f)
}

// handleLFSLocks answers the LFS locking API with 501: locking is out of scope for
// the internal provider. git-lfs treats this as "not supported" and continues, so
// push/pull are unaffected.
func (c config) handleLFSLocks(w http.ResponseWriter, r *http.Request) {
	writeLFSErr(w, http.StatusNotImplemented, "file locking is not supported")
}

// copyCapped copies src→dst. With cap<0 it copies everything; with cap>=0 it copies
// at most cap bytes and returns errQuotaExceeded if the source has more (so an
// over-quota upload is rejected without buffering it all in memory).
func copyCapped(dst io.Writer, src io.Reader, cap int64) (int64, error) {
	if cap < 0 {
		return io.Copy(dst, src)
	}
	n, err := io.Copy(dst, io.LimitReader(src, cap))
	if err != nil {
		return n, err
	}
	var probe [1]byte
	if m, _ := src.Read(probe[:]); m > 0 {
		return n, errQuotaExceeded
	}
	return n, nil
}

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Pasted-image support. A claude session driven over tmux can't receive an image through
// the terminal's paste buffer (send-keys is text only), so the Console uploads a pasted
// image here; we save it under the session's own dir and return its absolute path. The
// Console then references that path in the prompt and claude opens it with the Read tool
// (which reads images) — there's no native CLI image-input for a headless session.

// pastedDir is where a session's pasted images live — keyed by sid so they stay
// associated with the session (and survive across turns for later reference / preview).
func pastedDir(sid string) string {
	return filepath.Join(homeDir(), ".cache", "agent-fleet", "pasted", sid)
}

// imageExt maps a content-type (preferred) or filename to a supported image extension,
// and reports whether it's an accepted image type.
func imageExt(contentType, filename string) (string, bool) {
	switch {
	case strings.HasPrefix(contentType, "image/png"):
		return ".png", true
	case strings.HasPrefix(contentType, "image/jpeg"):
		return ".jpg", true
	case strings.HasPrefix(contentType, "image/gif"):
		return ".gif", true
	case strings.HasPrefix(contentType, "image/webp"):
		return ".webp", true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png":
		return ".png", true
	case ".jpg", ".jpeg":
		return ".jpg", true
	case ".gif":
		return ".gif", true
	case ".webp":
		return ".webp", true
	}
	return "", false
}

func imageMime(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// handlePasteImage saves one pasted image (multipart, field "file") under the session's
// pasted dir and returns {path, name}. The path is absolute so the prompt can reference
// it and claude's Read tool can open it regardless of cwd.
func handlePasteImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	if !agentOf(meta.Kind).caps().canTranscript {
		writeErr(w, http.StatusBadRequest, "not_claude", "画像を渡せるのは claude セッションのみです")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_form", "expected multipart/form-data")
		return
	}
	max := maxUploadBytes()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_part", err.Error())
			return
		}
		if part.FormName() != "file" {
			continue
		}
		ext, ok := imageExt(part.Header.Get("Content-Type"), part.FileName())
		if !ok {
			writeErr(w, http.StatusUnsupportedMediaType, "not_image", "画像ファイルのみ対応しています")
			return
		}
		sid := sessionUUID(meta.Dir, name)
		dir := pastedDir(sid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
			return
		}
		tmp, err := os.CreateTemp(dir, ".paste-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		n, err := io.Copy(tmp, io.LimitReader(part, max+1))
		_ = tmp.Close()
		if err != nil || n > max {
			_ = os.Remove(tmp.Name())
			if n > max {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "画像が大きすぎます")
			} else {
				writeErr(w, http.StatusInternalServerError, "write_failed", "upload failed")
			}
			return
		}
		fname := fmt.Sprintf("paste-%d%s", time.Now().UnixNano(), ext)
		dest := filepath.Join(dir, fname)
		if err := os.Rename(tmp.Name(), dest); err != nil {
			_ = os.Remove(tmp.Name())
			writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"path": dest, "name": fname})
		return
	}
	writeErr(w, http.StatusBadRequest, "no_file", "no file part")
}

// handlePastedImage serves a previously-pasted image by basename (GET), for the Console's
// in-chat thumbnail / preview. The name is reduced to its base and must be a known image.
func handlePastedImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	file := r.PathValue("file")
	meta, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	base := filepath.Base(file)
	if base != file || base == "." || base == ".." {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid file")
		return
	}
	ext, ok := imageExt("", base)
	if !ok {
		writeErr(w, http.StatusBadRequest, "not_image", "not an image")
		return
	}
	sid := sessionUUID(meta.Dir, name)
	b, err := os.ReadFile(filepath.Join(pastedDir(sid), base))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such image")
		return
	}
	w.Header().Set("Content-Type", imageMime(ext))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

package sessionx

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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

// SavePastedImageTo reads the "file" multipart part and stores it under dir, writing the
// {path, name} response (201) or an error. Shared by the session and chat paste endpoints
// — only the target dir (and the caller's own validation) differ. Any file type is
// accepted (drag&drop / the ＋ picker attach logs, PDFs, sources, …): an image keeps the
// bare paste-<n>.<ext> form (the bubble thumbnails key off it), any other file carries a
// sanitized copy of its original name so the agent sees a meaningful filename.
func SavePastedImageTo(w http.ResponseWriter, r *http.Request, dir string) {
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_form", "expected multipart/form-data")
		return
	}
	max := maxUploadBytes()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_part", err.Error())
			return
		}
		if part.FormName() != "file" {
			continue
		}
		suffix := ""
		if ext, ok := imageExt(part.Header.Get("Content-Type"), part.FileName()); ok {
			suffix = ext
		} else {
			suffix = "-" + sanitizeUploadName(part.FileName())
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
			return
		}
		tmp, err := os.CreateTemp(dir, ".paste-*")
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		n, err := io.Copy(tmp, io.LimitReader(part, max+1))
		_ = tmp.Close()
		if err != nil || n > max {
			_ = os.Remove(tmp.Name())
			if n > max {
				httpx.WriteErr(w, http.StatusRequestEntityTooLarge, errCodePasteTooLarge, "file is too large")
			} else {
				httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", "upload failed")
			}
			return
		}
		fname := fmt.Sprintf("paste-%d%s", time.Now().UnixNano(), suffix)
		dest := filepath.Join(dir, fname)
		if err := os.Rename(tmp.Name(), dest); err != nil {
			_ = os.Remove(tmp.Name())
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"path": dest, "name": fname})
		return
	}
	httpx.WriteErr(w, http.StatusBadRequest, "no_file", "no file part")
}

// sanitizeUploadName reduces an uploaded file's client-supplied name to a safe basename
// fragment for the paste-<n>-<name> form: base only, [A-Za-z0-9._-] kept (runs of
// anything else collapse to "-"), no leading dots, capped at 48 runes. The result is
// display/meaning only — uniqueness comes from the paste-<n> prefix.
func sanitizeUploadName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	dash := false
	for _, r := range base {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteRune('-')
			dash = true
		}
	}
	s := strings.TrimLeft(b.String(), ".-")
	if r := []rune(s); len(r) > 48 {
		s = string(r[len(r)-48:]) // keep the tail — the extension matters most
		s = strings.TrimLeft(s, ".-")
	}
	if s == "" {
		return "file.bin"
	}
	return s
}

// ServePastedImageFrom serves a stored image by basename (GET) from dir, for the Console's
// thumbnail / preview. The name is reduced to its base and must be a known image type.
func ServePastedImageFrom(w http.ResponseWriter, dir, file string) {
	base := filepath.Base(file)
	if base != file || base == "." || base == ".." {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid file")
		return
	}
	ext, ok := imageExt("", base)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "not_image", "not an image")
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, base))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such image")
		return
	}
	w.Header().Set("Content-Type", imageMime(ext))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// HandlePasteImage saves one pasted image (multipart, field "file") under the session's
// pasted dir and returns {path, name}. The path is absolute so the prompt can reference
// it and the agent can open it regardless of cwd (claude: Read tool / codex: view_image /
// opencode: its own tools — vision is model-dependent there).
func HandlePasteImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	if !AgentOf(meta.Kind).Caps().CanTranscript {
		httpx.WriteErr(w, http.StatusBadRequest, errCodePasteUnsupportedKind, "this session type cannot accept images")
		return
	}
	SavePastedImageTo(w, r, pastedDir(session.UUID(meta.Dir, name)))
}

// HandlePastedImage serves a previously-pasted session image by basename (GET).
func HandlePastedImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	ServePastedImageFrom(w, pastedDir(session.UUID(meta.Dir, name)), r.PathValue("file"))
}

// chatPastedDir is where an assistant chat's pasted images live — keyed by conversation
// id (namespaced so it can't collide with a tmux session's sid).
func chatPastedDir(convID string) string { return pastedDir("chat-" + convID) }

// HandleChatPasteImage saves a pasted image for an assistant chat (docs/log/19). Same flow as
// the session endpoint: the chat's headless agent opens the returned absolute path —
// claude via its Read tool (`-p`), codex via view_image (`codex exec`, live-verified).
// opencode is excluded on purpose: `opencode run` declines image input on non-vision
// models (big-pickle, live-verified), and the chat can't know the model sees images.
func HandleChatPasteImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := chatx.LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "no_conversation", "conversation not found: "+id)
		return
	}
	if c.Agent != session.KindClaude && c.Agent != session.KindCodex {
		httpx.WriteErr(w, http.StatusBadRequest, errCodePasteUnsupportedAgent, "only claude / codex assistants can accept images")
		return
	}
	SavePastedImageTo(w, r, chatPastedDir(id))
}

// HandleChatPastedImage serves a previously-pasted chat image by basename (GET).
func HandleChatPastedImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_id", "invalid conversation id")
		return
	}
	ServePastedImageFrom(w, chatPastedDir(id), r.PathValue("file"))
}

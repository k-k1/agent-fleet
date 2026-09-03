package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Memo image attachments (docs/log/21 画像添付). A memo is membership-scoped and syncs across
// devices — an image shared from an Android phone's 共有シート, or dragged into the memo
// composer, is uploaded here and later flushed into whichever session the user picks.
// Because the memo (and its images) aren't tied to a session or conversation, they live
// in a single per-container dir rather than under a session/conv key. Storage, serving
// and size limits reuse the paste-image helpers (session_paste.go); the CP flush composer
// embeds each image's absolute path so the target agent opens it with its Read tool,
// exactly like the session/chat paste flow.

// memoImagesDir is where a membership's memo images live in the workspace container.
func memoImagesDir() string {
	return filepath.Join(homeDir(), ".cache", "agent-fleet", "memo-images")
}

// handleMemoPasteImage saves one uploaded memo image (multipart, field "file") and
// returns {path, name}. The path is absolute so a later flush can reference it.
func handleMemoPasteImage(w http.ResponseWriter, r *http.Request) {
	sessionx.SavePastedImageTo(w, r, memoImagesDir())
}

// handleMemoPastedImage serves a stored memo image by basename (GET) for the Console
// thumbnail / preview.
func handleMemoPastedImage(w http.ResponseWriter, r *http.Request) {
	sessionx.ServePastedImageFrom(w, memoImagesDir(), r.PathValue("file"))
}

// handleMemoImageGC prunes memo images no longer referenced by any memo. The Console —
// which holds the full memo list — POSTs the basenames still in use ({keep:[...]}) and
// the agent unlinks every other paste-* file in the dir. Best-effort disk hygiene: the
// bytes are in ~/.cache, so a miss just leaves a stale file until the next sweep. Only
// paste-* files are ever removed (the in-flight ".paste-*" temp files are left alone).
func handleMemoImageGC(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keep []string `json:"keep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	keep := make(map[string]bool, len(in.Keep))
	for _, k := range in.Keep {
		keep[filepath.Base(k)] = true
	}
	dir := memoImagesDir()
	entries, _ := os.ReadDir(dir)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "paste-") || keep[name] {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

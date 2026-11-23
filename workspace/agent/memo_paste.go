package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Memo image attachments (docs/log/21 image attachments). A memo is membership-scoped and syncs across
// devices — an image shared from an Android phone's share sheet, or dragged into the memo
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

// memoImageGCGrace is how long a stored memo image is spared by the sweep even when no
// keep list names it. The keep list cannot name an upload whose response has not reached
// its client yet (the name is chosen here, at store time), nor one held by another tab's or
// device's composer, so without this a sweep racing a paste deletes a file a composer is
// about to show. The cost is hygiene only: a memo deleted within this window of its upload
// leaves its file for a later sweep.
const memoImageGCGrace = 10 * time.Minute

// handleMemoImageGC prunes memo images no longer referenced by any memo. The Console —
// which holds the full memo list — POSTs the basenames still in use ({keep:[...]}) and
// the agent unlinks every other paste-* file in the dir. Best-effort disk hygiene: the
// bytes are in ~/.cache, so a miss just leaves a stale file until the next sweep. Only
// paste-* files are ever removed (the in-flight ".paste-*" temp files are left alone), and
// none younger than memoImageGCGrace.
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
	cutoff := time.Now().Add(-memoImageGCGrace)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "paste-") || keep[name] {
			continue
		}
		// An unreadable mtime counts as young: when in doubt, keep.
		if info, err := e.Info(); err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

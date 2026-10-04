package main

// Every picture under a folder, several levels down, as one flat list (GET /fs/images,
// ADR 0080 decision 9's dedicated endpoint, built for the gallery's "include subfolders").
//
// Not api/fs/search: that honours .gitignore, so generated and built pictures vanish silently,
// and its query is required. Not the Console walking fs/tree either: that is one round trip per
// folder, which decision 2 refused for a single level already.
//
// A walk is the expensive shape a listing must never take unbounded, so it is fenced on every
// axis at once — depth, folders read, files looked at, wall time, and entries returned — and
// past any of them the answer is simply the newest of what was seen, marked `truncated`.
//
// The walk never leaves the folder asked for: it is gated exactly like fs/tree (safeBrowsePath,
// the codex store, fsQueryResolvedOK for a symlinked start), every child is checked against the
// denylist by its browse-relative path, and a symlink — to a file or a folder — is skipped
// rather than followed. os.ReadDir reports a symlink's own type, so a link to ~/.ssh is never
// descended into, and a link to a picture outside would only be refused by /fs/download's
// openat2 later; listing it would draw a broken card.

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/filemeta"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	// Levels below the folder asked for. 1 is "this folder and its subfolders".
	imagesDefaultDepth = 3
	imagesMaxDepth     = 6
	// Entries returned. The gallery's page is 300 and "show more" adds 300; past a few pages a
	// flat grid stops being something anyone scrolls.
	imagesDefaultLimit = 1000
	imagesMaxLimit     = 2000
	// What one walk may read at most, whatever was asked. These are what keep "flatten my home
	// folder" a bounded request on a shared host.
	imagesMaxDirs   = 400
	imagesMaxFiles  = 20000
	imagesWalkLimit = 500 * time.Millisecond
	// Pictures warmed for `warm=`: the newest, i.e. the gallery's first screens. The same reason
	// as warmThumbLimit, at a smaller number because these are spread over many folders.
	imagesWarmLimit = 120
)

type fsImagesResponse struct {
	Path string `json:"path"`
	// Entries are files only, newest first. Name is the path RELATIVE TO THE FOLDER ASKED FOR
	// ("sub/a.png"), so a reader that joins folder + "/" + name — which is what a gallery does
	// with an fs/tree entry — gets the browse-relative path without knowing it was flattened.
	Entries []fsEntry `json:"entries"`
	// Truncated says some pictures were left out: a bound was hit, or there were more than
	// `limit`. The reader says so rather than presenting a partial set as the whole folder.
	Truncated bool `json:"truncated,omitempty"`
}

// boundedQueryInt reads an optional integer query value; absent or unparseable is def, and
// anything outside 1..max is clamped rather than refused (advisory, like thumb).
func boundedQueryInt(raw string, def, max int) int {
	n, err := strconv.Atoi(raw)
	if raw == "" || err != nil {
		return def
	}
	if n < 1 {
		return 1
	}
	if n > max {
		return max
	}
	return n
}

func handleFSImages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	full, rel, ok := safeBrowsePath(q)
	if !ok || isCodexGeneratedImagesPath(full) || !fsQueryResolvedOK(q, full) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		httpx.WriteErr(w, http.StatusNotFound, "not_dir", "cannot list: "+rel)
		return
	}
	depth := boundedQueryInt(r.URL.Query().Get("depth"), imagesDefaultDepth, imagesMaxDepth)
	limit := boundedQueryInt(r.URL.Query().Get("limit"), imagesDefaultLimit, imagesMaxLimit)

	found, truncated := walkImages(full, rel, depth, time.Now().Add(imagesWalkLimit))
	sort.Slice(found, func(i, j int) bool {
		if found[i].Mtime != found[j].Mtime {
			return found[i].Mtime > found[j].Mtime
		}
		return found[i].Name < found[j].Name
	})
	if len(found) > limit {
		found, truncated = found[:limit], true
	}
	if edge := thumbEdge(r.URL.Query().Get("warm")); edge > 0 && len(found) > 0 {
		n := min(len(found), imagesWarmLimit)
		paths := make([]string, n)
		for i := range paths {
			paths[i] = filepath.Join(full, filepath.FromSlash(found[i].Name))
		}
		go warmThumbList(paths, edge)
	}
	httpx.WriteJSON(w, http.StatusOK, fsImagesResponse{Path: rel, Entries: found, Truncated: truncated})
}

// walkImages lists the pictures under full, breadth first so the shallow folders — the ones a
// reader asked about most directly — are the ones read when a bound cuts the walk short.
func walkImages(full, rel string, depth int, deadline time.Time) ([]fsEntry, bool) {
	type dir struct {
		full, rel, sub string // sub: relative to the folder asked for ("" for itself)
		level          int
	}
	queue := []dir{{full: full, rel: rel}}
	out := []fsEntry{}
	dirs, files := 0, 0
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if dirs >= imagesMaxDirs || time.Now().After(deadline) {
			return out, true
		}
		dirs++
		ents, err := os.ReadDir(d.full)
		if err != nil {
			continue // a folder that vanished or is unreadable mid-walk is just not listed
		}
		for _, e := range ents {
			if files >= imagesMaxFiles {
				return out, true
			}
			files++
			childRel := filepath.Join(d.rel, e.Name())
			if isDenied(childRel) || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			sub := e.Name()
			if d.sub != "" {
				sub = d.sub + "/" + e.Name()
			}
			if e.IsDir() {
				// Hidden folders are skipped below the start: .git holds thousands of objects
				// and no pictures, and a walk that spends its budget there answers nothing. A
				// hidden folder asked for directly (.cache/agent-fleet/generated) is the start,
				// and is walked.
				if d.level+1 > depth || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				childFull := filepath.Join(d.full, e.Name())
				if isCodexGeneratedImagesPath(childFull) {
					continue
				}
				queue = append(queue, dir{full: childFull, rel: childRel, sub: sub, level: d.level + 1})
				continue
			}
			if filemeta.ImageContentType(e.Name()) == "" {
				continue
			}
			fe := fsEntry{Name: sub, Type: "file"}
			if fi, err := e.Info(); err == nil {
				fe.Size = fi.Size()
				fe.Mtime = fi.ModTime().Unix()
			}
			out = append(out, fe)
		}
	}
	return out, false
}

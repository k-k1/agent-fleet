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
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/filemeta"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/pathguard"
)

const (
	// Levels below the folder asked for. 1 is "this folder and its subfolders".
	imagesDefaultDepth = 3
	imagesMaxDepth     = 6
	// Entries returned. The gallery's page is 300 and "show more" adds 300; past a few pages a
	// flat grid stops being something anyone scrolls.
	imagesDefaultLimit = 1000
	imagesMaxLimit     = 2000
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

// The walk's bounds and its shared slots. Variables rather than constants so a test can make a
// bound small enough to hit without writing tens of thousands of files.
var (
	imagesMaxDirs   = 400
	imagesMaxFiles  = 20000
	imagesWalkLimit = 500 * time.Millisecond
	// Directory entries read per batch. os.ReadDir would read (and sort) a whole folder before
	// any bound could apply — a million-file folder is a million entries in memory — so a
	// folder is read in batches no larger than what is left of imagesMaxFiles.
	imagesReadBatch = 256
	// Walks running at once, Agent-wide. Each is bounded, but a burst of requests (or ones the
	// reader abandoned) would otherwise multiply that bound on a shared host. A request that
	// cannot get a slot within imagesSlotWait answers 503, which the gallery retries.
	imagesWalkSlots = make(chan struct{}, 2)
	imagesSlotWait  = 2 * time.Second
	// One warm-up at a time: each would start its own two workers, and a gallery refreshing
	// every 20 seconds would stack them.
	imagesWarming atomic.Bool
	// imagesReadDir is File.ReadDir, a seam so a test can see the batch sizes asked for.
	imagesReadDir = func(f *os.File, n int) ([]os.DirEntry, error) { return f.ReadDir(n) }
)

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

	select {
	case imagesWalkSlots <- struct{}{}:
		defer func() { <-imagesWalkSlots }()
	case <-r.Context().Done():
		return
	case <-time.After(imagesSlotWait):
		httpx.WriteErr(w, http.StatusServiceUnavailable, "busy", "too many folder walks at once")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), imagesWalkLimit)
	defer cancel()
	found, truncated := walkImages(ctx, full, rel, imagesDenyBase(full), depth)
	sort.Slice(found, func(i, j int) bool {
		if found[i].Mtime != found[j].Mtime {
			return found[i].Mtime > found[j].Mtime
		}
		return found[i].Name < found[j].Name
	})
	if len(found) > limit {
		found, truncated = found[:limit], true
	}
	if edge := thumbEdge(r.URL.Query().Get("warm")); edge > 0 && len(found) > 0 && imagesWarming.CompareAndSwap(false, true) {
		n := min(len(found), imagesWarmLimit)
		paths := make([]string, n)
		for i := range paths {
			paths[i] = filepath.Join(full, filepath.FromSlash(found[i].Name))
		}
		go func() {
			defer imagesWarming.Store(false)
			warmThumbList(paths, edge)
		}()
	}
	httpx.WriteJSON(w, http.StatusOK, fsImagesResponse{Path: rel, Entries: found, Truncated: truncated})
}

// imagesDenyBase is the start's browse-relative path AFTER symlink resolution, which is what the
// denylist has to be checked against below it: a start reached through an alias
// (alias -> .local/share) spells its children "alias/agent-fleet/…", which no denylist entry
// matches, while the files are those of .local/share/agent-fleet. nil means the start is not
// under the browse root at all (an absolute scratch or docs root fsQueryResolvedOK admitted),
// where no denylist applies.
func imagesDenyBase(full string) *string {
	rel, ok := pathguard.ResolveUnder(full, browseRoot())
	if !ok {
		return nil
	}
	if rel == "." {
		rel = ""
	}
	rel = filepath.ToSlash(rel)
	return &rel
}

// walkImages lists the pictures under full, breadth first so the shallow folders — the ones a
// reader asked about most directly — are the ones read when a bound cuts the walk short. It
// stops, marking the answer truncated, when ctx ends (the time budget, or the reader gone).
//
// rel is the start as the request spelled it (the display side); denyBase is the start as it
// resolves (the denylist side, see imagesDenyBase). Both are extended in step below it, and
// a child is skipped when EITHER spelling is denied.
func walkImages(ctx context.Context, full, rel string, denyBase *string, depth int) ([]fsEntry, bool) {
	type dir struct {
		full, rel, sub string // sub: relative to the folder asked for ("" for itself)
		canon          string // the resolved browse-relative path, when denyBase is set
		level          int
	}
	start := dir{full: full, rel: rel}
	if denyBase != nil {
		start.canon = *denyBase
	}
	queue := []dir{start}
	out := []fsEntry{}
	dirs, files := 0, 0
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if dirs >= imagesMaxDirs || ctx.Err() != nil {
			return out, true
		}
		dirs++
		f, err := os.Open(d.full)
		if err != nil {
			continue // a folder that vanished or is unreadable mid-walk is just not listed
		}
		for {
			if files >= imagesMaxFiles || ctx.Err() != nil {
				f.Close()
				return out, true
			}
			ents, err := imagesReadDir(f, min(imagesReadBatch, imagesMaxFiles-files))
			for _, e := range ents {
				files++
				childRel := filepath.Join(d.rel, e.Name())
				childCanon := path.Join(d.canon, e.Name())
				if isDenied(childRel) || (denyBase != nil && isDenied(childCanon)) || e.Type()&fs.ModeSymlink != 0 {
					continue
				}
				sub := e.Name()
				if d.sub != "" {
					sub = d.sub + "/" + e.Name()
				}
				if e.IsDir() {
					// Hidden folders are skipped below the start: .git holds thousands of
					// objects and no pictures, and a walk that spends its budget there answers
					// nothing. A hidden folder asked for directly (.cache/agent-fleet/generated)
					// is the start, and is walked.
					if d.level+1 > depth || strings.HasPrefix(e.Name(), ".") {
						continue
					}
					childFull := filepath.Join(d.full, e.Name())
					if isCodexGeneratedImagesPath(childFull) {
						continue
					}
					queue = append(queue, dir{full: childFull, rel: childRel, sub: sub, canon: childCanon, level: d.level + 1})
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
			if err != nil || len(ents) == 0 {
				break // io.EOF ends the folder; any other error just stops reading it
			}
		}
		f.Close()
	}
	return out, false
}

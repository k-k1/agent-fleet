package main

// The disk side of cleanup: how much the Agent's own leftovers take, and the one delete
// that reclaims cache directories nothing can reach any more.
//
//   GET    /cleanup/usage            → the breakdown the Settings "Machine" tab shows
//   DELETE /cleanup/cache/{feature}  → delete_cache, the action of a "cache" survey row
//
// Why a breakdown and not the home disk figure: that figure is a statfs of the filesystem
// home sits on, which is the whole host (other members included) everywhere but a
// dedicated box. A cleanup button next to it would promise to move a number it barely
// touches. These are the bytes a cleanup can actually give back.
//
// The walk is a du, so it is on demand only — never on the 4-second stats path, which on
// EFS would be ADR 0087's metadata storm again — and held for a short while so a tab
// switched back and forth does not walk twice.

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// handleDeleteCacheOrphans (DELETE /cleanup/cache/{feature}) deletes the unreachable
// directories of one cache subtree. The scan runs again here rather than trusting the
// survey the Console showed: a restore in between makes a directory reachable again.
//
// No gz archive, unlike every other delete in the cleanup family: the directories belong
// to sessions that are already gone for good (not on the shelf, not in the trash), so there
// is nothing a restore could reattach them to.
func handleDeleteCacheOrphans(w http.ResponseWriter, r *http.Request) {
	feature := r.PathValue("feature")
	valid := false
	for _, f := range sessionx.CacheOrphanFeatures {
		valid = valid || f == feature
	}
	if !valid {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_feature", "unknown cache: "+feature)
		return
	}
	removed, err := sessionx.RemoveCacheOrphans(feature, time.Now())
	if errors.Is(err, sessionx.ErrCacheScanUnsafe) {
		httpx.WriteErr(w, http.StatusConflict, "cache_scan_unsafe", err.Error())
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	invalidateCleanupUsage()
	httpx.WriteJSON(w, http.StatusOK, cacheDeleteResult{
		Feature: feature, Dirs: len(removed.Dirs), Files: removed.Files, Bytes: removed.Bytes,
		Truncated: removed.Truncated, Unreadable: removed.Unreadable,
		Stalled: removed.Stalled, Stuck: removed.Stuck,
	})
}

// cacheDeleteResult is what a delete_cache reclaimed. Truncated = the scan stopped at its
// budget, so only part was taken and the next survey lists the rest; Unreadable = folders
// left out because something inside could not be read.
type cacheDeleteResult struct {
	Feature    string `json:"feature"`
	Dirs       int    `json:"dirs"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	Truncated  bool   `json:"truncated,omitempty"`
	Unreadable int    `json:"unreadable,omitempty"`
	// Stalled / Stuck: nothing could be taken, and pressing again will not change that —
	// said here so a 200 with zero is not read as "nothing left".
	Stalled bool `json:"stalled,omitempty"`
	Stuck   bool `json:"stuck,omitempty"`
}

type usagePart struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// usageOrphans is the part of the cache a delete_cache can take. OK false = the scan could
// not prove reachability (sessionx.ErrCacheScanUnsafe) and nothing is offered.
type usageOrphans struct {
	OK    bool  `json:"ok"`
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
	Dirs  int   `json:"dirs"`
}

type cleanupUsage struct {
	Cache struct {
		Bytes int64       `json:"bytes"`
		Files int         `json:"files"`
		Parts []usagePart `json:"parts"`
	} `json:"cache"`
	Orphans usageOrphans `json:"orphans"`
	Trash   struct {
		Bytes    int64 `json:"bytes"`
		Archives int   `json:"archives"`
	} `json:"trash"`
	// Truncated = the walk hit its entry cap; the figures are lower bounds.
	Truncated  bool   `json:"truncated,omitempty"`
	MeasuredAt string `json:"measured_at"`
}

const (
	cleanupUsageTTL = 30 * time.Second
	// A cap on the walk. The measured cache is ~9k files; this is two orders of magnitude
	// above, so it only bites on something pathological, where a slow answer is worse than
	// a lower bound.
	cleanupUsageMaxEntries = 500_000
)

var cleanupUsageCache struct {
	mu  sync.Mutex
	at  time.Time
	val *cleanupUsage
}

// invalidateCleanupUsage drops the held answer after anything that changes it, so the tab
// reopened after a cleanup shows the new figure rather than the one from before.
func invalidateCleanupUsage() {
	cleanupUsageCache.mu.Lock()
	cleanupUsageCache.val = nil
	cleanupUsageCache.mu.Unlock()
}

// handleCleanupUsage (GET /cleanup/usage).
func handleCleanupUsage(w http.ResponseWriter, r *http.Request) {
	cleanupUsageCache.mu.Lock()
	defer cleanupUsageCache.mu.Unlock()
	now := time.Now()
	if cleanupUsageCache.val == nil || now.Sub(cleanupUsageCache.at) > cleanupUsageTTL {
		cleanupUsageCache.val = measureCleanupUsage(now)
		cleanupUsageCache.at = now
	}
	httpx.WriteJSON(w, http.StatusOK, cleanupUsageCache.val)
}

func measureCleanupUsage(now time.Time) *cleanupUsage {
	u := &cleanupUsage{MeasuredAt: now.UTC().Format(time.RFC3339)}
	budget := cleanupUsageMaxEntries

	root := sessionx.CacheRoot()
	ents, _ := os.ReadDir(root)
	var loose usagePart
	for _, e := range ents {
		p := filepath.Join(root, e.Name())
		if !e.IsDir() {
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
				loose.Bytes += info.Size()
				loose.Files++
			}
			continue
		}
		part := usagePart{Name: e.Name()}
		part.Bytes, part.Files = walkSize(p, &budget)
		u.Cache.Parts = append(u.Cache.Parts, part)
	}
	if loose.Files > 0 {
		// Files directly under the root belong to no feature; "" is the Console's "other".
		u.Cache.Parts = append(u.Cache.Parts, loose)
	}
	sort.Slice(u.Cache.Parts, func(i, j int) bool { return u.Cache.Parts[i].Bytes > u.Cache.Parts[j].Bytes })
	for _, p := range u.Cache.Parts {
		u.Cache.Bytes += p.Bytes
		u.Cache.Files += p.Files
	}

	// Each orphan scan gets the same budget a delete would, not what the walk above left over:
	// a large cache would otherwise leave nothing for reachability and show "can't tell" for
	// a figure the cleanup itself can compute. Each is bounded on its own, and so is the time
	// it holds the cleanup lock. A feature that cannot be judged makes the figure unknown
	// rather than a lower bound mixed with a zero.
	u.Orphans.OK = true
	for _, feature := range sessionx.CacheOrphanFeatures {
		found, err := sessionx.ScanCacheOrphans(feature, now, nil)
		if err != nil || found.Stalled || found.Stuck {
			u.Orphans = usageOrphans{}
			break
		}
		u.Orphans.Bytes += found.Bytes
		u.Orphans.Files += found.Files
		u.Orphans.Dirs += len(found.Dirs)
		// Session folders left unjudged (no session store) make the figure a lower bound too.
		u.Truncated = u.Truncated || found.Truncated || found.Unjudged > 0
	}

	tents, _ := os.ReadDir(cleanupStoreDir())
	for _, e := range tents {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			u.Trash.Bytes += info.Size()
		}
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			u.Trash.Archives++
		}
	}
	u.Truncated = u.Truncated || budget <= 0
	return u
}

// walkSize sums the regular files under dir, spending one unit of budget per entry and
// stopping when it runs out.
func walkSize(dir string, budget *int) (bytes int64, files int) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if *budget <= 0 {
			return filepath.SkipAll
		}
		*budget--
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return bytes, files
}

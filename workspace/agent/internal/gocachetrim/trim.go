// Package gocachetrim trims the Go build cache by last use when home runs low (#4560,
// docs/log/137).
//
// Go's own trim keeps an entry for 5 days after its last use; with several sessions building
// different branches, test binaries and -race builds, that window holds more than home can
// (measured: 162 GB in a 466 GB home). Go refreshes an entry's mtime whenever it uses it (at
// most hourly), so mtime is the "last used" signal. Below the free-space threshold this deletes
// entries unused for longer than minAge, oldest first, until free space is back above
// the threshold plus a margin. A build that needs a deleted entry rebuilds it: the cost is time.
//
// Only files inside the cache's two-hex-digit subdirectories are touched. The top-level files
// (README, trim.txt) are Go's own bookkeeping, and an entry newer than the minimum age is never
// removed even when the target is not reached: that is what a running build is using.
// The switch is ui-prefs goCacheAutoTrim (default on, uiprefs.GoCacheAutoTrim).
package gocachetrim

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	// Trim when free space is under 15% of the volume (about 70 GB of a 466 GB home) and stop
	// at 20%. The notice that tells the member home is low raises at 90% used or 2 GiB free
	// (HomeLowNotice), so this acts well before it. The measured inflow was about 15 GB/day, so
	// the 5% margin (about 23 GB) is more than a day of builds.
	belowFrac = 0.15
	toFrac    = 0.20
	// Entries used within the last day stay (the owner's decision on #4560).
	minAge = 24 * time.Hour
	// How often free space is read (one statfs), and the pause after a pass that could not
	// reach the target, so a cache with nothing old is not walked every few minutes.
	checkEvery = 10 * time.Minute
	retryAfter = time.Hour
)

// Result is the last automatic trim, for the log and the tool-caches survey.
type Result struct {
	At    string `json:"at"` // RFC 3339
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

var last = struct {
	mu   sync.Mutex
	last *Result
}{}

// Last is the last automatic trim since the Agent started, nil when none ran.
func Last() *Result {
	last.mu.Lock()
	defer last.mu.Unlock()
	if last.last == nil {
		return nil
	}
	c := *last.last
	return &c
}

// trimmer holds what a pass reads from the outside, so a test can fake it.
type trimmer struct {
	dir     func() string
	free    func() (free, total uint64, ok bool)
	enabled func() bool
	now     func() time.Time
}

func newTrimmer(dir func() string) trimmer {
	return trimmer{
		dir:     dir,
		free:    homeFree,
		enabled: uiprefs.GoCacheAutoTrim,
		now:     time.Now,
	}
}

// homeFree is the space an ordinary user can still use on the volume home sits on.
func homeFree() (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(paths.HomeDir(), &st); err != nil || st.Bsize <= 0 || st.Blocks == 0 {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, true
}

// sameVolumeFn is sameVolume; a var so a test can say the cache is elsewhere.
var sameVolumeFn = sameVolume

// sameVolume reports whether a and b are on one filesystem. With $AF_WS_SCRATCH the cache is a
// link into a disk that is not home: trimming it would free nothing where space is short.
func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

type goCacheEntry struct {
	path string
	size int64
	mod  time.Time
}

// pass runs one check and, when home is low, one trim. done=false means it could not reach the
// target (nothing old enough, or an error), so the caller waits longer before trying again.
func (g trimmer) pass() (res Result, trimmed, done bool) {
	if !g.enabled() {
		return res, false, true
	}
	free, total, ok := g.free()
	if !ok || float64(free) >= belowFrac*float64(total) {
		return res, false, true
	}
	dir := g.dir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !sameVolumeFn(real, paths.HomeDir()) {
		return res, false, true
	}
	need := int64(toFrac*float64(total)) - int64(free)
	cutoff := g.now().Add(-minAge)

	var old []goCacheEntry
	tops, _ := os.ReadDir(real)
	for _, top := range tops {
		if !top.IsDir() || !isHex2(top.Name()) {
			continue // README, trim.txt, anything Go did not put in an entry directory
		}
		files, _ := os.ReadDir(filepath.Join(real, top.Name()))
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
				continue
			}
			old = append(old, goCacheEntry{filepath.Join(real, top.Name(), f.Name()), info.Size(), info.ModTime()})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].mod.Before(old[j].mod) })

	var freed int64
	for _, e := range old {
		if freed >= need {
			break
		}
		if os.Remove(e.path) != nil {
			continue
		}
		freed += e.size
		res.Files++
	}
	res.Bytes = freed
	res.At = g.now().UTC().Format(time.RFC3339)
	return res, res.Files > 0, freed >= need
}

func isHex2(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Run checks free space every checkEvery until stop closes.
func Run(dir func() string, stop <-chan struct{}) {
	g := newTrimmer(dir)
	wait := checkEvery
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		wait = checkEvery
		if res, trimmed, done := g.pass(); trimmed {
			last.mu.Lock()
			last.last = &res
			last.mu.Unlock()
			log.Printf("go-build: home is low, removed %d cache entries unused for over %s (%d MiB)",
				res.Files, minAge, res.Bytes>>20)
			if !done {
				wait = retryAfter
			}
		} else if !done {
			wait = retryAfter
		}
		t.Reset(wait)
	}
}

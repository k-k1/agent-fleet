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

	"golang.org/x/sys/unix"

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
	// Test seams: beforeOpen runs between resolving the cache directory and opening it,
	// beforeRemove between the survey and each removal.
	beforeOpen   func(real string)
	beforeRemove func(rel string)
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

// entry is one candidate file: its directory is held open by descriptor, so the file is judged
// and removed relative to that directory whatever happens to the path meanwhile.
type entry struct {
	dir  *os.File
	name string
	mod  time.Time
}

func (e entry) rel() string { return filepath.Join(filepath.Base(e.dir.Name()), e.name) }

// openDirNoFollow opens name under dirfd (AT_FDCWD semantics when dirfd is unix.AT_FDCWD) as a
// directory, refusing a symlink as its last element.
func openDirNoFollow(dirfd int, name string) (*os.File, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// statNoFollow is fstatat(dir, name, AT_SYMLINK_NOFOLLOW): a link is reported as a link.
func statNoFollow(dir *os.File, name string) (st unix.Stat_t, err error) {
	err = unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

// openat2Fn is unix.Openat2; a var so a test can make it fail the way an old kernel or a
// seccomp profile does.
var openat2Fn = unix.Openat2

// openRoot opens the cache directory. real has no links in it (EvalSymlinks), so
// RESOLVE_NO_SYMLINKS refuses a link at ANY element, not just the last. Where openat2 is
// unavailable (kernel before 5.6, or a seccomp profile that denies it) it falls back to an
// O_NOFOLLOW open, which covers only the last element; the identity check in pass() then
// catches a swapped parent.
func openRoot(real string) (*os.File, error) {
	fd, err := openat2Fn(unix.AT_FDCWD, real, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err == unix.ENOSYS || err == unix.EPERM {
		return openDirNoFollow(unix.AT_FDCWD, real)
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), real), nil
}

func isRegular(st unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFREG }

// pass runs one check and, when home is low, one trim. done=false means it could not reach the
// target (nothing old enough, or an error), so the caller waits longer before trying again.
//
// Nothing is reached by path after the cache directory is opened: the directory and each
// two-hex-digit directory are opened with O_NOFOLLOW and every stat and unlink is relative to
// those descriptors, so a path swapped for a link (to anywhere, including inside the cache)
// cannot redirect a delete. One window is left: Go can refresh an entry's mtime between the
// last check and the unlink. Go cannot be made to wait for this; the price is one rebuild.
func (g trimmer) pass() (res Result, trimmed, done bool) {
	if !g.enabled() {
		return res, false, true
	}
	free, total, ok := g.free()
	if !ok || float64(free) >= belowFrac*float64(total) {
		return res, false, true
	}
	real, err := filepath.EvalSymlinks(g.dir())
	if err != nil || !sameVolumeFn(real, paths.HomeDir()) {
		return res, false, true
	}
	// What was checked is what gets opened: the directory's identity is taken now and compared
	// with the opened descriptor, so a directory exchanged for another one (no link involved)
	// between the check and the open is refused as well.
	var want unix.Stat_t
	if unix.Stat(real, &want) != nil {
		return res, false, true
	}
	if g.beforeOpen != nil {
		g.beforeOpen(real)
	}
	root, err := openRoot(real)
	if err != nil {
		log.Printf("go-build: cannot open the cache directory for trimming: %v", err)
		return res, false, true
	}
	defer root.Close()
	var home, rst unix.Stat_t
	if unix.Stat(paths.HomeDir(), &home) != nil || unix.Fstat(int(root.Fd()), &rst) != nil ||
		home.Dev != rst.Dev || want.Dev != rst.Dev || want.Ino != rst.Ino {
		return res, false, true
	}
	target := int64(toFrac * float64(total))
	cutoff := g.now().Add(-minAge)

	var old []entry
	tops, _ := root.ReadDir(-1)
	for _, top := range tops {
		if !isHex2(top.Name()) {
			continue // README, trim.txt, anything Go did not put in an entry directory
		}
		dir, err := openDirNoFollow(int(root.Fd()), top.Name())
		if err != nil {
			continue // not a directory, or a link
		}
		defer dir.Close()
		names, _ := dir.Readdirnames(-1)
		for _, n := range names {
			st, err := statNoFollow(dir, n)
			if err != nil || !isRegular(st) || !time.Unix(st.Mtim.Unix()).Before(cutoff) {
				continue
			}
			old = append(old, entry{dir, n, time.Unix(st.Mtim.Unix())})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].mod.Before(old[j].mod) })

	// The real free space is read after every removal (one statfs), never estimated from file
	// sizes: blocks round sizes up, and a file that is hard-linked elsewhere or still open frees
	// nothing.
	for _, e := range old {
		if g.beforeRemove != nil {
			g.beforeRemove(e.rel())
		}
		// Go refreshes the mtime when it uses an entry, possibly after the survey: look again.
		st, err := statNoFollow(e.dir, e.name)
		if err != nil || !isRegular(st) || !time.Unix(st.Mtim.Unix()).Before(cutoff) {
			continue
		}
		if unix.Unlinkat(int(e.dir.Fd()), e.name, 0) != nil {
			continue
		}
		res.Bytes += st.Size
		res.Files++
		f, _, ok := g.free()
		if !ok {
			break // no evidence the target was reached: stop and back off
		}
		if int64(f) >= target {
			done = true
			break
		}
	}
	res.At = g.now().UTC().Format(time.RFC3339)
	return res, res.Files > 0, done
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

// tick is one wake-up of Run: it returns how long to wait before the next.
func (g trimmer) tick() time.Duration {
	res, trimmed, done := g.pass()
	if trimmed {
		last.mu.Lock()
		last.last = &res
		last.mu.Unlock()
		log.Printf("go-build: home is low, removed %d cache entries unused for over %s (%d MiB)",
			res.Files, minAge, res.Bytes>>20)
	}
	if !done {
		return retryAfter
	}
	return checkEvery
}

// Run checks free space every checkEvery until stop closes.
func Run(dir func() string, stop <-chan struct{}) {
	g := newTrimmer(dir)
	t := time.NewTimer(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		t.Reset(g.tick())
	}
}

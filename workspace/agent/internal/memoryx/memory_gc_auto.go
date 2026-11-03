package memoryx

import (
	"sync"
	"time"
)

// `git gc --auto` is a no-op until the repository passes git's own thresholds (about 6700
// loose objects or 50 packs), yet it used to run after almost every write: one git process each
// (#3255). memoryGCAuto lets the judgement stay with git but asks it at most once per
// interval, or once per batch of calls, per repository.
//
// The first call for a repository in this process always runs. A throttle that waited out the
// interval first would never fire in an Agent restarted more often than that, and the
// thresholds above are crossed by writes, not by time.
const (
	memoryGCInterval = time.Minute
	// memoryGCBatch caps how many writes can pass between two checks however quickly they
	// arrive: a bulk import writes thousands of objects inside one interval.
	memoryGCBatch = 20
	// memoryGCMaxRepos bounds the map; it holds one entry in production (one memory repository).
	memoryGCMaxRepos = 64
)

type memoryGCState struct {
	last  time.Time
	calls int
}

var memoryGCAuto = struct {
	sync.Mutex
	m   map[string]*memoryGCState
	now func() time.Time
}{m: map[string]*memoryGCState{}, now: time.Now}

// memoryGCDue records one write and reports whether this one should run `gc --auto`.
func memoryGCDue(repo string) bool {
	g := &memoryGCAuto
	g.Lock()
	defer g.Unlock()
	now := g.now()
	st := g.m[repo]
	if st == nil {
		if len(g.m) >= memoryGCMaxRepos {
			clear(g.m)
		}
		st = &memoryGCState{}
		g.m[repo] = st
		st.last, st.calls = now, 0
		return true
	}
	st.calls++
	if st.calls >= memoryGCBatch || now.Sub(st.last) >= memoryGCInterval {
		st.last, st.calls = now, 0
		return true
	}
	return false
}

// memoryRunGCAuto is the throttled `git gc --auto --quiet` after a write. A failure is
// swallowed: the write it follows already succeeded.
func memoryRunGCAuto() {
	if !memoryGCDue(memoryRepoDir()) {
		return
	}
	_, _ = memoryGitRun("gc", "--auto", "--quiet")
}

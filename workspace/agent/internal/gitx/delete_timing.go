package gitx

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// deleteTimer records how long each step of one working-copy delete took, and logs one line
// when the delete ends. It exists because the delete used to hold a process-wide gate for all of
// it, and which step was slow decides what is worth moving out of the gate (#4833). One line per
// delete is cheap enough to leave on.
type deleteTimer struct {
	dir   string
	start time.Time
	steps []string
}

func newDeleteTimer(dir string) *deleteTimer { return &deleteTimer{dir: dir, start: time.Now()} }

// deleteStepHook, when set, is called as each step of a delete starts (and at "pre-gate" and
// "git-remove", see fireDeleteHook). Tests use it to hold a delete at a chosen point and to see
// two deletes there at once; production never sets it.
var deleteStepHook atomic.Pointer[func(step, dir string)]

// SetDeleteStepHook installs fn (nil removes it) and returns a function that puts back the hook
// that was installed before, so nested or overlapping tests do not clear one another's.
func SetDeleteStepHook(fn func(step, dir string)) (restore func()) {
	var p *func(step, dir string)
	if fn != nil {
		p = &fn
	}
	prev := deleteStepHook.Swap(p)
	return func() { deleteStepHook.CompareAndSwap(p, prev) }
}

func fireDeleteHook(step, dir string) {
	if h := deleteStepHook.Load(); h != nil {
		(*h)(step, dir)
	}
}

func (t *deleteTimer) step(name string, fn func()) {
	fireDeleteHook(name, t.dir)
	s := time.Now()
	fn()
	t.steps = append(t.steps, fmt.Sprintf("%s=%s", name, time.Since(s).Round(time.Millisecond)))
}

func (t *deleteTimer) stepOK(name string, fn func() bool) (ok bool) {
	t.step(name, func() { ok = fn() })
	return ok
}

func (t *deleteTimer) stepErr(name string, fn func() error) (err error) {
	t.step(name, func() { err = fn() })
	return err
}

func (t *deleteTimer) log() {
	log.Printf("delete working copy %s: total=%s %s", t.dir, time.Since(t.start).Round(time.Millisecond), strings.Join(t.steps, " "))
}

// parentRegistry orders `git worktree remove` against `git worktree prune` on one base clone.
// Removes of different worktrees run side by side (measured: three removes on one base clone took
// about as long as one, and git left no entry behind); prune takes the write side because it
// deletes the registry entry of any worktree whose folder is gone, and a remove still in flight
// has deleted its folder but not yet its entry.
var parentRegistry sync.Map // base clone path -> *sync.RWMutex

func parentLock(parent string) *sync.RWMutex {
	l, _ := parentRegistry.LoadOrStore(markKey(parent), &sync.RWMutex{})
	return l.(*sync.RWMutex)
}

// removeWorktree drops a linked worktree from disk and from its base clone's registry.
func removeWorktree(parent, dir string) (out string, err error) {
	l := parentLock(parent)
	l.RLock()
	fireDeleteHook("git-remove", dir)
	out, err = Combined(parent, "worktree", "remove", "--force", dir)
	l.RUnlock()
	if err != nil {
		return out, err
	}
	pruneWorktrees(parent)
	return "", nil
}

// pruneWorktrees runs `git worktree prune` once no remove is in flight on parent.
func pruneWorktrees(parent string) {
	l := parentLock(parent)
	l.Lock()
	defer l.Unlock()
	_ = Cmd(parent, "worktree", "prune").Run()
}

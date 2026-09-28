package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A thread loaded on the shared app-server cannot be opened by a directly launched TUI: codex
// shows "This conversation is open in another app" and waits for a keypress (measured 0.157.1
// and 0.158.0). The server unloads a thread about 70 s after its last subscriber leaves, and
// only then does the TUI get in. A managed session switched to the Terminal route hits this
// every time — the writer unsubscribes in DropHandle, but the read-only observer in package
// main is subscribed to every loaded thread and keeps it loaded indefinitely.
//
// So a Terminal launch that resumes a thread the daemon may hold (1) has the observer let go
// of it, and (2) waits in the pane, before exec'ing codex, until the server has unloaded it.

// ReleaseObservedThread is the seam package main fills with its observer's release: unsubscribe
// from the thread and stop re-attaching it until it is unloaded. This package holds no observer.
var ReleaseObservedThread = func(threadID string) {}

// RestoreObservedThread is its counterpart, called when a managed Resume takes the thread back:
// the observer may attach to it again even though no unload was seen in between.
var RestoreObservedThread = func(threadID string) {}

// ThreadReleaseTimeout bounds the pane's wait. Past it the TUI starts anyway: the worst case is
// codex's own lock screen, which still offers a retry.
const ThreadReleaseTimeout = 3 * time.Minute

// releaseForTUI prepares a Terminal launch that resumes threadID and returns the app-server
// address the pane must wait on, or "" when there is nothing to wait for (a fresh launch, or
// no usable daemon — the mark is only set while one is up).
func releaseForTUI(threadID string) string {
	if threadID == "" {
		return ""
	}
	addr := os.Getenv(appServerAddrEnv)
	if addr == "" {
		return ""
	}
	ReleaseObservedThread(threadID)
	return addr
}

// AwaitThreadUnloaded polls the app-server at addr until threadID is no longer loaded. It
// reports true once the thread is gone (or the daemon is unreachable, which holds nothing)
// and false when timeout passes first. It only reads thread/loaded/list and subscribes to
// nothing, so it never becomes a holder itself.
func AwaitThreadUnloaded(addr, threadID string, timeout, poll time.Duration) bool {
	cl, err := newAppClient(addr)
	if err != nil {
		return true
	}
	go cl.readLoop()
	defer cl.close()
	deadline := time.Now().Add(timeout)
	for {
		loaded, err := threadLoaded(cl, threadID)
		if err != nil || !loaded {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

func threadLoaded(cl *appClient, threadID string) (bool, error) {
	var cursor any
	for {
		raw, err := cl.call("thread/loaded/list", map[string]any{"cursor": cursor, "limit": nil}, 5*time.Second)
		if err != nil {
			return false, err
		}
		var res struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return false, err
		}
		if slices.Contains(res.Data, threadID) {
			return true, nil
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			return false, nil
		}
		cursor = *res.NextCursor
	}
}

// While the pane waits, the terminal belongs to the waiter, not to codex: a prompt typed into
// it is read by nobody and lands, if at all, as stray keystrokes before codex draws its
// composer. A marker keyed by the session name covers the whole hand-over, so the prompt
// paths can refuse or hold back:
//
//   - "pending": written by BuildLaunch before the pane exists, so the gap until the waiter
//     starts is covered (valid for awaitPendingTTL);
//   - "<pid>": the waiter, for its lifetime (a dead pid does not count);
//   - "done": written by the waiter as it exits; codex is starting but has no composer yet
//     (recent for awaitDoneTTL).
//
// The pane's foreground command cannot tell: tmux reports the wrapping shell (measured: bash).
func awaitMarkerPath(name string) string {
	return filepath.Join(paths.AgentStateDir(), "codex-await", name)
}

const (
	awaitPending    = "pending"
	awaitDone       = "done"
	awaitPendingTTL = 15 * time.Second
	awaitDoneTTL    = 30 * time.Second
)

func writeAwaitMarker(name, state string) {
	if !session.ValidName(name) {
		return
	}
	p := awaitMarkerPath(name)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(state), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func readAwaitMarker(name string) (state string, age time.Duration, ok bool) {
	if !session.ValidName(name) {
		return "", 0, false
	}
	p := awaitMarkerPath(name)
	fi, err := os.Stat(p)
	if err != nil {
		return "", 0, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(string(b)), time.Since(fi.ModTime()), true
}

// markPending is BuildLaunch's half: the pane is about to wait.
func markPending(name string) { writeAwaitMarker(name, awaitPending) }

// MarkAwaiting records that this process is the pane's waiter for session name and returns
// the cleanup, which marks the wait done. An empty name (a pane without AF_SESSION_NAME)
// records nothing.
func MarkAwaiting(name string) func() {
	if !session.ValidName(name) {
		return func() {}
	}
	writeAwaitMarker(name, strconv.Itoa(os.Getpid()))
	return func() { writeAwaitMarker(name, awaitDone) }
}

// Awaiting reports whether session name's pane is waiting (or about to wait) for the
// app-server to release its thread.
func Awaiting(name string) bool {
	state, age, ok := readAwaitMarker(name)
	if !ok {
		return false
	}
	switch state {
	case awaitPending:
		return age < awaitPendingTTL
	case awaitDone:
		return false
	}
	pid, err := strconv.Atoi(state)
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// JustReleased reports that the wait ended moments ago: codex is starting in the pane and may
// not have drawn its composer yet.
func JustReleased(name string) bool {
	state, age, ok := readAwaitMarker(name)
	return ok && state == awaitDone && age < awaitDoneTTL
}

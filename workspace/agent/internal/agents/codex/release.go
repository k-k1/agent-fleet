package codex

import (
	"encoding/json"
	"os"
	"slices"
	"time"
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

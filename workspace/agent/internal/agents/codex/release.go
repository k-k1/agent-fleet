package codex

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"
)

// A thread loaded on the shared app-server cannot be opened by a directly launched TUI: codex
// shows "This conversation is open in another app" and waits for a keypress (measured 0.157.1
// and 0.158.0). The server unloads a thread about 70 s after its last subscriber leaves, and
// only then does the TUI get in.
//
// So a managed session goes to the Terminal route only once it is stopped and released:
//
//   - DropHandle (halt, archive, delete, a switch) unsubscribes the writer and has the
//     read-only observer in package main let go of the thread too (ReleaseObservedThread).
//     Without that the observer, subscribed to every loaded thread, keeps it loaded for good
//     (a halted session stayed locked for over 5 minutes, docs/log/124 §3.2).
//   - A Terminal launch that resumes a thread the server still has loaded is refused with
//     ErrThreadReleasing instead of waiting in the pane: prompts sent into a pane that nobody
//     reads were what made a live hand-over need its own state machine.

// ReleaseObservedThread is the seam package main fills with its observer's release: unsubscribe
// from the thread and stop re-attaching it until it is unloaded. This package holds no observer.
var ReleaseObservedThread = func(threadID string) {}

// RestoreObservedThread is its counterpart, called when a managed Resume takes the thread back:
// the observer may attach to it again even though no unload was seen in between.
var RestoreObservedThread = func(threadID string) {}

// ErrThreadReleasing refuses a Terminal launch while the shared app-server still has the
// conversation loaded; the caller answers 409 codex_releasing.
var ErrThreadReleasing = errors.New("codex がまだこの会話を手放していません（停止から通常 1 分ほど）。少し待ってからもう一度お試しください")

// threadHeld reports whether the shared app-server still has threadID loaded, with one
// read-only thread/loaded/list (it subscribes to nothing, so it never becomes a holder itself).
// No daemon, or one that does not answer, holds nothing. A held thread is also released from
// the observer here: a stop that never reached DropHandle (an Agent that died, a handle lost
// with its runtime) would otherwise leave it loaded for good.
func threadHeld(threadID string) bool {
	if threadID == "" {
		return false
	}
	addr := os.Getenv(appServerAddrEnv)
	if addr == "" {
		return false
	}
	cl, err := newAppClient(addr)
	if err != nil {
		return false
	}
	go cl.readLoop()
	defer cl.close()
	loaded, err := threadLoaded(cl, threadID)
	if err != nil || !loaded {
		return false
	}
	ReleaseObservedThread(threadID)
	return true
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

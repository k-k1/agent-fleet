package muse

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// registerHandle registers a handle under name and removes it on test cleanup.
func registerHandle(t *testing.T, name string, h *threadHandle) {
	t.Helper()
	handlesMu.Lock()
	handles[name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, name)
		handlesMu.Unlock()
	})
}

// TestManagedContextBeforeNotification verifies that ok=false is returned before the
// first session/contextUsage notification arrives — the ContextBar must not appear until
// a real reading exists.
func TestManagedContextBeforeNotification(t *testing.T) {
	h := &threadHandle{}
	newTestHandle(t, h)
	registerHandle(t, "ctx-before", h)

	if _, _, ok := ManagedContext("ctx-before"); ok {
		t.Fatal("ManagedContext ok=true before any notification — want false")
	}
}

// TestContextUsageNotificationRecorded checks that a session/contextUsage notification
// is decoded and stored on the handle, and that ManagedContext / ContextFill reflect it.
func TestContextUsageNotificationRecorded(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, "ctx-ok", h)

	win := int64(1_007_997)
	host.Notify(msp.NotificationSessionContextUsage, map[string]any{
		"sessionId":    "01a0c1d6-0000-7000-8000-000000000001",
		"usedTokens":   int64(50_000),
		"windowTokens": win,
		"pressure":     "low",
		"sourceRange":  map[string]any{},
		"viewCursor":   "1",
	})
	waitFor(t, func() bool {
		_, _, ok := ManagedContext("ctx-ok")
		return ok
	})

	used, winOut, ok := ManagedContext("ctx-ok")
	if !ok {
		t.Fatal("ManagedContext ok=false after notification")
	}
	if used != 50_000 {
		t.Errorf("usedTokens = %d, want 50000", used)
	}
	if winOut == nil || *winOut != win {
		t.Errorf("windowTokens = %v, want %d", winOut, win)
	}

	// ContextFill round-trip.
	c := (agentImpl{}).ContextFill(session.Meta{Name: "ctx-ok"})
	if c == nil {
		t.Fatal("ContextFill returned nil with live data")
	}
	if c.Tokens != 50_000 {
		t.Errorf("ContextFill tokens = %d, want 50000", c.Tokens)
	}
	if c.Window != int(win) {
		t.Errorf("ContextFill window = %d, want %d", c.Window, win)
	}
}

// TestContextUsageWindowTokensAbsent verifies that when windowTokens is not present in
// the notification, ContextFill falls back to MuseDefaultWindow rather than zero.
func TestContextUsageWindowTokensAbsent(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, "ctx-nowin", h)

	host.Notify(msp.NotificationSessionContextUsage, map[string]any{
		"sessionId":   "01a0c1d6-0000-7000-8000-000000000001",
		"usedTokens":  int64(20_000),
		"pressure":    "low",
		"sourceRange": map[string]any{},
		"viewCursor":  "1",
		// windowTokens deliberately absent
	})
	waitFor(t, func() bool {
		_, _, ok := ManagedContext("ctx-nowin")
		return ok
	})

	used, winOut, ok := ManagedContext("ctx-nowin")
	if !ok {
		t.Fatal("ManagedContext ok=false after notification")
	}
	if used != 20_000 {
		t.Errorf("usedTokens = %d, want 20000", used)
	}
	if winOut != nil {
		t.Errorf("windowTokens = %v, want nil (absent)", winOut)
	}

	// ContextFill must use MuseDefaultWindow as the fallback, not zero.
	c := (agentImpl{}).ContextFill(session.Meta{Name: "ctx-nowin"})
	if c == nil {
		t.Fatal("ContextFill returned nil")
	}
	if c.Window != MuseDefaultWindow {
		t.Errorf("ContextFill window = %d, want MuseDefaultWindow (%d)", c.Window, MuseDefaultWindow)
	}
}

// TestContextUsageLatestWins checks that successive notifications replace the stored
// value rather than accumulating (context fill is a snapshot, not a sum).
func TestContextUsageLatestWins(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, "ctx-latest", h)

	win := int64(1_007_997)
	host.Notify(msp.NotificationSessionContextUsage, map[string]any{
		"sessionId":    "01a0c1d6-0000-7000-8000-000000000001",
		"usedTokens":   int64(10_000),
		"windowTokens": win,
		"pressure":     "low",
		"sourceRange":  map[string]any{},
		"viewCursor":   "1",
	})
	waitFor(t, func() bool {
		u, _, ok := ManagedContext("ctx-latest")
		return ok && u == 10_000
	})

	host.Notify(msp.NotificationSessionContextUsage, map[string]any{
		"sessionId":    "01a0c1d6-0000-7000-8000-000000000001",
		"usedTokens":   int64(80_000),
		"windowTokens": win,
		"pressure":     "high",
		"sourceRange":  map[string]any{},
		"viewCursor":   "2",
	})
	waitFor(t, func() bool {
		u, _, ok := ManagedContext("ctx-latest")
		return ok && u == 80_000
	})

	used, _, ok := ManagedContext("ctx-latest")
	if !ok {
		t.Fatal("ManagedContext ok=false after second notification")
	}
	// Latest value replaces; earlier value must not persist.
	if used != 80_000 {
		t.Errorf("usedTokens = %d, want latest 80000", used)
	}
}

// TestManagedContextNoHandle verifies that a name with no registered handle returns false.
func TestManagedContextNoHandle(t *testing.T) {
	if _, _, ok := ManagedContext("no-such-session"); ok {
		t.Fatal("ManagedContext ok=true for non-existent handle")
	}
}

// TestContextFillNoHandle verifies ContextFill returns nil when no handle is registered,
// so the ContextBar is not drawn for stopped or TUI sessions.
func TestContextFillNoHandle(t *testing.T) {
	if c := (agentImpl{}).ContextFill(session.Meta{Name: "gone"}); c != nil {
		t.Errorf("ContextFill = %+v, want nil for no handle", c)
	}
}

package main

import (
	"testing"
	"time"
)

// A visible pane is presence only within presenceGrace of the member's last
// interaction (#3462): visibility alone must not pin the Workspace.
func TestBrowserViewerPresenceIsBoundedByInteraction(t *testing.T) {
	const ws = "ws-pane"
	grace := 30 * time.Minute
	old := presenceGrace
	presenceGrace = grace
	defer func() { presenceGrace = old }()

	t.Run("fresh show is present, stale is not", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		if !r.watched(ws, grace, time.Now()) {
			t.Fatal("a pane that was just shown must count (first show bounds an old Console)")
		}
		if r.watched(ws, grace, time.Now().Add(grace+time.Minute)) {
			t.Fatal("a visible pane with no interaction must stop counting after the grace")
		}
	})

	t.Run("interaction renews", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		r.mu.Lock()
		r.lastInput[ws] = time.Now().Add(-grace - time.Minute) // as if untouched for a long time
		r.mu.Unlock()
		if r.watched(ws, grace, time.Now()) {
			t.Fatal("setup: expected the pane to have lapsed")
		}
		v.noteInteraction()
		if !r.watched(ws, grace, time.Now()) {
			t.Fatal("an interaction on a visible pane must count")
		}
	})

	t.Run("re-show does not refresh", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		v.mu.Lock()
		v.lastInteract = time.Now().Add(-grace - time.Minute)
		v.mu.Unlock()
		v.setVisible(false)
		v.setVisible(true)
		if r.watched(ws, grace, time.Now()) {
			t.Fatal("becoming visible again must not count as interaction")
		}
	})

	t.Run("re-show keeps a recent interaction", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		v.noteInteraction()
		v.setVisible(false)
		if r.watched(ws, grace, time.Now()) {
			t.Fatal("a hidden pane must not count")
		}
		v.setVisible(true)
		if !r.watched(ws, grace, time.Now()) {
			t.Fatal("a tab switch moments after a click must keep the pane present")
		}
	})

	t.Run("hidden interaction is replayed", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		v.mu.Lock()
		v.lastInteract = time.Now().Add(-grace - time.Minute)
		v.mu.Unlock()
		v.setVisible(false)
		v.noteInteraction()
		v.setVisible(true)
		if !r.watched(ws, grace, time.Now()) {
			t.Fatal("an interaction reported while hidden must be remembered")
		}
	})

	t.Run("close releases", func(t *testing.T) {
		r := newConnRegistry()
		v := newBrowserViewer(r, ws)
		v.setVisible(true)
		v.close()
		v.noteInteraction()
		if c, _, _ := r.snapshot(ws); c != 0 {
			t.Fatalf("conns after close = %d", c)
		}
		if r.watched(ws, grace, time.Now()) {
			t.Fatal("a closed viewer must not count")
		}
	})
}

func TestBrowserInteractionFrame(t *testing.T) {
	for data, want := range map[string]bool{
		`{"type":"interaction"}`:               true,
		`{"type":"mouse","event":"move"}`:      false,
		`{"type":"visibility","visible":true}`: false,
		`not json`:                             false,
	} {
		if got := browserInteraction([]byte(data), 1); got != want {
			t.Errorf("%s: got %v want %v", data, got, want)
		}
	}
	if browserInteraction([]byte(`{"type":"interaction"}`), 2) {
		t.Error("a binary frame is not an interaction report")
	}
}

package main

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestConnRegistryWatched pins presence to "a human is touching it" rather than "a
// socket exists" (docs/log/75 P3). If this table breaks, one forgotten Console tab
// holding a terminal pane open warms the workspace forever — a main cause of "it never
// stops", and nothing on screen tells the user they are still being billed.
func TestConnRegistryWatched(t *testing.T) {
	const ws = "ws1"
	grace := 30 * time.Minute
	now := time.Now()

	r := newConnRegistry()
	if r.watched(ws, grace, now) {
		t.Error("answered present with no connection at all")
	}

	// Terminal (attention-bearing): present the moment it opens, absent once the
	// keystrokes stop.
	r.addConn(ws, "s1", true)
	if !r.watched(ws, grace, now) {
		t.Error("a terminal just opened is not counted as present")
	}
	if !r.watched(ws, grace, now.Add(29*time.Minute)) {
		t.Error("went absent while still inside the grace window")
	}
	if r.watched(ws, grace, now.Add(31*time.Minute)) {
		t.Error("a terminal with no keystrokes stays present = one forgotten tab warms the workspace forever")
	}
	// Typing again brings presence back.
	r.noteInput(ws, "s1")
	if !r.watched(ws, grace, time.Now().Add(time.Minute)) {
		t.Error("still absent after typing again")
	}

	// A non-terminal long-lived connection (a scheduled-run wake-up) counts as presence
	// unconditionally: it has no notion of a keystroke, and reading it as absence would
	// stop the workspace mid-delivery.
	r2 := newConnRegistry()
	r2.addConn(ws, "", false)
	if !r2.watched(ws, grace, now.Add(10*time.Hour)) {
		t.Error("the scheduled-run presence was cut off by the grace window")
	}

	// 0 disables the grace: any open socket counts as presence.
	r3 := newConnRegistry()
	r3.addConn(ws, "s1", true)
	if !r3.watched(ws, 0, now.Add(10*time.Hour)) {
		t.Error("AF_PRESENCE_IDLE_TIMEOUT=0 does not restore the previous behaviour")
	}

	// Disconnecting ends presence.
	r.doneConn(ws, "s1", true)
	if r.watched(ws, grace, time.Now()) {
		t.Error("answered present after the disconnect")
	}
}

// TestAttentionFreshGatesTheLease covers the decision the heartbeat makes about whether
// the presence lease may keep being renewed. It is a pure function precisely so the
// decision can be pinned without waiting on the 5s goroutine.
func TestAttentionFreshGatesTheLease(t *testing.T) {
	const ws = "ws1"
	r := newConnRegistry()
	r.addConn(ws, "s1", true)
	now := time.Now()
	if !r.attentionFresh(ws, time.Minute, now) {
		t.Error("the lease was stopped right after the socket opened")
	}
	if r.attentionFresh(ws, time.Minute, now.Add(2*time.Minute)) {
		t.Error("the lease keeps being renewed after the keystrokes stopped (connected_until in the DB stays alive)")
	}
	if !r.attentionFresh(ws, 0, now.Add(10*time.Hour)) {
		t.Error("the lease stopped with grace 0 (disabled)")
	}
	if !r.attentionFresh("unknown-ws", 0, now) {
		t.Error("while disabled, even an unknown workspace must be true")
	}
	if r.attentionFresh("unknown-ws", time.Minute, now) {
		t.Error("answered present for a workspace with no record")
	}
}

// TestIsTerminalInputCountsOnlyKeystrokes guards that ping and resize are not counted as
// keystrokes. The Console pings every open socket periodically, so treating "a frame
// arrived" as presence restores the forgotten-tab-warms-forever behaviour.
func TestIsTerminalInputCountsOnlyKeystrokes(t *testing.T) {
	cases := []struct {
		name string
		mt   int
		data string
		want bool
	}{
		{"keystroke", websocket.TextMessage, `{"type":"input","data":"ls\n"}`, true},
		{"ping is not presence", websocket.TextMessage, `{"type":"ping"}`, false},
		{"resize is not presence", websocket.TextMessage, `{"type":"resize","cols":80,"rows":24}`, false},
		{"PTY output (binary) is not presence", websocket.BinaryMessage, `{"type":"input"}`, false},
		{"broken JSON", websocket.TextMessage, `{`, false},
		{"empty", websocket.TextMessage, ``, false},
	}
	for _, c := range cases {
		if got := isTerminalInput(c.mt, []byte(c.data)); got != c.want {
			t.Errorf("%s: isTerminalInput = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWatchingSessionNeedsKeystrokes pins tier 1's "someone is at this session's
// terminal" to the same keystroke bound as tier 2's presence (#3460). If attachment alone
// counted, a Console tab left open with terminal panes would keep its sessions from ever
// being halted.
func TestWatchingSessionNeedsKeystrokes(t *testing.T) {
	const ws = "ws1"
	grace := 30 * time.Minute
	now := time.Now()

	r := newConnRegistry()
	if r.watchingSession(ws, "s1", grace, now) {
		t.Error("an unattached session counts as watched")
	}
	r.addConn(ws, "s1", true)
	if !r.watchingSession(ws, "s1", grace, now) {
		t.Error("a terminal just opened does not protect its session")
	}
	if r.watchingSession(ws, "s2", grace, now) {
		t.Error("another session of the workspace counts as watched")
	}
	if r.watchingSession(ws, "s1", grace, now.Add(31*time.Minute)) {
		t.Error("an attached terminal with no keystrokes pins its session forever")
	}
	r.noteInput(ws, "s1")
	if !r.watchingSession(ws, "s1", grace, time.Now().Add(time.Minute)) {
		t.Error("typing again does not protect the session")
	}
	if !r.watchingSession(ws, "s1", 0, now.Add(10*time.Hour)) {
		t.Error("grace 0 (disabled) does not restore attached-means-watched")
	}
	r.doneConn(ws, "s1", true)
	if r.watchingSession(ws, "s1", 0, now) {
		t.Error("a detached session still counts as watched")
	}
}

// TestWatchingSessionIsPerSession: typing into one session's terminal must not spare
// another session of the same workspace whose pane was left open untouched, or a single
// active session keeps every forgotten pane pinned (#3460 review).
func TestWatchingSessionIsPerSession(t *testing.T) {
	const ws = "ws1"
	grace := 30 * time.Minute
	r := newConnRegistry()
	r.addConn(ws, "idle", true)
	r.addConn(ws, "busy", true)
	later := time.Now().Add(40 * time.Minute)
	// Only "busy" is typed into, and only after "idle"'s grace ran out.
	r.mu.Lock()
	r.sessInput[ws]["busy"] = later
	r.lastInput[ws] = later
	r.mu.Unlock()
	if !r.watchingSession(ws, "busy", grace, later.Add(time.Minute)) {
		t.Error("the session being typed into is not protected")
	}
	if r.watchingSession(ws, "idle", grace, later.Add(time.Minute)) {
		t.Error("typing into another session pins an untouched pane")
	}
	// A shell with no session name protects nobody in tier 1.
	r.noteInput(ws, "")
	if r.watchingSession(ws, "idle", grace, time.Now().Add(31*time.Minute)) {
		t.Error("an unnamed shell's keystroke pins a session")
	}
	// Detaching the last terminal of a session drops its clock: a re-attach starts fresh.
	r.doneConn(ws, "idle", true)
	if _, ok := r.sessInput[ws]["idle"]; ok {
		t.Error("a detached session's keystroke clock was left behind")
	}
}

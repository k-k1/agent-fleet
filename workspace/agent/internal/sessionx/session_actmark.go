package sessionx

// The act mark orders a session list against the acts of a person that follow it (ADR 0137 §6,
// note of 2026-11-15). The Control Plane retires a session from a list it read a moment before;
// a person who starts that session or types into it in between must not have their work stopped.
//
// One counter serves the whole Agent. Every answer to GET /sessions takes a new value (takeActMark)
// and carries it; an act reads the current value (the same lock) and records it in the session's
// meta (PersonActMark). So "the act came after that list" is exactly PersonActMark >= the value the
// list carried. Order is decided by this number alone: no clocks are compared across machines, and
// nothing is inferred from a session's state.
//
// The value is never reused and never lower after a restart. The disk holds a ceiling (reserved)
// that every value handed out stays below; a restart begins at the higher of that ceiling and the
// clock, so a value from before the restart is below every value after it, whatever the clock did.
// The ceiling is raised a block ahead, so a list answer costs a disk write once per block.
//
// The guarantee holds while the state file survives. Restoring an older copy is not enough to break
// it by itself, because a restart begins at the higher of the restored ceiling and the clock. A
// number handed out before the restore can be reused only when it is above both: the clock is
// behind it (the clock stepped back, or the counter had run ahead of it) or the restore falls
// inside the block reserved ahead of the clock. A retire carrying a number above the counter is
// never read as "no act since" (retireOne releases the tree instead), but a number at or below it
// cannot be told from a reused one. Closing that needs an incarnation identity the Control Plane
// echoes (#4265).

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const actMarkBlock = 1 << 16

type actMarkState struct {
	Reserved int64 `json:"reserved"`
}

var actMarkStore = fstore.JSON[actMarkState](paths.AgentStateDir, "act-mark", ".json")

const actMarkKey = "mark"

type actClock struct {
	mu       sync.Mutex
	loaded   bool
	cur      int64
	reserved int64
	// recorded is the mark each session's meta already holds from an act, so a keystroke at an
	// unchanged mark costs a map lookup and no write.
	recorded map[string]int64
}

var actMarks actClock

func (c *actClock) loadLocked() {
	if c.loaded {
		return
	}
	st, _ := actMarkStore.Read(actMarkKey)
	c.cur = max(st.Reserved, time.Now().UnixMilli())
	c.reserved = max(st.Reserved, c.cur)
	c.recorded = map[string]int64{}
	c.loaded = true
}

// take hands out a new mark. ok is false when the ceiling could not be made durable: a value that
// a restart might hand out again must not be sent.
func (c *actClock) take() (n int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	c.cur++
	if c.cur >= c.reserved {
		next := c.cur + actMarkBlock
		if err := actMarkStore.Write(actMarkKey, actMarkState{Reserved: next}); err != nil {
			log.Printf("act mark: ceiling not written, the list carries no mark: %v", err)
			return 0, false
		}
		c.reserved = next
	}
	return c.cur, true
}

// now is the highest mark handed out or read by this Agent so far.
func (c *actClock) now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	return c.cur
}

// current is the mark an act made now carries, and whether name's meta already holds it.
func (c *actClock) current(name string) (mark int64, recorded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	return c.cur, c.recorded[name] >= c.cur
}

func (c *actClock) noteRecorded(name string, mark int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	if c.recorded[name] < mark {
		c.recorded[name] = mark
	}
}

// resetActMarks forgets the in-memory state so the next use reads the disk again: a restart, for a
// test.
func resetActMarks() {
	actMarks.mu.Lock()
	defer actMarks.mu.Unlock()
	actMarks.loaded = false
}

// takeActMark is the mark for one answer to GET /sessions. It is taken before the sessions are
// read: an act that lands between the two carries a value at or above it and is reported as after
// the list, which only ever errs towards leaving a session alone.
func takeActMark() (int64, bool) { return actMarks.take() }

// recordPersonAct writes the current mark into name's meta. The caller holds name's retire gate
// (either side), so the retire's check, release and mark are ordered against it: an act is seen by
// the check or lands on a session that is already marked, never in between. It writes nothing
// when the meta already holds the current mark.
func recordPersonAct(name string) error {
	mark, done := actMarks.current(name)
	if done {
		return nil
	}
	_, wrote, err := updateSessionMetaChecked(name, func(m *session.Meta) bool {
		if m.PersonActMark >= mark {
			return false
		}
		m.PersonActMark = mark
		return true
	})
	if err != nil {
		return err
	}
	if wrote {
		actMarks.noteRecorded(name, mark)
	}
	return nil
}

// notePersonAct records an act under the read side of name's retire gate. A failure is logged and
// the act goes on: the input is the person's and is not refused for want of a meta write. For a
// caller that already holds the gate, recordPersonAct.
func notePersonAct(name string) {
	g := retireGate(name)
	g.RLock()
	defer g.RUnlock()
	if err := recordPersonAct(name); err != nil {
		log.Printf("act mark: %s: the act could not be recorded: %v", name, err)
	}
}

// holdPersonRelaunch is for a person's relaunch of an existing session that does not go through
// /start or a Managed resume (a driver switch, a route switch, a carried answer): it takes the
// read side of the retire gate, refuses a session a retire has marked (errSessionRetired: resume it
// first, which clears the mark), records the act and returns the release. Held across the launch,
// so a retire cannot mark the session between the check and the launch and leave it marked and
// running. Call it with the lifecycle gate already held, never with the gate's read side.
func holdPersonRelaunch(name string) (release func(), err error) {
	g := retireGate(name)
	g.RLock()
	cur, ok := readMetaStable(name)
	if (ok && isRetired(cur)) || (!ok && metaFileExists(name)) { // unreadable is in doubt: not launched
		g.RUnlock()
		return nil, errSessionRetired
	}
	if err := recordPersonAct(name); err != nil {
		log.Printf("act mark: %s: the act could not be recorded: %v", name, err)
	}
	return g.RUnlock, nil
}

// paneActHold bounds how long a key waits for the read side: one that still cannot be recorded by
// then is dropped. A var for the tests.
var paneActHold = 30 * time.Second

// NotePersonActPane is notePersonAct for the terminal pane's writer, and reports whether the bytes
// may go on to the PTY. It takes no lock and writes nothing while the meta holds the current mark.
// Otherwise it waits for the read side, without holding anything meanwhile, and records under it.
// Failing to take the read side proves nothing about the session (a pending retire also stops new
// readers while a long /start holds one) and a retired mark in the meta may be an older retire's, so
// no key goes on unrecorded because of either: once the retire that holds the write side is done
// the key records itself, on a session that is by then marked, which is the "act arrives at a
// retired session" state. The retire stops the session, which ends the pane, within seconds; a key
// that waits longer than paneActHold is dropped.
func NotePersonActPane(name string) (deliver bool) {
	if _, done := actMarks.current(name); done {
		return true
	}
	g := retireGate(name)
	start := time.Now()
	for !g.TryRLock() {
		if time.Since(start) > paneActHold {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer g.RUnlock()
	if err := recordPersonAct(name); err != nil {
		log.Printf("act mark: %s: the act could not be recorded: %v", name, err)
	}
	return true
}

// inputCarriesOrigin reports whether a /input body carries any field an automatic sender sets: a
// source of any value (the /mcp stamp included, an unknown one too), a peer message, a report, or a
// schedule's fields. Only an input with none of them is read as the member's own. This is
// deliberately not typedByMember, which decides what the mirror draws as the user's turn and
// treats a source such as "operator" as the member's when no report_to rides with it.
func inputCarriesOrigin(source, peerFrom, peerIntent, reportTo, scheduleID, scheduleSlot string, scheduleDelivery bool) bool {
	return source != "" || peerFrom != "" || strings.TrimSpace(peerIntent) != "" || reportTo != "" ||
		scheduleID != "" || scheduleSlot != "" || scheduleDelivery
}

// NotePersonActInput is what the terminal pane's writer calls for each write from the browser: a
// write that consists only of focus reports is not a person's act and goes on unrecorded;
// everything else is one (NotePersonActPane). Reason: a member who watches a run finish and then
// switches the browser tab sends a focus report, and counting it would release a session nobody
// touched.
func NotePersonActInput(name string, b []byte) (deliver bool) {
	if focusReportsOnly(b) {
		return true
	}
	return NotePersonActPane(name)
}

// focusReportsOnly reports whether b is one or more focus reports (ESC [ I, ESC [ O) and nothing
// else. It is the only write that is not an act, and deliberately the only one: a byte grammar
// cannot tell a terminal's other replies from keys (xterm.js sends Shift+F3 as ESC [ 1 ; 2 R, which
// is a cursor position report by grammar, and an OSC or DCS payload is arbitrary). No xterm.js key
// produces ESC [ I or ESC [ O, so these two cannot be a person's key. A report followed by any
// byte, or cut across two writes, is not matched and is an act: when in doubt, an act. Known limit:
// a person who pastes the literal bytes ESC [ I or ESC [ O with bracketed paste off sends exactly
// a focus report (the Console's paste passes the clipboard through unchanged), and it is not
// counted.
func focusReportsOnly(b []byte) bool {
	if len(b) == 0 || len(b)%3 != 0 {
		return false
	}
	for i := 0; i < len(b); i += 3 {
		if b[i] != 0x1b || b[i+1] != '[' || (b[i+2] != 'I' && b[i+2] != 'O') {
			return false
		}
	}
	return true
}

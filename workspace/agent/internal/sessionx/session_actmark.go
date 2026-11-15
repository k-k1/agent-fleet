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

import (
	"log"
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

// paneActWait bounds how long a keystroke waits for a retire that is stopping the session.
const paneActWait = 2 * time.Second

// NotePersonActPane is notePersonAct for the terminal pane's writer. It takes no lock and writes
// nothing while the meta holds the current mark. Otherwise it waits at most paneActWait for the
// read side: a retire holds the write side through a stop, and a keystroke into a session being
// stopped is the "act arrives at a retired session" outcome, so past the bound the key goes on
// unrecorded and the next one tries again.
func NotePersonActPane(name string) {
	if _, done := actMarks.current(name); done {
		return
	}
	g := retireGate(name)
	deadline := time.Now().Add(paneActWait)
	for !g.TryRLock() {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer g.RUnlock()
	if err := recordPersonAct(name); err != nil {
		log.Printf("act mark: %s: the act could not be recorded: %v", name, err)
	}
}

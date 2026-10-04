package opencode

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The compaction message shapes measured on 1.18.34 (/summarize and the automatic overflow
// compaction write the same row).
const (
	compactionRunning = `{"role":"assistant","mode":"compaction","agent":"compaction","summary":true,"cost":0,"time":{"created":2000}}`
	compactionDone    = `{"role":"assistant","mode":"compaction","agent":"compaction","summary":true,"cost":0.0055,"finish":"stop","time":{"created":2000,"completed":2500}}`
	compactionAborted = `{"role":"assistant","mode":"compaction","agent":"compaction","summary":true,"cost":0,"error":{"name":"MessageAbortedError"},"time":{"created":2000,"completed":2100}}`
	buildRunning      = `{"role":"assistant","mode":"build","agent":"build","time":{"created":3000}}`
)

func TestOpencodeCompactionInFlight(t *testing.T) {
	db := newOpencodeTestDB(t)
	cases := []struct {
		ses, data string
		want      bool
	}{
		{"ses_run", compactionRunning, true},
		{"ses_done", compactionDone, false},
		{"ses_abort", compactionAborted, false},
		{"ses_build", buildRunning, false},
	}
	for i, c := range cases {
		insMsg(t, db, c.ses+"_u", c.ses, 1000, `{"role":"user","time":{"created":1000}}`)
		insMsg(t, db, c.ses+"_a", c.ses, 2000+i, c.data)
		if got := compactionInFlight(db, c.ses); got != c.want {
			t.Errorf("%s: compactionInFlight = %v, want %v", c.ses, got, c.want)
		}
	}
	// A later release that writes session.time_compacting is honoured too.
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created,time_compacting) VALUES('ses_tc',NULL,'/d',1,12345)`); err != nil {
		t.Fatal(err)
	}
	if !compactionInFlight(db, "ses_tc") {
		t.Error("ses_tc: time_compacting set, want compacting")
	}
	if compactionInFlight(db, "ses_missing") {
		t.Error("ses_missing: want not compacting")
	}
}

// Terminal: the store is the only source, confirmed by liveState's "working" so a compaction
// an earlier process left incomplete does not stick.
func TestOpencodeTerminalCompacting(t *testing.T) {
	db := newOpencodeLiveStore(t)
	fakeTmuxSession(t, time.Time{})
	dir, ses := "/home/dev/repos/tc", "ses_term"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	insMsg(t, db, "u1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insMsg(t, db, "a1", ses, 2000, compactionRunning)
	m := session.Meta{Dir: dir, Name: "tc", Kind: session.KindOpencode}

	if got := (agentImpl{}).WireLive(m, true).State; got != "compacting" {
		t.Fatalf("WireLive state = %q, want compacting", got)
	}
	if td, _ := readTranscript(m); !td.Compacting {
		t.Fatal("readTranscript: Compacting = false, want true")
	}
	// The DriveState vocabulary stays "working": only the list state and the chat banner
	// carry compacting, as with codex.
	if got := LiveState(m); got != "working" {
		t.Fatalf("LiveState = %q, want working", got)
	}

	// The summary finished: back to plain working/idle.
	if _, err := db.Exec(`UPDATE message SET data = ? WHERE id = 'a1'`, compactionDone); err != nil {
		t.Fatal(err)
	}
	if got := (agentImpl{}).WireLive(m, true).State; got != "idle" {
		t.Fatalf("after the summary completed: WireLive state = %q, want idle", got)
	}
	if td, _ := readTranscript(m); td.Compacting {
		t.Fatal("after the summary completed: Compacting = true")
	}
}

// A Terminal pane relaunched after a SIGKILL mid-compaction: the store keeps the incomplete
// summary forever, and liveState's process bound must keep it from reading as compacting.
func TestOpencodeTerminalCompactingStaleAfterRelaunch(t *testing.T) {
	db := newOpencodeLiveStore(t)
	fakeTmuxSession(t, time.Now()) // the pane's process started after the message was written
	dir, ses := "/home/dev/repos/tcs", "ses_stale"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	insMsg(t, db, "u1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insMsg(t, db, "a1", ses, 2000, compactionRunning)
	m := session.Meta{Dir: dir, Name: "tcs", Kind: session.KindOpencode}
	if got := (agentImpl{}).WireLive(m, true).State; got == "compacting" {
		t.Fatal("a compaction an earlier process left incomplete reads as compacting")
	}
	if td, _ := readTranscript(m); td.Compacting {
		t.Fatal("readTranscript: stale compaction reads as compacting")
	}
}

// Managed: serve's message.updated sets the handle's flag; the store has to agree; and every
// end — the summary completing, session.compacted / session.idle, a terminal turn state, a stop,
// the daemon going away, teardown — clears it.
func TestOpencodeManagedCompacting(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	db := newOpencodeLiveStore(t)
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, h.ses, h.dir); err != nil {
		t.Fatal(err)
	}
	insMsg(t, db, "u1", h.ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insMsg(t, db, "a1", h.ses, 2000, compactionRunning)
	m := session.Meta{Name: h.name, Dir: h.dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
	sids.Write(session.UUID(m.Dir, m.Name), h.ses)
	t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })

	state := func() string { return (agentImpl{}).WireLive(m, true).State }
	if got := state(); got != "working" {
		t.Fatalf("store alone (no event yet): state = %q, want working", got)
	}

	started := []byte(`{"payload":{"type":"message.updated","properties":{"sessionID":"ses_test","info":` +
		`{"id":"msg_c","sessionID":"ses_test","role":"assistant","mode":"compaction","agent":"compaction","summary":true,"cost":0,"time":{"created":2000}}}}}`)
	start := func(label string) {
		t.Helper()
		handleServeEvent(started)
		if got := state(); got != "compacting" {
			t.Fatalf("%s: after the compaction message.updated state = %q, want compacting", label, got)
		}
		if td, _ := readTranscript(m); !td.Compacting {
			t.Fatalf("%s: readTranscript Compacting = false, want true", label)
		}
	}
	cleared := func(label string) {
		t.Helper()
		if h.isCompacting() {
			t.Fatalf("%s: the handle is still compacting", label)
		}
		if got := state(); got == "compacting" {
			t.Fatalf("%s: state still compacting", label)
		}
	}

	start("summary completes")
	handleServeEvent([]byte(`{"payload":{"type":"message.updated","properties":{"sessionID":"ses_test","info":` +
		`{"id":"msg_c","sessionID":"ses_test","role":"assistant","mode":"compaction","agent":"compaction","summary":true,"cost":0.0055,"time":{"created":2000,"completed":2500}}}}}`))
	cleared("summary completes")

	start("the next step starts")
	handleServeEvent([]byte(`{"payload":{"type":"message.updated","properties":{"sessionID":"ses_test","info":` +
		`{"id":"msg_b","sessionID":"ses_test","role":"assistant","mode":"build","agent":"build","time":{"created":3000}}}}}`))
	cleared("the next step starts")

	start("user message updates do not clear")
	handleServeEvent([]byte(`{"payload":{"type":"message.updated","properties":{"sessionID":"ses_test","info":` +
		`{"id":"msg_u","sessionID":"ses_test","role":"user","time":{"created":1000}}}}}`))
	if !h.isCompacting() {
		t.Fatal("a user message.updated mid-compaction cleared the flag")
	}

	for _, typ := range []string{"session.compacted", "session.idle"} {
		start(typ)
		handleServeEvent([]byte(`{"payload":{"type":"` + typ + `","properties":{"sessionID":"ses_test"}}}`))
		cleared(typ)
	}

	for _, st := range []agents.TurnState{agents.TurnCompleted, agents.TurnFailed, agents.TurnAborted,
		agents.TurnCancelled, agents.TurnInterrupting, agents.TurnUnknown} {
		start(string(st))
		h.setState(st)
		cleared(string(st))
	}
	h.setState(agents.TurnRunning)
	start("running stays")
	h.setState(agents.TurnRunning)
	if !h.isCompacting() {
		t.Fatal("a running state cleared the flag")
	}

	start("stop")
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	cleared("stop")

	start("daemon lost")
	h.runtimeLost()
	if h.isCompacting() {
		t.Fatal("daemon lost: the handle is still compacting")
	}
	h.mu.Lock()
	h.alive = true
	h.mu.Unlock()

	start("teardown")
	DropHandle(h.name)
	if h.isCompacting() {
		t.Fatal("teardown: the handle is still compacting")
	}
	if got := state(); got == "compacting" {
		t.Fatalf("teardown: state = %q", got)
	}
}

// A subagent's compaction (its own child session id) is not the parent's.
func TestOpencodeManagedCompactingOtherSession(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	handleServeEvent([]byte(`{"payload":{"type":"message.updated","properties":{"sessionID":"ses_child","info":` +
		`{"id":"msg_c","sessionID":"ses_child","role":"assistant","mode":"compaction","agent":"compaction","time":{"created":2000}}}}}`))
	if h.isCompacting() {
		t.Fatal("a child session's compaction set the parent's flag")
	}
}

// Each assistant message carries its own cost (measured 1.18.34: per message, the session row
// holds the running total), and it reaches the turn so the usage fold can sum it once.
func TestOpencodeReadSessionCost(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_cost"
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"hi"}`)
	insMsg(t, db, "m2", ses, 2000, `{"role":"assistant","modelID":"big-pickle","cost":0.0187629,`+
		`"tokens":{"input":5858,"output":41,"cache":{"read":1913,"write":0}},"time":{"created":2000,"completed":2100}}`)
	insPart(t, db, "p2", "m2", ses, 1, `{"type":"text","text":"hello"}`)
	insPart(t, db, "p3", "m2", ses, 2, `{"type":"step-finish","cost":0.0187629}`)
	insMsg(t, db, "m3", ses, 3000, `{"role":"assistant","modelID":"big-pickle","cost":0,"time":{"created":3000,"completed":3100}}`)
	insPart(t, db, "p4", "m3", ses, 1, `{"type":"text","text":"free"}`)

	turns := readSession(db, ses)
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if turns[0].CostUSD != 0 || turns[1].CostUSD != 0.0187629 || turns[2].CostUSD != 0 {
		t.Fatalf("CostUSD = %v / %v / %v, want 0 / 0.0187629 / 0", turns[0].CostUSD, turns[1].CostUSD, turns[2].CostUSD)
	}
}

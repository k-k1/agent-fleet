package opencode

// Unit tests for the managed driver (docs/log/27 P2). serve is mocked with httptest, and the
// tests cover the turn state machine (Send → completed, queueing while busy, interrupt →
// cancelled, ledger idempotency) and SSE dispatch.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// --- pure mapping helpers ---

func TestNormalizeMsgID(t *testing.T) {
	if got := normalizeMsgID("msg_abc"); got != "msg_abc" {
		t.Errorf("msg_abc → %q", got)
	}
	if got := normalizeMsgID("abc-123"); got != "msg_af_abc-123" {
		t.Errorf("abc-123 → %q", got)
	}
	a, b := normalizeMsgID(""), normalizeMsgID("")
	if !strings.HasPrefix(a, "msg_af") || a == b {
		t.Errorf("generated ids must be msg_af-prefixed and unique: %q %q", a, b)
	}
}

func TestSplitModel(t *testing.T) {
	if p, m, ok := splitModel("anthropic/claude-sonnet-5"); !ok || p != "anthropic" || m != "claude-sonnet-5" {
		t.Errorf("got %q %q %v", p, m, ok)
	}
	// The openrouter form carries a / in the model id too, so split on the first / only.
	if p, m, ok := splitModel("openrouter/anthropic/claude"); !ok || p != "openrouter" || m != "anthropic/claude" {
		t.Errorf("got %q %q %v", p, m, ok)
	}
	for _, bad := range []string{"", "noslash", "/lead", "trail/"} {
		if _, _, ok := splitModel(bad); ok {
			t.Errorf("splitModel(%q) should not parse", bad)
		}
	}
}

func TestAgentForMode(t *testing.T) {
	if agentForMode("plan") != "plan" || agentForMode("normal") != "build" || agentForMode("") != "" {
		t.Error("mode mapping broken")
	}
}

func TestUpdateSettingsAndClear(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "openai/gpt-test", Effort: "high", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := h.Snapshot()
	if snap.Settings.Model != "openai/gpt-test" || snap.Settings.Effort != "high" || snap.Settings.Mode != "plan" {
		t.Fatalf("settings snapshot = %+v", snap.Settings)
	}
	if err := h.UpdateSettings(agents.ThreadSettings{ClearModel: true, ClearEffort: true}); err != nil {
		t.Fatal(err)
	}
	snap, _ = h.Snapshot()
	if snap.Settings.Model != "" || snap.Settings.Effort != "" || snap.Settings.Mode != "plan" {
		t.Fatalf("cleared settings snapshot = %+v", snap.Settings)
	}
}

// --- turn state machine against a mock serve ---

// mockServe fakes the opencode 2 endpoints the handle drives. A prompt only queues the input
// (like the real /prompt); the turn then runs until turnDelay passes or an interrupt arrives, and
// its end is an idle message carrying the outcome, which sendTurn reads back.
type mockServe struct {
	mu        sync.Mutex
	turns     []string // received prompt texts, in order
	clientIDs []string // messageIDs received (must stay empty - serve assigns them)
	busy      bool
	turnGate  chan struct{} // closed to end the running turn (a test may close it directly)
	// interruptedGate is the gate an interrupt closed: that turn ends "interrupted", while a
	// gate a test closes directly ends the turn the way it would have ended anyway.
	interruptedGate chan struct{}
	turnDelay       time.Duration
	// holdAbort answers the interrupt without releasing the turn, so a test can read the queue
	// while the pump is still parked in the aborted turn — once the turn returns, the pump may
	// already have taken what the interrupt left, and an empty queue proves nothing.
	holdAbort   bool
	statusPolls int // GET /api/session/active calls, so a test can tell the pump reached waitIdle
	// statusGate, when set, holds every GET /api/session/active until it is closed, and
	// statusEntered is signalled as each one arrives: the window of the pump's busy check.
	statusGate    chan struct{}
	statusEntered chan struct{}
	aborts        int
	// holdFirstAbort, when set, parks the first interrupt until it is closed (it is consumed by that call).
	holdFirstAbort chan struct{}
	// failAborts makes the next N interrupts answer 500 without releasing the turn.
	failAborts int
	// turnOutcome and turnErr are how a turn that runs to its end is recorded ("succeeded" when
	// empty; turnErr is a v2 error object). opencode reports a provider-side failure in the
	// execution record, not as an HTTP error, so a failing turn is simulated here.
	turnOutcome string
	turnErr     string
	msgs        []string // the session's messages, oldest first, as JSON
	seq         int
}

func newMockServe(t *testing.T) (*mockServe, *httptest.Server) {
	m := &mockServe{}
	oldPoll := v2PollEvery
	v2PollEvery = 10 * time.Millisecond
	t.Cleanup(func() { v2PollEvery = oldPoll })
	js := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session/active", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.statusPolls++
		gate, entered := m.statusGate, m.statusEntered
		m.mu.Unlock()
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		if gate != nil {
			<-gate
		}
		m.mu.Lock()
		busy := m.busy
		m.mu.Unlock()
		if busy {
			js(w, `{"data":{"ses_test":{"type":"running"}}}`)
			return
		}
		js(w, `{"data":{}}`)
	})
	mux.HandleFunc("POST /api/session/ses_test/prompt", func(w http.ResponseWriter, r *http.Request) {
		// No messageID is sent (serve assigns it), so a turn is identified by its prompt text.
		var body struct {
			MessageID string `json:"messageID"`
			Text      string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		if body.MessageID != "" {
			m.clientIDs = append(m.clientIDs, body.MessageID)
		}
		m.turns = append(m.turns, body.Text)
		m.busy = true
		gate := make(chan struct{})
		m.turnGate = gate
		delay := m.turnDelay
		m.seq++
		userID := fmt.Sprintf("msg_%06d", m.seq)
		m.msgs = append(m.msgs, fmt.Sprintf(`{"id":%q,"type":"user","text":%q}`, userID, body.Text))
		m.mu.Unlock()
		go func() {
			select {
			case <-gate:
			case <-time.After(delay):
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			interrupted := m.interruptedGate == gate
			outcome, e := m.turnOutcome, m.turnErr
			if outcome == "" {
				outcome = "succeeded"
			}
			if interrupted {
				outcome, e = "interrupted", ""
			}
			m.seq++
			idle := fmt.Sprintf(`{"id":"msg_%06d","type":"idle","outcome":%q`, m.seq, outcome)
			if e != "" {
				idle += `,"error":` + e
			}
			m.msgs = append(m.msgs, idle+"}")
			m.busy = false
		}()
		js(w, `{"data":{"id":"`+userID+`"}}`)
	})
	mux.HandleFunc("GET /api/session/ses_test/message", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		out := make([]string, 0, len(m.msgs))
		for i := len(m.msgs) - 1; i >= 0; i-- { // newest first
			out = append(out, m.msgs[i])
		}
		m.mu.Unlock()
		js(w, `{"data":[`+strings.Join(out, ",")+`]}`)
	})
	mux.HandleFunc("POST /api/session/ses_test/interrupt", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		if m.failAborts > 0 {
			m.failAborts--
			m.mu.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		hold := m.holdFirstAbort
		m.holdFirstAbort = nil
		m.mu.Unlock()
		if hold != nil {
			<-hold
		}
		m.mu.Lock()
		m.aborts++
		if m.turnGate != nil && !m.holdAbort {
			select {
			case <-m.turnGate:
			default:
				m.interruptedGate = m.turnGate
				close(m.turnGate)
			}
		}
		m.mu.Unlock()
		js(w, `{}`)
	})
	mux.HandleFunc("POST /api/session/ses_test/{setting}", func(w http.ResponseWriter, r *http.Request) {
		js(w, `{}`) // agent / model: session state the prompt does not carry
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv
}

func newTestHandle(t *testing.T, srv *httptest.Server) *threadHandle {
	t.Setenv("HOME", t.TempDir()) // isolate where status.Persist writes
	// Give every test its own sid. A turn runs in the pump goroutine and does not stop when
	// the test returns (TestQuestionFlow and TestRespondRejectOnCancel return with a
	// turnDelay=2s turn still running), so with a fixed sid the previous test's turn ends
	// inside a later one and feeds a transition for the same sid into the process-wide state
	// notification seam (agents.SetStateNotifier). Measured: under load
	// TestManagedTurnNotifiesCompletion fails with `transition = [sid-test idle idle]` or
	// `[sid-test  idle]`, roughly once in 8 runs.
	h := &threadHandle{
		name: "slot-test", dir: "/tmp", ocSid: "sid-" + t.Name(),
		addr: srv.URL, ses: "ses_test", alive: true, gen: 1,
		events: make(chan agents.Event, 64),
	}
	// The isolation above only holds if we wait. When a test returns with a turn still
	// running, the pump's MarkTurnEnd → status.Persist runs AFTER HOME is restored and writes
	// to the real `~/.config/agent-fleet` (measured: `sid-TestQuestionFlow.json` and
	// `sid-TestRespondRejectOnCancel.json` were left in the user's session-status/).
	// t.Cleanup is LIFO, so this wait, pushed after t.Setenv, runs before HOME is restored
	// (pushed before, it would run after - too late). The mock server's Close was pushed
	// earlier still in newMockServe, so it is alive while we wait.
	t.Cleanup(func() { waitPumpIdle(t, h) })
	return h
}

// waitPumpIdle blocks until the handle's pump has drained: nothing left in the queue
// (pumping=false) and no turn running (running=false). Failing is the safe direction -
// leaving without the wait dirties the real HOME, so give up loudly rather than silently.
func waitPumpIdle(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := h.pumping || h.running
		h.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("pump is still running after the test: restoring HOME now would write to the real ~/.config/agent-fleet")
}

func waitState(t *testing.T, h *threadHandle, want agents.TurnState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		st := h.state
		h.mu.Unlock()
		if st == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Fatalf("state = %s, want %s", h.state, want)
}

func TestSendCompletesTurn(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 50 * time.Millisecond
	h := newTestHandle(t, srv)
	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_one"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 || m.turns[0] != "hi" {
		t.Errorf("turns = %v", m.turns)
	}
	if len(m.clientIDs) != 0 {
		t.Errorf("messageID must not be sent to serve (sortable-id constraint) - got %v", m.clientIDs)
	}
}

func TestLedgerDedupesResend(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 30 * time.Millisecond
	h := newTestHandle(t, srv)
	for i := 0; i < 3; i++ {
		if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_dup"}); err != nil {
			t.Fatal(err)
		}
	}
	waitState(t, h, agents.TurnCompleted)
	time.Sleep(100 * time.Millisecond) // grace to confirm no extra turn started
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 {
		t.Errorf("resends must be idempotent — turns = %v", m.turns)
	}
}

func TestSteerQueuesBehindRunningTurn(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 300 * time.Millisecond
	h := newTestHandle(t, srv)
	if err := h.Send(agents.TurnInput{Prompt: "first", ClientMessageID: "msg_a"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "second", ClientMessageID: "msg_b"}); err != nil {
		t.Fatal(err)
	}
	if got := h.queuedPrompts(); len(got) != 1 || got[0] != "second" {
		t.Errorf("queuedPrompts = %v", got)
	}
	// Both turns run, in order.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.turns)
		m.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 2 || m.turns[0] != "first" || m.turns[1] != "second" {
		t.Errorf("turns = %v", m.turns)
	}
}

// A turn this handle did not start (an attached TUI's, or one a previous Agent process left
// running) holds the session too: the pump waits for it before the input goes out, so the input
// is reported as queued, and it still runs once the session goes idle.
func TestSendQueuedCountsATurnThisHandleDidNotStart(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	m.mu.Lock()
	m.busy = true
	m.mu.Unlock()
	queued, err := h.SendQueued(peerInput("from a peer", "msg_peer"))
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Error("input waiting behind another client's turn was reported as started")
	}
	m.mu.Lock()
	m.busy = false
	m.mu.Unlock()
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 || m.turns[0] != "from a peer" {
		t.Errorf("turns = %q, want the input sent once the session went idle", m.turns)
	}
}

// Agent shutdown interrupts through the teardown path: the whole queue goes, peer input
// included, and nothing is kept for return (ADR 0105 decision 8).
func TestAbortManagedDiscardsTheQueue(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 5 * time.Second
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "long", ClientMessageID: "msg_long"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	if err := h.Send(peerInput("from a peer", "msg_peer")); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.holdAbort = true
	m.mu.Unlock()
	AbortManaged()
	if got := h.queuedPrompts(); len(got) != 0 {
		t.Errorf("queue after shutdown interrupt = %v", got)
	}
	h.mu.Lock()
	kept := h.tq().Discards()
	h.mu.Unlock()
	if len(kept) != 0 {
		t.Errorf("teardown kept %d discards for return, want none (decision 8)", len(kept))
	}
	m.mu.Lock()
	m.holdAbort = false
	close(m.turnGate)
	m.mu.Unlock()
	waitState(t, h, agents.TurnCancelled)
	waitPumpIdle(t, h)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 {
		t.Errorf("turns = %q, want no turn started after the shutdown interrupt", m.turns)
	}
}

// registerTestHandle puts h into the package registry (what handleBySes resolves against) and
// removes it on cleanup so tests don't leak into each other.
func registerTestHandle(t *testing.T, h *threadHandle) {
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
}

// TestManagedEnrich checks that managedEnrich merges the driver's runtime state into the read
// layer's TranscriptData: the Interaction id (the address /respond takes) onto pending
// questions, the driver's own queue into the queued list, and the mode from the driver's
// settings (the value the next turn will use) into the mode chip (§10.2-5).
func TestManagedEnrich(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	h.mu.Lock()
	h.inter = &agents.Interaction{ID: "que_9", Kind: "question",
		Questions: []transcript.Question{{Question: "q1"}, {Question: "q2"}}}
	h.tq().Accept(memberInput("queued one", "msg_q1"))
	h.settings.Mode = "plan"
	h.mu.Unlock()

	m := session.Meta{Name: h.name, Dir: h.dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
	td := agents.TranscriptData{
		Pending: []transcript.Question{{Question: "q1"}}, // db-sourced (no id) gets overwritten
		Queued:  []string{"store queued"},
		Mode:    "normal",
	}
	managedEnrich(m, &td)
	if len(td.Pending) != 2 || td.Pending[0].ID != "que_9" || td.Pending[1].ID != "que_9" {
		t.Errorf("Pending = %+v, want 2 questions carrying que_9", td.Pending)
	}
	if len(td.Queued) != 2 || td.Queued[1] != "queued one" {
		t.Errorf("Queued = %v, want store queued + driver queue", td.Queued)
	}
	if len(td.QueuedItems) != 1 || td.QueuedItems[0].ID != "msg_q1" || td.QueuedItems[0].State != agents.EntryQueued {
		t.Errorf("QueuedItems = %+v, want the driver queue with ids", td.QueuedItems)
	}
	if td.Mode != "plan" {
		t.Errorf("Mode = %q, want plan (driver settings win over the db value)", td.Mode)
	}

	// tui meta (no driver) is left untouched.
	td2 := agents.TranscriptData{Mode: "normal"}
	managedEnrich(session.Meta{Name: h.name, Kind: session.KindOpencode}, &td2)
	if td2.Mode != "normal" || td2.Pending != nil {
		t.Errorf("tui meta must be untouched: %+v", td2)
	}
}

// TestManagedTurnNotifiesCompletion is the same regression as on the codex side: a managed
// session's turn completion must reach the state notification seam (agents.SetStateNotifier →
// package main's recordSessionNotification). A managed driver has no hooks, so it used to
// write status directly and tell nobody, which structurally suppressed the session report of
// docs/log/30.
func TestManagedTurnNotifiesCompletion(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 20 * time.Millisecond
	h := newTestHandle(t, srv)

	got := make(chan [3]string, 8)
	// The notification seam is process-wide. If a turn an earlier test left running (see the
	// comment in newTestHandle) ends inside this window, its transition arrives here too, so
	// take only the ones for our own sid.
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		if sid != h.ocSid {
			return
		}
		got <- [3]string{sid, previous, state}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_report"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnCompleted)

	// The notifier runs on its own goroutine (agents.notify is async on purpose), so it is
	// not ordered against the state we just waited for. Wait for the signal itself; the
	// ceiling only exists so a notification that never comes fails instead of hanging, and
	// is far above anything a loaded runner needs (measured: a 3 s budget was exceeded in a full
	// -p 2 run).
	notifyDeadline := time.NewTimer(30 * time.Second)
	defer notifyDeadline.Stop()
	select {
	case tr := <-got:
		if tr[0] != h.ocSid || tr[1] != "working" || tr[2] != "idle" {
			t.Fatalf("transition = %v, want %s working→idle", tr, h.ocSid)
		}
	case <-notifyDeadline.C:
		t.Fatal("turn completed without notifying the state seam - no report is sent")
	}
}

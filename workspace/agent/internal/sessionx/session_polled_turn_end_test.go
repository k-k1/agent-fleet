package sessionx

// The end of a turn on the four hook-less TUI kinds is observed by whoever polls
// (turn_end_poll.go, docs/log/89 §89.3). These tests pin the two halves of that split against
// each other, because the obvious implementation silently trades one for the other:
//
//   - the SESSIONS LIST must record WHEN the turn ended, on its own, with nobody calling
//     get_session_status (that lag is the defect being fixed);
//   - and doing so must NOT cost the completion notification / the operator's completion
//     report, which only DriveState fires. Recording an end as "persist idle" would settle the
//     state the notification gate reads (LiveState == "working"), and every one of the four
//     kinds would stop reporting completions — silently, with every unit test still green.
//
// copilot stands in for the four: its state source is a plain events.jsonl (state.go), so the
// whole route runs off files in the test's own HOME with no tmux and no CLI. agy / cursor /
// kiro reach the same two functions through the same two call sites.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// polledTurnEndFixture stands up a live copilot TUI session with a turn IN FLIGHT: the meta,
// the imposed session id a launch allocates, the events.jsonl the state is read from, and the
// optimistic "working" /input persists (which is the notification gate's precondition).
// turnEnded() appends the event that ends the turn.
func polledTurnEndFixture(t *testing.T, name string) (m session.Meta, sid string, turnEnded func()) {
	t.Helper()
	home := withTempHome(t)
	// COPILOT_HOME is unset in this container so ~/.copilot follows HOME, but pin it rather
	// than depend on that: the events file is what the state is read from, and a leak would
	// read the developer's real copilot sessions (isolateAgentConfigDirs' lesson).
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))

	m = session.Meta{
		Name: name, Kind: session.KindCopilot, Dir: t.TempDir(),
		Title: "copilot検証タスク", CreatedAt: time.Now().Format(time.RFC3339),
		Origin: session.OriginSession, OriginSession: "parent1",
	}
	session.WriteMeta(m)
	// The launch is what imposes the conversation id (`--session-id`), and both the state
	// source and the transcript are read under it. Going through the real BuildLaunch keeps
	// the fixture on copilot's own path instead of guessing at its layout.
	if _, err := AgentOf(session.KindCopilot).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatalf("BuildLaunch: %v", err)
	}
	cliSid := copilot.SessionID(m)
	if cliSid == "" {
		t.Fatal("the launch allocated no copilot session id; the state source cannot be addressed")
	}
	writeEvents := func(lines ...string) {
		t.Helper()
		p := copilot.EventsPath(cliSid)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A turn in flight: a user message with no assistant.turn_end after it (state.go).
	writeEvents(`{"type":"user.message"}`)
	sid = session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working") // what POST /input persists; copilot has no hook to do it
	return m, sid, func() {
		t.Helper()
		writeEvents(`{"type":"user.message"}`, `{"type":"assistant.turn_end"}`)
	}
}

// The listing alone has to record the end. Before this, MarkTurnEnd was fired only from
// DriveState, so a child driven in its TUI carried no lastTurnEndAt until somebody called
// get_session_status on it — a parent that only polls list_child_sessions (the intended use,
// ADR 0073 decision 9) never saw its child finish.
func TestSessionsListRecordsAPolledTurnEndWithoutGetSessionStatus(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_listing")

	if got := wireSession(m, true); got.LastTurnEndAt != "" {
		t.Fatalf("a session mid-turn reported lastTurnEndAt=%q; only an END may write one", got.LastTurnEndAt)
	}

	turnEnded()
	got := wireSession(m, true)
	if got.State != "idle" {
		t.Fatalf("state = %q, want idle (the fixture's events say the turn ended)", got.State)
	}
	if got.LastTurnEndAt == "" {
		t.Fatal("the sessions list did not record the turn end; a parent polling the list still cannot tell its child finished")
	}

	// The trap: recording must leave the state machine exactly as it was, or the very next
	// poll of DriveState finds an idle where it needs "working" and the completion is never
	// notified or reported (TestPolledTurnEndStillReportsExactlyOnceAfterTheListingRecordedIt
	// is the other side of this).
	st, ok := status.Read(sid)
	if !ok || st.State != "working" || st.TurnEnd {
		t.Fatalf("the listing changed the state machine: %+v — the notification gate is consumed", st)
	}

	// The listing must not write the status file AT ALL — not even to add a field to it. That
	// write is what raced the notification route's (status.ObservedTurnEnd), and it is also
	// what a read route polled for every session in the workspace has no business doing.
	// "Recorded once per turn" itself is pinned in the status package, which can see the
	// observation store's own mtime.
	at, ok := status.StateAt(sid)
	if !ok {
		t.Fatal("no status file")
	}
	time.Sleep(10 * time.Millisecond)
	if again := wireSession(m, true); again.LastTurnEndAt != got.LastTurnEndAt {
		t.Errorf("lastTurnEndAt moved between polls: %q → %q", got.LastTurnEndAt, again.LastTurnEndAt)
	}
	if at2, _ := status.StateAt(sid); !at2.Equal(at) {
		t.Errorf("a listing poll wrote the status file (%v → %v)", at, at2)
	}
}

// The regression guard. The four TUI kinds have no Stop hook, so DriveState is the ONLY place
// their completion becomes a notification and consumes the operator's completion-report arm
// (docs/log/30 ②). It has to keep doing that — exactly once — after the listing has already
// observed and recorded the same end.
func TestPolledTurnEndStillReportsExactlyOnceAfterTheListingRecordedIt(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_report")

	fired := make(chan [2]string, 8)
	agents.SetStateNotifier(func(_, previous, state, _ string) {
		fired <- [2]string{previous, state}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	turnEnded()
	// The Console polls the list for every session all the time; it gets there first.
	recorded := ""
	for i := 0; i < 3; i++ {
		got := wireSession(m, true)
		if got.LastTurnEndAt == "" {
			t.Fatalf("poll %d: the listing did not record the end", i)
		}
		recorded = got.LastTurnEndAt
	}
	// … and only then does something ask for this session's state (get_session_status, the
	// chat chip, the mirror).
	if st := DriveState(m, true, false); st != "idle" {
		t.Fatalf("DriveState = %q, want idle", st)
	}

	select {
	case got := <-fired:
		// The transition itself matters: recordSessionNotification decides what to emit from
		// (previous, state), so a completion recorded off previous="idle" is not counted.
		if got[0] != "working" || got[1] != "idle" {
			t.Fatalf("notified transition = %v, want working→idle", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the turn's completion was never notified — the listing consumed the end and the operator's report never arrives")
	}
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("status after the completion = %+v, want the settled end of turn (idle + TurnEnd)", st)
	}
	// Both routes describe the SAME end, so the time may not move when the second one lands:
	// a parent watching lastTurnEndAt reads a moved timestamp as a second turn finishing.
	if again := wireSession(m, true).LastTurnEndAt; again != recorded {
		t.Errorf("lastTurnEndAt moved %q → %q when the notification route settled the same turn", recorded, again)
	}

	// Exactly once: neither route may fire a second time for the same turn.
	for i := 0; i < 3; i++ {
		wireSession(m, true)
		DriveState(m, true, false)
	}
	select {
	case got := <-fired:
		t.Fatalf("the same turn end was notified twice (%v); the operator gets two completion reports", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// The whole point of the notification is the card the operator reads (docs/log/30), so take it
// all the way there for a TUI kind whose end was recorded by the listing first: the arm is
// consumed and exactly one 【セッション報告】 lands in the conversation.
//
// ⚠️ This is NOT the guard against recording an end as "persist idle" — measured: with that
// mutation in place this test stays GREEN, because the docs/log/51 reconciler reads the settled
// idle+TurnEnd marker and delivers the report by its compensation route (which is itself the
// defect §89.3 refused: the sessions list would then be firing operator reports). The test above
// is the guard, because it watches the notification seam MarkTurnEnd fires.
func TestPolledTurnEndDeliversTheOperatorReportAfterTheListingRecordedIt(t *testing.T) {
	m, _, turnEnded := polledTurnEndFixture(t, "copilot_card")
	convID := armedOperatorConversation(t, m)

	turnEnded()
	if got := wireSession(m, true); got.LastTurnEndAt == "" {
		t.Fatal("the listing did not record the end")
	}
	if st := DriveState(m, true, false); st != "idle" {
		t.Fatalf("DriveState = %q, want idle", st)
	}

	card := awaitReportCard(t, convID)
	if card == nil {
		t.Fatal("no session report reached the operator's conversation")
	}
	if card.Session != m.Name || !strings.Contains(card.Content, "copilot検証タスク") {
		t.Fatalf("report card = %+v", card)
	}
	awaitReported(t, m.Name)

	// One instruction, one report: another round of polls must not produce a second card.
	for i := 0; i < 3; i++ {
		wireSession(m, true)
		DriveState(m, true, false)
	}
	time.Sleep(200 * time.Millisecond)
	unlock := chatx.LockConv(convID)
	c, err := chatx.LoadConv(convID)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, msg := range c.Messages {
		if msg.Role == "report" {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("%d report cards for one turn, want 1", reports)
	}
}

// Only the kinds whose end of turn can be observed BY A POLL may be recorded this way. For a
// kind that has a hook (or a managed driver) reporting its own end, an idle read off the wire
// while the store still says working is exactly the "idle nobody can explain" the TurnEnd bit
// exists to keep out of the answer — claude reaches it through the pane self-heal — and a
// parent polling its child reads any timestamp here as evidence of completion (docs/log/51).
func TestPolledTurnEndIgnoresKindsThatReportTheirOwnEnd(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "claude_child", Kind: session.KindClaude, Dir: t.TempDir()}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	status.Persist(sid, "working")

	recordPolledTurnEnd(m, "idle")

	if got := status.ObservedTurnEnd(sid); got != "" {
		t.Fatalf("an unexplained idle was recorded as the end of a turn (%q)", got)
	}
}

// armedOperatorConversation wires the report route exactly as main() does (the notifier, the
// reconciler tick and the real /chat/report endpoint) and arms one operator instruction against
// m. Same shape as managedReportFixture, for a session that is TUI rather than managed.
func armedOperatorConversation(t *testing.T, m session.Meta) string {
	t.Helper()
	// The auto turn would call a real provider; what's under test is the report card.
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", chatx.HandleChatReport)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatx.ChatConversation{ID: chatx.RandUUID(), Agent: "claude", Messages: []chatx.ChatMessage{}}
	if err := chatx.SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	agents.SetStateNotifier(RecordSessionNotification)
	t.Cleanup(func() { agents.SetStateNotifier(nil) })
	chatx.AddInstruction(m.Name, conv.ID, TurnSourceOperator)
	return conv.ID
}

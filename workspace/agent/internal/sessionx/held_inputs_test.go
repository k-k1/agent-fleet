package sessionx

// #1257: an operator or scheduled prompt sent to a Managed session is tied to its instruction
// row and its scheduled run, so a drop before it runs is reported on both.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// queueFakeHandle is a Managed handle whose accept is a real TurnQueue with a turn running, so
// what /input sends is held exactly as a driver holds it.
type queueFakeHandle struct {
	agents.ThreadHandle
	mu   sync.Mutex
	q    *agents.TurnQueue
	fail error
}

func (h *queueFakeHandle) Send(in agents.TurnInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail != nil {
		return h.fail
	}
	h.q.Accept(in)
	return nil
}

func useQueueFake(t *testing.T, name string) *queueFakeHandle {
	t.Helper()
	useOriginFake(t, name) // the session, and the driver slot restored on cleanup
	h := &queueFakeHandle{q: agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)}
	h.q.Accept(agents.TurnInput{Prompt: "running", Origin: agents.Origin{Kind: agents.OriginMember}})
	run := h.q.Take()
	h.q.Commit(run)
	h.q.Received(run)
	managedDrivers[session.KindCodex] = &queueFakeDriver{h: h}
	return h
}

type queueFakeDriver struct {
	agents.Driver
	h *queueFakeHandle
}

func (d *queueFakeDriver) Resume(session.Meta) (agents.ThreadHandle, error) { return d.h, nil }

func newConv(t *testing.T) string {
	t.Helper()
	c := &chatx.ChatConversation{ID: chatx.RandUUID(), Agent: "claude", Messages: []chatx.ChatMessage{}}
	if err := chatx.SaveConv(c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// captureScheduleNotRun installs the drop hook with the Control Plane call replaced.
func captureScheduleNotRun(t *testing.T) *[]agents.HeldDrop {
	t.Helper()
	var mu sync.Mutex
	var got []agents.HeldDrop
	prevFn, prevHook := recordScheduleNotRunFn, agents.OnHeldDropped
	recordScheduleNotRunFn = func(d agents.HeldDrop) { mu.Lock(); got = append(got, d); mu.Unlock() }
	InstallHeldDropHook()
	t.Cleanup(func() {
		scheduleNotRuns.Wait()
		recordScheduleNotRunFn, agents.OnHeldDropped = prevFn, prevHook
	})
	return &got
}

func TestManagedOperatorInputIsTiedToItsRowAndReportedWhenDropped(t *testing.T) {
	h := useQueueFake(t, "held_dst")
	conv := newConv(t)
	sched := captureScheduleNotRun(t)

	rec := postInput(t, "held_dst", `{"prompt":"nightly","report_to":"`+conv+`","source":"schedule",`+
		`"schedule_id":"sch1","schedule_slot":"2026-10-03T03:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	items := h.q.Items()
	if len(items) != 1 {
		t.Fatalf("queue = %+v", items)
	}
	msg := items[0].ID
	rows := chatx.ReadInstrRows("held_dst")
	if len(rows) != 1 || rows[0].Msg != msg || rows[0].Source != TurnSourceSchedule {
		t.Fatalf("instruction rows = %+v, want one naming %s", rows, msg)
	}
	if !agents.HeldWaiting("held_dst", msg) {
		t.Fatal("the scheduled prompt is not held")
	}

	h.q.DropAll() // halt: the prompt waits on disk
	agents.DropHeld("held_dst", agents.DropArchived)
	scheduleNotRuns.Wait()
	rows = chatx.ReadInstrRows("held_dst")
	if rows[0].Dropped != agents.DropArchived {
		t.Fatalf("row after the archive = %+v", rows[0])
	}
	if len(*sched) != 1 || (*sched)[0].Schedule != (agents.ScheduleRef{ID: "sch1", Slot: "2026-10-03T03:00:00Z"}) ||
		(*sched)[0].Reason != agents.DropArchived || (*sched)[0].Session != "held_dst" {
		t.Fatalf("schedule not-run calls = %+v", *sched)
	}
}

// A send the driver refuses withdraws the row raised for it: there is no prompt to report on.
func TestManagedOperatorInputRefusedWithdrawsTheRow(t *testing.T) {
	h := useQueueFake(t, "held_refused")
	conv := newConv(t)
	h.fail = errors.New("runtime gone")
	if rec := postInput(t, "held_refused", `{"prompt":"do it","report_to":"`+conv+`"}`); rec.Code == http.StatusOK {
		t.Fatalf("a refused send answered 200: %s", rec.Body.String())
	}
	if rows := chatx.ReadInstrRows("held_refused"); len(rows) != 0 {
		t.Fatalf("rows after a refused send = %+v", rows)
	}
}

// A schedule id is kept only for a schedule source.
func TestScheduleRefOf(t *testing.T) {
	if r := scheduleRefOf("", "sch1", "2026-10-03T03:00:00Z"); r != (agents.ScheduleRef{}) {
		t.Fatalf("operator send kept %+v", r)
	}
	if r := scheduleRefOf(TurnSourceScheduleManual, "sch1", "garbage"); r != (agents.ScheduleRef{ID: "sch1"}) {
		t.Fatalf("bad slot = %+v", r)
	}
}

// A held operator prompt delivered after a restart carries a queue-time mark; the mirror still
// badges it as the operator's.
func TestHeldMarkKeepsTheBadge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordInjection("held_badge", "do the thing", TurnSourceOperator)
	turns := []transcript.Turn{{Role: "user",
		Text: agents.MarkHeldInstruction("do the thing", time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC))}}
	tagInjectedTurns("held_badge", turns)
	if turns[0].Source != TurnSourceOperator {
		t.Fatalf("source = %q, want operator", turns[0].Source)
	}
}

// holdInputs queues one input of each held origin behind a running turn and tears the queue
// down, as a halt leaves them: on disk for the next start.
func holdInputs(t *testing.T, name string) {
	t.Helper()
	q := agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)
	q.Accept(agents.TurnInput{Prompt: "running", Origin: agents.Origin{Kind: agents.OriginMember}})
	q.Take()
	for _, k := range []string{agents.OriginOperator, agents.OriginPeer, agents.OriginSchedule} {
		q.Accept(agents.TurnInput{Prompt: k, ClientMessageID: k, Origin: agents.Origin{Kind: k}})
	}
	q.DropAll()
}

func postHalt(t *testing.T, name, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/halt", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleHaltSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("halt: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The Console's halt keeps every held input for the next start; the operator's stop_session
// (disarm_report) withdraws its own prompts and keeps the peer's and the schedule's.
func TestHaltKeepsHeldInputsAndStopSessionWithdrawsTheOperators(t *testing.T) {
	fakeTmux(t)
	t.Setenv("HOME", t.TempDir())
	const name = "held_stop"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	holdInputs(t, name)
	postHalt(t, name, "")
	if n := agents.HeldCount(name); n != 3 {
		t.Fatalf("held after the Console's halt = %d, want 3", n)
	}
	postHalt(t, name, `{"disarm_report":true}`)
	if n := agents.HeldCount(name); n != 2 || agents.HeldWaiting(name, agents.OriginOperator) {
		t.Fatalf("held after stop_session = %d (operator still waiting: %v), want the peer and the schedule",
			n, agents.HeldWaiting(name, agents.OriginOperator))
	}
}

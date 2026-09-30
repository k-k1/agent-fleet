package sessionx

// ADR 0105's wire, read from the fixture the Console tests read too
// (testdata/stop-queue-wire.json): /turn's interrupt / remove / dismiss_discard and the
// messages payload's queuedItems / discardedInputs. Bodies are compared whole; only prose
// (error.message) and the session name echoed in "sent" are normalised away.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

type stopWireFixture struct {
	Turn map[string]struct {
		Request  json.RawMessage `json:"request"`
		Status   int             `json:"status"`
		Response json.RawMessage `json:"response"`
	} `json:"turn"`
	Messages map[string]map[string]json.RawMessage `json:"messages"`
}

func loadStopWireFixture(t *testing.T) stopWireFixture {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/stop-queue-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx stopWireFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// stopWireHandle answers the three ADR 0105 verbs from canned values and records what it was
// asked. Every other ThreadHandle method is the nil embedded interface: a call to one panics,
// which is the point — these ops must reach nothing else.
type stopWireHandle struct {
	agents.ThreadHandle
	interrupt    agents.InterruptResult
	interruptErr error
	removed      agents.QueueItem
	removeErr    error
	dismissed    bool

	opts        []agents.InterruptOpts
	removeIDs   []string
	dismissIDs  []string
	transcript  agents.TranscriptData // what the messages route reads back from the driver
	resumeCalls int
}

func (h *stopWireHandle) Interrupt(o agents.InterruptOpts) (agents.InterruptResult, error) {
	h.opts = append(h.opts, o)
	return h.interrupt, h.interruptErr
}

func (h *stopWireHandle) RemoveQueued(id string) (agents.QueueItem, error) {
	h.removeIDs = append(h.removeIDs, id)
	return h.removed, h.removeErr
}

func (h *stopWireHandle) DismissDiscard(id string) bool {
	h.dismissIDs = append(h.dismissIDs, id)
	return h.dismissed
}

type stopWireDriver struct {
	agents.Driver
	h *stopWireHandle
}

func (d *stopWireDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	d.h.resumeCalls++
	return d.h, nil
}

// stopWireAgent serves the fake handle's TranscriptData through the registry, the way a real
// driver's Agent.Transcript reads its live handle.
type stopWireAgent struct {
	agents.Agent
	h *stopWireHandle
}

func (a stopWireAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return a.h.transcript, true
}

// useStopWire installs the fake as muse's driver and agent, with the sessions and HOME isolated.
func useStopWire(t *testing.T) *stopWireHandle {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	h := &stopWireHandle{}
	prev, had := managedDrivers[session.KindMuse]
	managedDrivers[session.KindMuse] = &stopWireDriver{h: h}
	t.Cleanup(func() {
		if had {
			managedDrivers[session.KindMuse] = prev
			return
		}
		delete(managedDrivers, session.KindMuse)
	})
	withFakeAgent(t, session.KindMuse, stopWireAgent{h: h})
	return h
}

// normalizeWire drops what the fixture says not to compare: error.message and the session
// name in "sent".
func normalizeWire(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %s (%v)", raw, err)
	}
	if e, ok := m["error"].(map[string]any); ok {
		e["message"] = "prose"
	}
	if _, ok := m["sent"]; ok {
		m["sent"] = "<name>"
	}
	return m
}

func decodeInto[T any](t *testing.T, raw json.RawMessage, key string) T {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var v T
	if b, ok := env[key]; ok {
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	return v
}

// stopWireTurnCases is how each fixture case is staged. Managed cases set the fake's canned
// answers from the fixture itself; the tui cases only pick the route. wantCall says which
// verb must have reached the driver, so a case that returns the right body without asking the
// driver at all cannot pass.
var stopWireTurnCases = map[string]struct {
	tui      bool
	stage    func(t *testing.T, h *stopWireHandle, resp json.RawMessage)
	wantCall string // "interrupt" | "remove" | "dismiss" | "" (the driver is never asked)
}{
	"interrupt_first":                        {stage: stageInterrupt, wantCall: "interrupt"},
	"interrupt_second":                       {stage: stageInterrupt, wantCall: "interrupt"},
	"interrupt_discard_queue":                {stage: stageInterrupt, wantCall: "interrupt"},
	"interrupt_discard_queue_nothing_queued": {stage: stageInterrupt, wantCall: "interrupt"},
	"interrupt_tui":                          {tui: true},
	"interrupt_discard_queue_tui":            {tui: true},
	"remove": {wantCall: "remove", stage: func(t *testing.T, h *stopWireHandle, resp json.RawMessage) {
		h.removed = decodeInto[agents.QueueItem](t, resp, "removed")
	}},
	// Wrapped, so the handler has to use errors.Is and not compare.
	"remove_already_started": {wantCall: "remove", stage: func(t *testing.T, h *stopWireHandle, _ json.RawMessage) {
		h.removeErr = fmt.Errorf("cm_a: %w", agents.ErrAlreadyStarted)
	}},
	"remove_not_queued": {wantCall: "remove", stage: func(t *testing.T, h *stopWireHandle, _ json.RawMessage) {
		h.removeErr = fmt.Errorf("cm_gone: %w", agents.ErrNotQueued)
	}},
	"remove_missing_id": {},
	"remove_tui":        {tui: true},
	"dismiss_discard": {wantCall: "dismiss", stage: func(t *testing.T, h *stopWireHandle, _ json.RawMessage) {
		h.dismissed = true
	}},
	"dismiss_discard_gone": {wantCall: "dismiss"},
	"dismiss_discard_tui":  {tui: true},
}

func stageInterrupt(t *testing.T, h *stopWireHandle, resp json.RawMessage) {
	h.interrupt = agents.InterruptResult{
		Stop:    decodeInto[agents.StopKind](t, resp, "stop"),
		Discard: decodeInto[*agents.Discard](t, resp, "discard"),
	}
}

func TestTurnStopQueueWireFixture(t *testing.T) {
	fx := loadStopWireFixture(t)
	ran := map[string]bool{}
	for name, c := range fx.Turn {
		sc, ok := stopWireTurnCases[name]
		if !ok {
			t.Errorf("fixture case %q has no scenario here: stage it, or the fixture moved under this test", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			const sess = "fixture-session"
			h := useStopWire(t)
			var tmuxLog string
			meta := session.Meta{Name: sess, Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged,
				StopAfterTurnAt: "2026-09-30T00:00:00Z"}
			if sc.tui {
				tmuxLog = fakeTmux(t)
				meta.Kind, meta.Driver = session.KindCodex, ""
			}
			session.WriteMeta(meta)
			if sc.stage != nil {
				sc.stage(t, h, c.Response)
			}

			rec := postTurn(t, sess, string(c.Request))
			if rec.Code != c.Status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, c.Status, rec.Body.String())
			}
			got, want := normalizeWire(t, rec.Body.Bytes()), normalizeWire(t, c.Response)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body mismatch\n got %s\nwant %s", rec.Body.String(), c.Response)
			}

			// What reached the driver, and with what.
			var req struct {
				DiscardQueue bool   `json:"discard_queue"`
				ID           string `json:"id"`
			}
			_ = json.Unmarshal(c.Request, &req)
			calls := map[string]int{"interrupt": len(h.opts), "remove": len(h.removeIDs), "dismiss": len(h.dismissIDs)}
			for verb, n := range calls {
				if verb != sc.wantCall && n != 0 {
					t.Errorf("%s reached the driver %d times, want 0", verb, n)
				}
			}
			switch sc.wantCall {
			case "interrupt":
				if len(h.opts) != 1 || h.opts[0].DiscardQueue != req.DiscardQueue {
					t.Errorf("Interrupt opts = %+v, want one call with DiscardQueue=%v", h.opts, req.DiscardQueue)
				}
			case "remove":
				if !reflect.DeepEqual(h.removeIDs, []string{req.ID}) {
					t.Errorf("RemoveQueued ids = %v, want [%s]", h.removeIDs, req.ID)
				}
			case "dismiss":
				if !reflect.DeepEqual(h.dismissIDs, []string{req.ID}) {
					t.Errorf("DismissDiscard ids = %v, want [%s]", h.dismissIDs, req.ID)
				}
			}

			// A stop, a removal and a dismissal start no turn: they must not mark the session
			// working or release a stop-after-turn arm (docs/log/85).
			if st, ok := status.Read(session.UUID(meta.Dir, sess)); ok && st.State == "working" {
				t.Error("the op marked the session working")
			}
			if m, _ := session.ReadMeta(sess); m.StopAfterTurnAt == "" {
				t.Error("the op cancelled the stop-after-turn arm")
			}
			if sc.tui {
				log, _ := os.ReadFile(tmuxLog)
				sentEscape := strings.Contains(string(log), "send-keys -t %7 Escape")
				if wantEscape := name == "interrupt_tui"; sentEscape != wantEscape {
					t.Errorf("Escape sent = %v, want %v (tmux log %q)", sentEscape, wantEscape, log)
				}
			}
			ran[name] = true
		})
	}
	// Every case ran: a scenario the fixture no longer names would otherwise pass unnoticed.
	for name := range stopWireTurnCases {
		if !ran[name] {
			t.Errorf("scenario %q never ran against a fixture case", name)
		}
	}
	if len(ran) != len(fx.Turn) || len(ran) < 14 {
		t.Fatalf("ran %d of %d fixture turn cases", len(ran), len(fx.Turn))
	}
}

// The harness has to be able to see the arm being released, or the "does not cancel" check
// above proves nothing: a steer is new work and must release it.
func TestTurnStopWireHarnessSeesAnArmBeingReleased(t *testing.T) {
	h := useStopWire(t)
	h.ThreadHandle = &sendOnlyHandle{}
	meta := session.Meta{Name: "arm-control", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged,
		StopAfterTurnAt: "2026-09-30T00:00:00Z"}
	session.WriteMeta(meta)
	if rec := postTurn(t, meta.Name, `{"op":"steer","prompt":"more"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if m, _ := session.ReadMeta(meta.Name); m.StopAfterTurnAt != "" {
		t.Error("a steer left the stop-after-turn arm in place")
	}
	if st, ok := status.Read(session.UUID(meta.Dir, meta.Name)); !ok || st.State != "working" {
		t.Errorf("a steer did not mark the session working: %+v", st)
	}
}

type sendOnlyHandle struct{ agents.ThreadHandle }

func (*sendOnlyHandle) Steer(agents.TurnInput) error { return nil }

// A dismissal names a kept discard; without an id there is nothing to name. It is refused
// like remove, and never reaches the driver.
func TestTurnDismissDiscardNeedsAnID(t *testing.T) {
	h := useStopWire(t)
	session.WriteMeta(session.Meta{Name: "dismiss-noid", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged})
	rec := postTurn(t, "dismiss-noid", `{"op":"dismiss_discard"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing_id") {
		t.Fatalf("status = %d, body = %s, want 400 missing_id", rec.Code, rec.Body.String())
	}
	if len(h.dismissIDs) != 0 {
		t.Error("an empty id reached the driver")
	}
}

// A driver failure that is neither of remove's two sentinels is a runtime error, not a 404/409
// the Console would read as "the entry is gone".
func TestTurnRemoveOtherErrorsAreRuntimeErrors(t *testing.T) {
	h := useStopWire(t)
	h.removeErr = errors.New("boom")
	session.WriteMeta(session.Meta{Name: "remove-boom", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged})
	rec := postTurn(t, "remove-boom", `{"op":"remove","id":"cm_x"}`)
	if rec.Code == http.StatusOK || rec.Code == http.StatusConflict || rec.Code == http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want a runtime error", rec.Code, rec.Body.String())
	}
}

// The three ADR 0105 keys of GET /messages, one fixture case at a time. The fake handle holds
// the TranscriptData the case describes; the response has to carry each key exactly as the
// fixture spells it, and no key the case leaves out.
func TestMessagesStopQueueWireFixture(t *testing.T) {
	fx := loadStopWireFixture(t)
	// How the case is polled. The fixture's own note says queuedItems follows queuedPrompts'
	// gate (alive and working) and discardedInputs follows nothing.
	polled := map[string]struct {
		alive bool
		state string
	}{
		"working":                  {true, "working"},
		"held_by_the_runtime":      {true, "working"},
		"idle_after_a_second_stop": {true, "idle"},
	}
	keys := []string{"queuedPrompts", "queuedItems", "discardedInputs"}
	ran := 0
	for name, want := range fx.Messages {
		p, ok := polled[name]
		if !ok {
			t.Errorf("fixture messages case %q has no scenario here", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			h := useStopWire(t)
			raw, _ := json.Marshal(want)
			h.transcript = agents.TranscriptData{
				Queued:      decodeInto[[]string](t, raw, "queuedPrompts"),
				QueuedItems: decodeInto[[]agents.QueueItem](t, raw, "queuedItems"),
				Discards:    decodeInto[[]agents.Discard](t, raw, "discardedInputs"),
			}
			m := session.Meta{Name: "msg-fixture", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged}
			session.WriteMeta(m)

			rec := httptest.NewRecorder()
			handleGenericMessages(rec, httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil), m, p.alive, p.state)
			var resp map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			for _, k := range keys {
				gotRaw, gotOK := resp[k]
				wantRaw, wantOK := want[k]
				if gotOK != wantOK {
					t.Errorf("%s present = %v, fixture says %v", k, gotOK, wantOK)
					continue
				}
				if !gotOK {
					continue
				}
				var g, w any
				_ = json.Unmarshal(gotRaw, &g)
				_ = json.Unmarshal(wantRaw, &w)
				if !reflect.DeepEqual(g, w) {
					t.Errorf("%s mismatch\n got %s\nwant %s", k, gotRaw, wantRaw)
				}
			}
			ran++
		})
	}
	if ran != len(fx.Messages) || ran < 3 {
		t.Fatalf("ran %d of %d fixture messages cases", ran, len(fx.Messages))
	}
}

// Where the two new keys part ways: queuedItems is only meaningful while a turn runs (it
// hides a stale leftover, like queuedPrompts), discardedInputs is what a member sees after the
// turn went idle, or the session stopped.
func TestMessagesStopQueueKeysGates(t *testing.T) {
	item := agents.QueueItem{ID: "cm_a", Text: "next", Origin: agents.Origin{Kind: agents.OriginMember}, State: agents.EntryQueued}
	discard := agents.Discard{ID: "dsc_1", At: "2026-09-30T12:00:00Z", Reason: agents.DiscardSecondStop,
		Items: []agents.QueueItem{{ID: "cm_x", Text: "old", Origin: agents.Origin{Kind: agents.OriginMember}}}}
	cases := []struct {
		name                   string
		alive                  bool
		state                  string
		wantItems, wantDiscard bool
	}{
		{"working", true, "working", true, true},
		{"idle", true, "idle", false, true},
		{"question", true, "question", false, true},
		{"stopped", false, "stopped", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := useStopWire(t)
			h.transcript = agents.TranscriptData{
				Queued: []string{"next"}, QueuedItems: []agents.QueueItem{item}, Discards: []agents.Discard{discard},
			}
			m := session.Meta{Name: "msg-gates", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged}
			session.WriteMeta(m)
			rec := httptest.NewRecorder()
			handleGenericMessages(rec, httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil), m, tc.alive, tc.state)
			var resp map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if _, ok := resp["queuedItems"]; ok != tc.wantItems {
				t.Errorf("queuedItems present = %v, want %v", ok, tc.wantItems)
			}
			if _, ok := resp["queuedPrompts"]; ok != tc.wantItems {
				t.Errorf("queuedPrompts present = %v, want %v (the two share one gate)", ok, tc.wantItems)
			}
			if _, ok := resp["discardedInputs"]; ok != tc.wantDiscard {
				t.Errorf("discardedInputs present = %v, want %v", ok, tc.wantDiscard)
			}
		})
	}
}

package codex

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sleepTerminal is the entry a live 0.159.2 app-server listed for a `sleep 45` the model left
// running (measured; ids shortened).
var sleepTerminal = map[string]any{
	"itemId": "exec-062a2ed8", "processId": "62110", "command": "sleep 45; echo done",
	"cwd": "/work", "osPid": nil, "cpuPercent": nil, "rssKb": nil,
}

// waitCodexBg polls BackgroundWork until it answers want. Reads never block on the app-server
// — a stale cache answers and starts a refresh — so the answer arrives a read or two later.
func waitCodexBg(t *testing.T, name string, want bool) string {
	t.Helper()
	deadline := time.Now().Add(waitBackstop)
	for {
		busy, reason := BackgroundWork(name)
		if busy == want {
			return reason
		}
		if time.Now().After(deadline) {
			t.Fatalf("BackgroundWork(%s) = %v, want %v", name, busy, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func managedMeta(t *testing.T, h *threadHandle) session.Meta {
	t.Helper()
	m := session.Meta{Kind: session.KindCodex, Name: h.name, Dir: h.dir, Driver: session.DriverManaged}
	h.slotSid = session.UUID(m.Dir, m.Name) // WireLive reads the status store under this sid
	return m
}

// A command the turn left running shows as background work on the session list once the
// turn is over, and stops showing once the app-server no longer lists it.
func TestBackgroundTerminalAfterTheTurnIsBackgroundBusy(t *testing.T) {
	old := bgTTL
	bgTTL = 0 // every read asks again, so the exit is seen without waiting out a window
	t.Cleanup(func() { bgTTL = old })

	m, cl := newMockCodexServer(t)
	m.autoComplete = true
	m.bgTerminals = []map[string]any{sleepTerminal}
	h := newCodexTestHandle(t, cl, "codex-bg")
	meta := managedMeta(t, h)
	registerCodexTestHandle(t, h)

	if err := h.Send(agents.TurnInput{Prompt: "start it", ClientMessageID: "af_bg"}); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnCompleted)
	// The turn's end asks by itself, so the first list poll after it already has an answer.
	waitCodexCalls(t, m, "thread/backgroundTerminals/list", 1)
	if reason := waitCodexBg(t, h.name, true); reason != bgReasonShell {
		t.Errorf("reason = %q, want %q", reason, bgReasonShell)
	}
	li := New().WireLive(meta, true)
	if li.State != "idle" || !li.BackgroundBusy || li.BackgroundBusyReason != bgReasonShell {
		t.Errorf("WireLive = state %q busy %v reason %q, want idle + busy shell", li.State, li.BackgroundBusy, li.BackgroundBusyReason)
	}
	params, _ := m.lastCall("thread/backgroundTerminals/list")
	if string(params) != `{"threadId":"thr_test"}` {
		t.Errorf("list params = %s", params)
	}

	m.mu.Lock()
	m.bgTerminals = nil // the command exited
	m.mu.Unlock()
	waitCodexBg(t, h.name, false)
}

// An older pinned CLI does not know the method: the session reads as it always did, and the
// app-server is not asked again on every poll.
func TestUnknownBackgroundTerminalsMethodIsNotBusyAndNotRetried(t *testing.T) {
	old := bgTTL
	bgTTL = 0
	t.Cleanup(func() { bgTTL = old })

	m, cl := newMockCodexServer(t)
	m.bgUnknown = true
	h := newCodexTestHandle(t, cl, "codex-bg-old")
	registerCodexTestHandle(t, h)

	h.kickBg()
	waitCodexCalls(t, m, "thread/backgroundTerminals/list", 1)
	deadline := time.Now().Add(waitBackstop)
	for !cl.noBgTerminals.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the unknown-method answer was not recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		if busy, _ := BackgroundWork(h.name); busy {
			t.Fatal("an app-server without the method made the session busy")
		}
		time.Sleep(time.Millisecond)
	}
	if n := m.callCount("thread/backgroundTerminals/list"); n != 1 {
		t.Errorf("list asked %d times, want once", n)
	}
}

// Reads are throttled: within one window the cached answer serves every poll.
func TestBackgroundWorkIsThrottled(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.bgTerminals = []map[string]any{sleepTerminal}
	h := newCodexTestHandle(t, cl, "codex-bg-ttl")
	registerCodexTestHandle(t, h)

	waitCodexBg(t, h.name, true)
	settled := func() {
		deadline := time.Now().Add(waitBackstop)
		for {
			h.bg.mu.Lock()
			flight := h.bg.flight
			h.bg.mu.Unlock()
			if !flight {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("a refresh never finished")
			}
			time.Sleep(time.Millisecond)
		}
	}
	// Each read comes after the previous refresh has finished, so only the window can be what
	// stops the next one.
	for i := 0; i < 5; i++ {
		settled()
		BackgroundWork(h.name)
	}
	settled()
	if n := m.callCount("thread/backgroundTerminals/list"); n != 1 {
		t.Errorf("list asked %d times inside one window, want once", n)
	}
}

// A Terminal (CLI) codex session has no app-server connection to ask; its answer is always no.
func TestTerminalCodexIsNeverBackgroundBusy(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.bgTerminals = []map[string]any{sleepTerminal}
	h := newCodexTestHandle(t, cl, "codex-bg-tui")
	registerCodexTestHandle(t, h)
	waitCodexBg(t, h.name, true)
	meta := session.Meta{Kind: session.KindCodex, Name: h.name, Dir: h.dir}
	if busy, _ := (agentImpl{}).BackgroundWork(meta); busy {
		t.Error("a Terminal session read busy through the managed handle")
	}
}

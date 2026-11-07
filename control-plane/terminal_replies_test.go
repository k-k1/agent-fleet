package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func inputFrame(t *testing.T, typ, data string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"type": typ, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIsTerminalPresence(t *testing.T) {
	cases := []struct {
		name string
		mt   int
		data []byte
		want bool
	}{
		{"typed key", websocket.TextMessage, inputFrame(t, "input", "x"), true},
		{"focus report", websocket.TextMessage, inputFrame(t, "input", "\x1b[I"), false},
		{"DA reply", websocket.TextMessage, inputFrame(t, "input", "\x1b[?1;2c"), false},
		{"reply plus key", websocket.TextMessage, inputFrame(t, "input", "\x1b[Ix"), true},
		{"arrow", websocket.TextMessage, inputFrame(t, "input", "\x1b[A"), true},
		{"ping", websocket.TextMessage, inputFrame(t, "ping", ""), false},
		{"resize", websocket.TextMessage, inputFrame(t, "resize", ""), false},
		{"input without data", websocket.TextMessage, []byte(`{"type":"input"}`), true},
		{"binary", websocket.BinaryMessage, inputFrame(t, "input", "x"), false},
	}
	for _, c := range cases {
		if got := isTerminalPresence(c.mt, c.data); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTerminalAutoReconnectParam(t *testing.T) {
	if terminalAutoReconnect(httptest.NewRequest("GET", "/ws/terminal?session=s", nil)) {
		t.Error("unmarked attach must count as a human arriving")
	}
	if !terminalAutoReconnect(httptest.NewRequest("GET", "/ws/terminal?session=s&reconnect=1", nil)) {
		t.Error("reconnect=1 must be honoured")
	}
}

func TestAddConnOptSkipsInputClocks(t *testing.T) {
	r := newConnRegistry()
	r.addConnOpt("ws", "s", true, false)
	now := time.Now()
	if r.attentionFresh("ws", time.Minute, now) {
		t.Error("an automatic reconnect must not stamp lastInput")
	}
	if r.watchingSession("ws", "s", time.Minute, now) {
		t.Error("an automatic reconnect must not stamp the per-session clock")
	}
	// Still attached, still an attention connection.
	if c, _, _ := r.snapshot("ws"); c != 1 {
		t.Errorf("conns = %d, want 1", c)
	}
	r.noteInput("ws", "s")
	if !r.attentionFresh("ws", time.Minute, now) || !r.watchingSession("ws", "s", time.Minute, now) {
		t.Error("a real keystroke must still count after an automatic reconnect")
	}
	r.doneConn("ws", "s", true)

	r.addConn("ws2", "s", true) // first attach
	if !r.attentionFresh("ws2", time.Minute, now) || !r.watchingSession("ws2", "s", time.Minute, now) {
		t.Error("a first attach must still count as presence")
	}
}

// A connection flap must not cost the protection of someone who typed a moment ago,
// whichever socket closes first.
func TestAutoReconnectKeepsRecentInputAcrossFlap(t *testing.T) {
	grace := 30 * time.Minute
	old := presenceGrace
	presenceGrace = grace
	defer func() { presenceGrace = old }()
	check := func(t *testing.T, r *connRegistry, ws string, want bool) {
		t.Helper()
		now := time.Now()
		if got := r.attentionFresh(ws, grace, now); got != want {
			t.Errorf("attentionFresh = %v, want %v", got, want)
		}
		if got := r.watchingSession(ws, "s", grace, now); got != want {
			t.Errorf("watchingSession = %v, want %v", got, want)
		}
	}
	typed := func(r *connRegistry, ws string, ago time.Duration) {
		r.addConn(ws, "s", true)
		at := time.Now().Add(-ago)
		r.mu.Lock()
		r.lastInput[ws] = at
		r.sessInput[ws]["s"] = at
		r.mu.Unlock()
	}

	t.Run("old closes first", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", time.Minute)
		r.doneConn("ws", "s", true)
		r.addConnOpt("ws", "s", true, false)
		check(t, r, "ws", true)
	})
	t.Run("new attaches first", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", time.Minute)
		r.addConnOpt("ws", "s", true, false)
		r.doneConn("ws", "s", true)
		check(t, r, "ws", true)
	})
	t.Run("second tab", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", time.Minute)
		r.addConn("ws", "s", true) // other tab, first attach
		r.doneConn("ws", "s", true)
		r.addConnOpt("ws", "s", true, false)
		check(t, r, "ws", true)
	})
	t.Run("grace expired", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", grace+time.Minute)
		r.doneConn("ws", "s", true)
		r.addConnOpt("ws", "s", true, false)
		check(t, r, "ws", false)
	})
	t.Run("unmarked reconnect does not consume the park, first attach stamps", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", grace+time.Minute)
		r.doneConn("ws", "s", true)
		r.addConn("ws", "s", true)
		check(t, r, "ws", true)
	})
	t.Run("expired park is pruned", func(t *testing.T) {
		r := newConnRegistry()
		typed(r, "ws", grace+time.Minute)
		r.doneConn("ws", "s", true)
		r.doneConn("other", "x", true)
		if len(r.parkedInput) != 0 || len(r.parkedSessInput) != 0 {
			t.Errorf("stale parked clocks survived: %v %v", r.parkedInput, r.parkedSessInput)
		}
	})
}

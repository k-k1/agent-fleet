package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestIsAutomaticTerminalReply(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"focus in", "\x1b[I", true},
		{"focus out", "\x1b[O", true},
		{"DA1", "\x1b[?1;2c", true},
		{"DA1 xterm.js", "\x1b[?1;2;4c", true},
		{"DA2", "\x1b[>0;276;0c", true},
		{"CPR", "\x1b[24;80R", true},
		{"CPR row 2", "\x1b[2;1R", true},
		{"CPR row 1 col 1", "\x1b[1;1R", true},
		{"CPR row 1 wide col", "\x1b[1;80R", true},
		{"DECXCPR", "\x1b[?24;80R", true},
		{"DSR ok", "\x1b[0n", true},
		{"DECRPM", "\x1b[?2004;2$y", true},
		{"DECRPM ansi", "\x1b[4;2$y", true},
		{"window size", "\x1b[8;24;80t", true},
		{"kitty query", "\x1b[?0u", true},
		{"OSC 11 BEL", "\x1b]11;rgb:0000/0000/0000\x07", true},
		{"OSC 10 ST", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\", true},
		{"OSC 4 palette", "\x1b]4;1;rgb:cc/00/00\x07", true},
		{"XTVERSION", "\x1bP>|xterm.js(5.5.0)\x1b\\", true},
		{"DECRQSS", "\x1bP1$r0m\x1b\\", true},
		{"two replies", "\x1b[I\x1b[?1;2c", true},
		{"three replies", "\x1b[24;80R\x1b]11;rgb:0000/0000/0000\x07\x1b[O", true},

		// Anything else is presence.
		{"empty", "", false},
		{"letter", "a", false},
		{"enter", "\r", false},
		{"ctrl-c", "\x03", false},
		{"lone ESC", "\x1b", false},
		{"arrow up", "\x1b[A", false},
		{"arrow with mod", "\x1b[1;5C", false},
		{"app-mode arrow", "\x1bOA", false},
		{"home", "\x1b[H", false},
		{"delete", "\x1b[3~", false},
		{"F3 shift", "\x1b[1;2R", false},
		{"F3 ctrl", "\x1b[1;5R", false},
		{"F3 all mods", "\x1b[1;16R", false},
		{"alt-[", "\x1b[", false},
		{"paste", "hello world\n", false},
		{"reply then key", "\x1b[Ia", false},
		{"key then reply", "a\x1b[I", false},
		{"reply then enter", "\x1b[?1;2c\r", false},
		{"reply then arrow", "\x1b[O\x1b[A", false},
		{"unterminated OSC", "\x1b]11;rgb:0000/0000/0000", false},
		{"OSC not a colour", "\x1b]0;title\x07", false},
		{"truncated CSI", "\x1b[?1;2", false},
		{"space", " ", false},
	}
	for _, c := range cases {
		if got := isAutomaticTerminalReply(c.data); got != c.want {
			t.Errorf("%s (%q): got %v, want %v", c.name, c.data, got, c.want)
		}
	}
}

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

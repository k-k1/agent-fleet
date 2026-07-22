package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsURL turns an httptest http:// base into a ws:// one.
func wsURL(httpBase string) string { return "ws" + strings.TrimPrefix(httpBase, "http") }

// TestGatewayIdentifyAndDispatch drives one connection through the real client state machine
// against a fake Gateway: HELLO → (client IDENTIFY, asserted) → READY (resume tokens) →
// MESSAGE_CREATE (must reach onMsg).
func TestGatewayIdentifyAndDispatch(t *testing.T) {
	var gotIntents int
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// HELLO with a long heartbeat so the beat never fires during the test.
		_ = c.WriteJSON(gwPayload{Op: opHello, D: json.RawMessage(`{"heartbeat_interval":600000}`)})
		// Expect IDENTIFY.
		_, b, err := c.ReadMessage()
		if err != nil {
			return
		}
		var id gwPayload
		_ = json.Unmarshal(b, &id)
		var d struct {
			Intents int `json:"intents"`
		}
		_ = json.Unmarshal(id.D, &d)
		gotIntents = d.Intents
		// READY, then a MESSAGE_CREATE.
		s1 := 1
		_ = c.WriteJSON(gwPayload{Op: opDispatch, T: "READY", S: &s1,
			D: json.RawMessage(`{"session_id":"sess-abc","resume_gateway_url":"wss://resume.example"}`)})
		s2 := 2
		_ = c.WriteJSON(gwPayload{Op: opDispatch, T: "MESSAGE_CREATE", S: &s2,
			D: json.RawMessage(`{"id":"9","channel_id":"T1","content":"hi","author":{"id":"U1"}}`)})
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	old := gatewayDialURL
	gatewayDialURL = func(string) (string, error) { return wsURL(srv.URL), nil }
	defer func() { gatewayDialURL = old }()

	got := make(chan gatewayMessage, 1)
	gw := &gateway{token: "tok", onMsg: func(m gatewayMessage) { got <- m }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = gw.connectOnce(ctx) }()

	select {
	case m := <-got:
		if m.ChannelID != "T1" || m.Content != "hi" || m.Author.ID != "U1" {
			t.Fatalf("unexpected message: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MESSAGE_CREATE never reached onMsg")
	}
	if gotIntents != discordIntents {
		t.Errorf("IDENTIFY intents = %d, want %d (GUILD_MESSAGES|MESSAGE_CONTENT)", gotIntents, discordIntents)
	}
	cancel()
	// Resume tokens captured from READY.
	if gw.sessionID != "sess-abc" || gw.resumeURL != "wss://resume.example" {
		t.Errorf("resume state not captured: id=%q url=%q", gw.sessionID, gw.resumeURL)
	}
}

// TestGatewayDisallowedIntent maps close 4014 to the fatal errDisallowedIntent so the
// supervisor stops instead of hammering reconnects.
func TestGatewayDisallowedIntent(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(gwPayload{Op: opHello, D: json.RawMessage(`{"heartbeat_interval":600000}`)})
		_, _, _ = c.ReadMessage() // IDENTIFY
		_ = c.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4014, "Disallowed intent(s)"), time.Now().Add(time.Second))
	}))
	defer srv.Close()

	old := gatewayDialURL
	gatewayDialURL = func(string) (string, error) { return wsURL(srv.URL), nil }
	defer func() { gatewayDialURL = old }()

	gw := &gateway{token: "tok", onMsg: func(gatewayMessage) {}}
	err := gw.connectOnce(context.Background())
	if err != errDisallowedIntent {
		t.Fatalf("close 4014 → err = %v, want errDisallowedIntent", err)
	}
}

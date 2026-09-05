package bridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOperatorStore covers the bridge-operator.json pointer: save/read, the thread→conv
// match used by routeInbound, and the disconnect reset that KEEPS the conversation id.
func TestOperatorStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := OperatorState(); ok {
		t.Fatal("no state before provisioning")
	}
	SaveOperatorState("C1", "T-op", "conv-1")

	ref, ok := OperatorState()
	if !ok || ref.Channel != "C1" || ref.Thread != "T-op" || ref.Conv != "conv-1" {
		t.Fatalf("state = %+v ok=%v", ref, ok)
	}
	if conv, ok := OperatorThreadMatch("T-op"); !ok || conv != "conv-1" {
		t.Fatalf("OperatorThreadMatch(T-op) = %q,%v; want conv-1,true", conv, ok)
	}
	if _, ok := OperatorThreadMatch("T-other"); ok {
		t.Fatal("a non-operator thread must not match")
	}

	// Disconnect: coordinates drop, the conversation id survives (one continuous chat).
	ResetOperatorThread()
	ref, ok = OperatorState()
	if !ok || ref.Conv != "conv-1" {
		t.Fatalf("reset must keep conv: %+v ok=%v", ref, ok)
	}
	if ref.Channel != "" || ref.Thread != "" {
		t.Fatalf("reset must clear channel/thread: %+v", ref)
	}
	if _, ok := OperatorThreadMatch("T-op"); ok {
		t.Fatal("a reset thread must no longer match")
	}
}

// captureDiscordBodies records "METHOD path" and the JSON `content` of every request,
// and signals `posted` on each message POST so an async operator reply can be awaited.
func captureDiscordBodies(t *testing.T) (hits *[]string, bodies *[]string, posted chan struct{}) {
	t.Helper()
	var mu sync.Mutex
	var hs, bs []string
	ch := make(chan struct{}, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		hs = append(hs, r.Method+" "+r.URL.Path)
		if body.Content != "" {
			bs = append(bs, body.Content)
		}
		mu.Unlock()
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages") {
			ch <- struct{}{}
		}
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	t.Cleanup(srv.Close)
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })
	return &hs, &bs, ch
}

// TestRouteOperatorInbound: a bound-user reply in the operator thread runs the operator
// turn and posts the reply back into that thread, with a 👀 receipt.
func TestRouteOperatorInbound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SaveOperatorState("C1", "T-op", "conv-abc")
	hits, bodies, posted := captureDiscordBodies(t)

	const boundUser = "U-owner"
	var gotConv, gotText string
	deps := ReceiverDeps{
		Inject: func(string, string, string) (string, error) { return "", nil },
		Operator: func(conv, text string) (string, error) {
			gotConv, gotText = conv, text
			return "フリートは2件稼働中です", nil
		},
	}
	m := gatewayMessage{ID: "MSG1", ChannelID: "T-op", Content: "<@9> 稼働状況は?"}
	m.Author.ID = boundUser
	routeInbound(m, "tok", boundUser, deps)

	select {
	case <-posted:
	case <-time.After(2 * time.Second):
		t.Fatal("operator reply was never posted back")
	}
	if gotConv != "conv-abc" || gotText != "稼働状況は?" {
		t.Fatalf("operator called with conv=%q text=%q (mention should be stripped)", gotConv, gotText)
	}
	var reacted, replied bool
	for _, h := range *hits {
		if strings.HasPrefix(h, "PUT /channels/T-op/messages/MSG1/reactions/") {
			reacted = true
		}
		if h == "POST /channels/T-op/messages" {
			replied = true
		}
	}
	if !reacted || !replied {
		t.Fatalf("want 👀 receipt + reply post; hits=%v", *hits)
	}
	if len(*bodies) == 0 || !strings.Contains((*bodies)[len(*bodies)-1], "フリートは2件稼働中です") {
		t.Fatalf("reply body not posted: %v", *bodies)
	}
}

// TestRouteOperatorInboundGate: the operator branch obeys the same sole defense (contract 5) —
// only the bound user's replies run a turn; a stranger's message touches nothing.
func TestRouteOperatorInboundGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SaveOperatorState("C1", "T-op", "conv-abc")
	hits, _, _ := captureDiscordBodies(t)

	var called bool
	deps := ReceiverDeps{
		Inject:   func(string, string, string) (string, error) { return "", nil },
		Operator: func(string, string) (string, error) { called = true; return "x", nil },
	}
	msg := func(id, author, content string, bot bool) gatewayMessage {
		var m gatewayMessage
		m.ID, m.ChannelID, m.Content = id, "T-op", content
		m.Author.ID, m.Author.Bot = author, bot
		return m
	}
	for _, m := range []gatewayMessage{
		msg("A", "U-stranger", "do evil", false), // wrong author → drop
		msg("B", "U-owner", "echo", true),        // bot → drop
	} {
		routeInbound(m, "tok", "U-owner", deps)
	}
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("operator turn ran for a non-bound / bot author")
	}
	if len(*hits) != 0 {
		t.Fatalf("dropped operator messages must not touch Discord: %v", *hits)
	}
}

// TestPostOperatorChunks: the return leg scrubs secrets and splits over the 2000-char
// limit, posting each chunk.
func TestPostOperatorChunks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, bodies, _ := captureDiscordBodies(t)

	long := strings.Repeat("あ", 2500) // > discordContentLimit → at least 2 chunks
	secret := "\nトークンは xoxb-123456789012-abcdefghijklmnopqrstuvwx です"
	if err := postOperatorChunks("tok", "T-op", long+secret); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) < 2 {
		t.Fatalf("expected chunking into ≥2 messages, got %d", len(*bodies))
	}
	for _, b := range *bodies {
		if strings.Contains(b, "xoxb-123456789012-abcdefghijklmnopqrstuvwx") {
			t.Fatalf("secret leaked unscrubbed into a chunk: %q", b)
		}
	}
}

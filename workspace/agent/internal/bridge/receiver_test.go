package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// captureDiscord stands in for the Discord REST API in receiver tests, recording
// every "METHOD path" it receives (reactions, typing, message posts) so the ack
// wiring can be asserted without touching the network.
func captureDiscord(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	t.Cleanup(srv.Close)
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })
	return &hits
}

// TestThreadToSession reverse-looks-up by thread id only (a thread MESSAGE_CREATE carries
// only the thread's own channel_id).
func TestThreadToSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{
		"sess-a": {Channel: "C1", Thread: "T-a"},
		"sess-b": {Channel: "C1", Thread: "T-b"},
	})
	if name, ok := ThreadToSession("T-b"); !ok || name != "sess-b" {
		t.Fatalf("ThreadToSession(T-b) = %q,%v; want sess-b,true", name, ok)
	}
	if _, ok := ThreadToSession("T-missing"); ok {
		t.Fatal("unknown thread should not resolve")
	}
}

// TestRouteInboundGate exercises the security gate (ADR0020 contract 5): only the bound user's
// messages, only in a known session thread, get injected — everything else is dropped.
func TestRouteInboundGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})

	hits := captureDiscord(t)
	const boundUser = "U-owner"
	var injected []string // "name|text|source" for each accepted inject
	deps := ReceiverDeps{Inject: func(name, text, source string) (string, error) {
		injected = append(injected, name+"|"+text+"|"+source)
		return "", nil
	}}

	msg := func(author, channel, content string, bot bool) gatewayMessage {
		var m gatewayMessage
		m.ChannelID = channel
		m.Content = content
		m.Author.ID = author
		m.Author.Bot = bot
		return m
	}

	cases := []struct {
		name string
		m    gatewayMessage
	}{
		{"other user", msg("U-stranger", "T-a", "do evil", false)},   // wrong author → drop
		{"bot echo", msg(boundUser, "T-a", "notice text", true)},     // bot → drop
		{"unknown thread", msg(boundUser, "T-unknown", "hi", false)}, // not a session thread → drop
		{"empty after strip", msg(boundUser, "T-a", "<@123>  ", false)},
	}
	for _, c := range cases {
		routeInbound(c.m, "tok", boundUser, deps)
	}
	if len(injected) != 0 {
		t.Fatalf("gate leaked: %v", injected)
	}
	if len(*hits) != 0 {
		t.Fatalf("dropped messages must not touch Discord: %v", *hits)
	}

	// The one legitimate case: bound user, known thread, real text (leading bot mention stripped).
	m := msg(boundUser, "T-a", "<@999> retry the build", false)
	m.ID = "MSG1"
	routeInbound(m, "tok", boundUser, deps)
	if len(injected) != 1 || injected[0] != "sess-a|retry the build|discord" {
		t.Fatalf("legit reply not routed correctly: %v", injected)
	}
	// Ack on success: a 👀 reaction on the user's message + a typing pulse in the thread.
	var reacted, typed bool
	for _, h := range *hits {
		if strings.HasPrefix(h, "PUT /channels/T-a/messages/MSG1/reactions/") {
			reacted = true
		}
		if h == "POST /channels/T-a/typing" {
			typed = true
		}
	}
	if !reacted || !typed {
		t.Fatalf("success ack missing (reacted=%v typed=%v): %v", reacted, typed, *hits)
	}
}

// TestRouteInboundFailureReason posts the localized reason back into the thread when
// the inject is rejected (e.g. a pending question), instead of silently dropping it.
func TestRouteInboundFailureReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})
	hits := captureDiscord(t)

	const boundUser = "U-owner"
	deps := ReceiverDeps{Inject: func(name, text, source string) (string, error) {
		return "⚠️ 質問への回答待ちです", errTest
	}}
	m := gatewayMessage{ID: "MSG9", ChannelID: "T-a", Content: "answer"}
	m.Author.ID = boundUser
	routeInbound(m, "tok", boundUser, deps)

	// The reason is posted (a message), and NO success ack (reaction/typing) fires.
	var posted, acked bool
	for _, h := range *hits {
		if h == "POST /channels/T-a/messages" {
			posted = true
		}
		if strings.Contains(h, "/reactions/") || strings.HasSuffix(h, "/typing") {
			acked = true
		}
	}
	if !posted || acked {
		t.Fatalf("want reason posted and no ack; hits=%v", *hits)
	}
}

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "test error" }

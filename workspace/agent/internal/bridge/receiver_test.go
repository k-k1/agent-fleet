package bridge

import (
	"testing"
)

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

// TestRouteInboundGate exercises the security gate (ADR0020 契約5): only the bound user's
// messages, only in a known session thread, get injected — everything else is dropped.
func TestRouteInboundGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})

	const boundUser = "U-owner"
	var injected []string // "name|text|source" for each accepted inject
	deps := ReceiverDeps{Inject: func(name, text, source string) error {
		injected = append(injected, name+"|"+text+"|"+source)
		return nil
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
		routeInbound(c.m, boundUser, deps)
	}
	if len(injected) != 0 {
		t.Fatalf("gate leaked: %v", injected)
	}

	// The one legitimate case: bound user, known thread, real text (leading bot mention stripped).
	routeInbound(msg(boundUser, "T-a", "<@999> retry the build", false), boundUser, deps)
	if len(injected) != 1 || injected[0] != "sess-a|retry the build|discord" {
		t.Fatalf("legit reply not routed correctly: %v", injected)
	}
}

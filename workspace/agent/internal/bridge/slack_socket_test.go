package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRouteSlackInboundIdentityGate: only the bound user's in-thread replies route; bot
// echoes, subtypes, other users, and non-thread messages are dropped. A session-thread reply
// injects; an operator-thread reply runs the operator turn.
func TestRouteSlackInboundIdentityGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	slackThreads.save(threadMap{"sess-a": {Channel: "C1", Thread: "root-a"}})
	slackOperator.save("C1", "root-op", "conv-op")

	var injected []string
	opCh := make(chan string, 1)
	deps := ReceiverDeps{
		Inject: func(name, text, source string) (string, error) {
			injected = append(injected, name+"|"+text+"|"+source)
			return "", nil
		},
		Operator: func(conv, text string) (string, error) { opCh <- conv + "|" + text; return "ok", nil },
	}
	creds := slackReceiveCreds{botToken: "xoxb-tok", boundUser: "U9", botUserID: "UBOT"}

	drop := []slackInboundMsg{
		{User: "U9", BotID: "B1", Text: "x", Channel: "C1", ThreadTS: "root-a"},                // bot echo
		{User: "U9", Subtype: "message_changed", Text: "x", Channel: "C1", ThreadTS: "root-a"}, // edit
		{User: "UOTHER", Text: "x", Channel: "C1", ThreadTS: "root-a"},                         // not bound
		{User: "UBOT", Text: "x", Channel: "C1", ThreadTS: "root-a"},                           // our bot
		{User: "U9", Text: "top-level", Channel: "C1", ThreadTS: ""},                           // not in a thread
		{User: "U9", Text: "unknown", Channel: "C1", ThreadTS: "root-x"},                       // unknown thread
	}
	for _, m := range drop {
		routeSlackInbound(m, creds, deps)
	}
	if len(injected) != 0 {
		t.Fatalf("dropped messages must not inject: %v", injected)
	}

	routeSlackInbound(slackInboundMsg{User: "U9", Text: "<@UBOT> hello ", Channel: "C1", TS: "t1", ThreadTS: "root-a"}, creds, deps)
	if len(injected) != 1 || injected[0] != "sess-a|hello|slack" {
		t.Fatalf("session reply must inject stripped text: %v", injected)
	}

	routeSlackInbound(slackInboundMsg{User: "U9", Text: "status?", Channel: "C1", TS: "t2", ThreadTS: "root-op"}, creds, deps)
	select {
	case got := <-opCh:
		if got != "conv-op|status?" {
			t.Fatalf("operator turn got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operator turn was not invoked")
	}
	// The turn being invoked is not the end of it: the reply is posted later on the same
	// goroutine, and without waiting here that post lands on the slackAPIBase the NEXT test
	// substituted and pollutes its recording (measured: this is why TestSlackFlatSend flaked
	// with two posts where it wanted one). The reply's content is covered by the Discord twin
	// (TestRouteOperatorInbound), so all this checks is that it went back into the thread —
	// enough to let the writer finish.
	reply := posts.wait(t, 1)[0]
	if reply.channel != "C1" || reply.threadTS != "root-op" {
		t.Fatalf("operator reply must go back into the operator thread: %+v", reply)
	}
}

// TestRouteSlackInteractionGate: the bound user's click is applied + the message edited; a
// non-bound user's click is ignored.
func TestRouteSlackInteractionGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	var answered []string
	deps := ReceiverDeps{Answer: func(pi ParsedInteraction) (string, error) {
		answered = append(answered, pi.Kind+"|"+pi.Choice)
		return "✓ done", nil
	}}
	creds := slackReceiveCreds{botToken: "xoxb-tok", boundUser: "U9"}

	routeSlackInteraction(slackInboundInteraction{UserID: "UOTHER", ChannelID: "C1", MessageTS: "m1", CustomID: customID("p", "allow", "s1")}, creds, deps)
	if len(answered) != 0 {
		t.Fatalf("non-bound click must be ignored: %v", answered)
	}
	routeSlackInteraction(slackInboundInteraction{UserID: "U9", ChannelID: "C1", MessageTS: "m1", CustomID: customID("p", "allow", "s1")}, creds, deps)
	if len(answered) != 1 || answered[0] != "p|allow" {
		t.Fatalf("bound click must apply: %v", answered)
	}
	// The message is edited to show the outcome (chat.update).
	var updated bool
	for _, p := range posts.all() {
		if p.method == "chat.update" && p.channel == "C1" && p.text == "✓ done" {
			updated = true
		}
	}
	if !updated {
		t.Fatalf("interaction must edit the message with feedback: %+v", posts.all())
	}
}

// TestSlackHandleEventParsing: a message envelope is parsed and forwarded; a non-message event
// is dropped.
func TestSlackHandleEventParsing(t *testing.T) {
	var got []slackInboundMsg
	ss := &slackSocket{onEvent: func(m slackInboundMsg) { got = append(got, m) }}
	ss.handleEvent(json.RawMessage(`{"event":{"type":"message","user":"U9","text":"hi","channel":"C1","ts":"t1","thread_ts":"root"}}`))
	ss.handleEvent(json.RawMessage(`{"event":{"type":"reaction_added","user":"U9"}}`))
	if len(got) != 1 || got[0].User != "U9" || got[0].ThreadTS != "root" || got[0].Text != "hi" {
		t.Fatalf("event parsing wrong: %+v", got)
	}
}

// TestSlackHandleInteractiveParsing: a block_actions envelope yields the click's user, message
// ts and custom_id (from value).
func TestSlackHandleInteractiveParsing(t *testing.T) {
	var got []slackInboundInteraction
	ss := &slackSocket{onInteract: func(gi slackInboundInteraction) { got = append(got, gi) }}
	ss.handleInteractive(json.RawMessage(`{"type":"block_actions","user":{"id":"U9"},"channel":{"id":"C1"},"message":{"ts":"m1"},"actions":[{"action_id":"af|op|approve|id1","value":"af|op|approve|id1"}]}`))
	if len(got) != 1 || got[0].UserID != "U9" || got[0].MessageTS != "m1" || got[0].CustomID != "af|op|approve|id1" {
		t.Fatalf("interactive parsing wrong: %+v", got)
	}
}

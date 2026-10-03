package muse

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

// #1255 review finding 3: the host refuses the first held message's turn/start. It fails like any
// failed start and is not written back, and the next held message still starts ahead of the
// caller's input — muse starts an idle session's input at once, so a held message left on disk
// would be overtaken.
func TestHeldRefusedStartDoesNotLetTheCallerOvertake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{name: "held-muse"}
	host := newTestHandle(t, h)
	starts := make(chan string, 8)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		text := *p.Input[0].Text
		starts <- text
		if strings.HasSuffix(text, "refused") {
			return nil, &msp.Error{Code: -32602, Message: "invalid params"}
		}
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionStarted, StartedNewTurn: true, TurnID: p.CommandID}, nil
	})

	// Two peer messages left held by an earlier run of this session.
	q := agents.NewTurnQueue(h.name, nil, agents.LedgerAtAccept)
	q.Accept(agents.TurnInput{Prompt: "running", Origin: agents.Origin{Kind: agents.OriginMember}})
	q.Take()
	for _, p := range []string{"refused", "second"} {
		q.Accept(agents.TurnInput{Prompt: "[agent-fleet:peer from=x intent=notice reply=none] " + p,
			ClientMessageID: p, Origin: agents.Origin{Kind: agents.OriginPeer, From: "x"}})
	}

	agents.DeliverHeld(h.name, h)
	if err := h.Send(agents.TurnInput{Prompt: "caller", Origin: agents.Origin{Kind: agents.OriginMember}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"refused", "second"} {
		select {
		case got := <-starts:
			if !strings.HasSuffix(got, want) || !strings.Contains(got, " queued=") {
				t.Fatalf("started %q, want the held %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q never reached the host", want)
		}
	}
	select {
	case got := <-starts:
		t.Fatalf("%q started while the held message was still running", got)
	case <-time.After(200 * time.Millisecond):
	}
	if n := agents.HeldCount(h.name); n != 0 {
		t.Fatalf("held after delivery = %d, want 0 (the refused one must not come back)", n)
	}
}

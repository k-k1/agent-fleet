package lcpp

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
)

// blockingClient holds Send until released: a model request, or a tool, that stays silent.
type blockingClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *blockingClient) Send(ctx context.Context, _ []harness.Message, _ []harness.ToolDef) (harness.Turn, error) {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return harness.Turn{Content: "done"}, nil
	case <-ctx.Done():
		return harness.Turn{}, ctx.Err()
	}
}

func (c *blockingClient) InputTokens(context.Context, []harness.Message, []harness.ToolDef) (int, error) {
	return 1, nil
}

func staleStamp(h *threadHandle) { h.act.TouchAt(time.Now().Add(-2 * time.Hour)) }

// A turn that is running keeps the row holding however long it has been silent: the stamp is two
// hours old and nothing was sent, yet the answer is "now".
func TestRunningTurnKeepsAStaleRowHolding(t *testing.T) {
	c := &blockingClient{entered: make(chan struct{}, 1), release: make(chan struct{})}
	wireEngine(t, c)
	m := testMeta(t, "lcpp-progress-run")
	hh, err := managedDriver{}.Resume(m)
	if err != nil {
		t.Fatal(err)
	}
	h := hh.(*threadHandle)
	if err := h.Send(agents.TurnInput{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	<-c.entered
	staleStamp(h)

	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("turn running, silent 2 h: got %v, %v; want now", got, ok)
	}

	close(c.release)
	waitState(t, h, agents.TurnCompleted)
	for deadline := time.Now().Add(5 * time.Second); h.Busy() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	staleStamp(h)
	got, ok = (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) < time.Hour {
		t.Fatalf("turn over, silent 2 h: got %v, %v; want the stale stamp, so the bound can lapse", got, ok)
	}
}

// A handle that has not run a turn since it was created cannot tell, and no handle at all cannot
// either: both keep holding.
func TestManagedProgressWithoutATurnOrHandleCannotTell(t *testing.T) {
	m := testMeta(t, "lcpp-progress-none")
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("no handle: answered %v", got)
	}
	if _, err := (managedDriver{}).Resume(m); err != nil {
		t.Fatal(err)
	}
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("handle that never ran a turn: answered %v", got)
	}
}

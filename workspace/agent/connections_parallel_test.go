package main

import (
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

type barrierKind struct {
	wg *sync.WaitGroup
}

func (b barrierKind) ConnectionStatus(agents.StatusInputs) map[string]any {
	b.wg.Done()
	b.wg.Wait() // returns only once every kind is inside its probe at the same time
	return map[string]any{"connected": true}
}

type nilKind struct{}

func (nilKind) ConnectionStatus(agents.StatusInputs) map[string]any { return nil }

// A serial loop would block the first kind forever, waiting for the others to arrive.
func TestCollectStatusesRunsKindsConcurrently(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(3)
	m := map[string]agents.ConnectionStatuser{"a": barrierKind{&wg}, "b": barrierKind{&wg}, "c": barrierKind{&wg}, "n": nilKind{}}
	ks := []string{"a", "missing", "b", "c", "n"}
	done := make(chan map[string]any, 1)
	go func() {
		done <- collectStatuses(ks, func(k string) (agents.ConnectionStatuser, bool) { c, ok := m[k]; return c, ok }, agents.StatusInputs{})
	}()
	select {
	case out := <-done:
		if len(out) != 3 || out["a"] == nil || out["b"] == nil || out["c"] == nil {
			t.Fatalf("out = %v, want a, b, c only", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("kinds were probed one after another")
	}
}

type panicKind struct{}

func (panicKind) ConnectionStatus(agents.StatusInputs) map[string]any { panic("boom") }

// A panicking kind is left out; the others still answer and the process survives.
func TestCollectStatusesSurvivesPanickingKind(t *testing.T) {
	m := map[string]agents.ConnectionStatuser{"ok": nilOKKind{}, "bad": panicKind{}}
	out := collectStatuses([]string{"bad", "ok"}, func(k string) (agents.ConnectionStatuser, bool) { c, ok := m[k]; return c, ok }, agents.StatusInputs{})
	if len(out) != 1 || out["ok"] == nil {
		t.Fatalf("out = %v, want only ok", out)
	}
}

type nilOKKind struct{}

func (nilOKKind) ConnectionStatus(agents.StatusInputs) map[string]any {
	return map[string]any{"connected": true}
}

package muse

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func tokenUsage(turn string, prompt, cacheRead, out int64) map[string]any {
	return map[string]any{
		"sessionId":    "01a0c1d6-0000-7000-8000-000000000001",
		"turnId":       turn,
		"promptTokens": prompt,
		"totalTokens":  prompt + out,
		"usage": map[string]any{
			"inputTokens":     prompt,
			"cachedTokens":    cacheRead,
			"cacheReadTokens": cacheRead,
			"outputTokens":    out,
		},
		"cumulative":  map[string]any{},
		"sourceRange": map[string]any{},
		"viewCursor":  "1",
	}
}

func turnStarted(turn string) map[string]any {
	return map[string]any{
		"sessionId":   "01a0c1d6-0000-7000-8000-000000000001",
		"turnId":      turn,
		"sourceRange": map[string]any{},
		"viewCursor":  "1",
	}
}

// The overview card of a running muse session carries the same context fill the mirror
// draws and one spend per turn. A turn's spend is its output summed over every model
// completion plus the LAST completion's uncached prompt: each completion re-states the whole
// prompt, so summing prompts would count the context once per tool call.
func TestWireLiveCarriesContextAndSpends(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	registerHandle(t, "ov", h)
	m := session.Meta{Name: "ov", Kind: session.KindMuse, Dir: t.TempDir()}

	if li := (agentImpl{}).WireLive(m, true); li.Context != nil || li.TokenSpends != nil {
		t.Fatalf("before any usage: %+v, want no context and no spends", li)
	}

	// Turn A: two completions. Spend = (21857-20721) + 300 + 200.
	host.Notify(msp.NotificationTurnStarted, turnStarted("turn-a"))
	host.Notify(msp.NotificationSessionTokenUsage, tokenUsage("turn-a", 20776, 0, 300))
	host.Notify(msp.NotificationSessionTokenUsage, tokenUsage("turn-a", 21857, 20721, 200))
	// Turn B: one completion, cache read only partly covers the prompt.
	host.Notify(msp.NotificationTurnStarted, turnStarted("turn-b"))
	host.Notify(msp.NotificationSessionTokenUsage, tokenUsage("turn-b", 23000, 21000, 50))
	host.Notify(msp.NotificationSessionContextUsage, map[string]any{
		"sessionId":   "01a0c1d6-0000-7000-8000-000000000001",
		"usedTokens":  int64(23050),
		"pressure":    "low",
		"sourceRange": map[string]any{},
		"viewCursor":  "2",
	})
	waitFor(t, func() bool {
		_, _, ok := ManagedContext("ov")
		return ok
	})

	li := (agentImpl{}).WireLive(m, true)
	if li.Context == nil {
		t.Fatal("Context nil with a live contextUsage reading")
	}
	if li.Context.Fresh != 23050 || li.Context.Read != 0 || li.Context.Create != 0 {
		t.Errorf("Context = %+v, want a single 23050-token segment", li.Context)
	}
	if li.Context.Window != MuseDefaultWindow || li.Context.WindowSource != "estimated" {
		t.Errorf("Context window = %d/%q, want the default window marked estimated", li.Context.Window, li.Context.WindowSource)
	}
	want := []int{21857 - 20721 + 300 + 200, 23000 - 21000 + 50}
	if !reflect.DeepEqual(li.TokenSpends, want) {
		t.Errorf("TokenSpends = %v, want %v", li.TokenSpends, want)
	}

	// A stopped session has no live handle to read, so neither is carried.
	if li := (agentImpl{}).WireLive(m, false); li.Context != nil || li.TokenSpends != nil {
		t.Errorf("stopped: %+v, want no context and no spends", li)
	}
}

// Only the newest spends are kept, oldest first, so a long session does not grow the handle
// or the list payload without bound.
func TestManagedSpendsKeepsNewest(t *testing.T) {
	h := &threadHandle{}
	for i := 0; i < spendKeep+5; i++ {
		usageInTurn(h, fmt.Sprintf("turn-%d", i), int64(i), 1)
	}
	registerHandle(t, "ov-cap", h)
	got := ManagedSpends("ov-cap")
	if len(got) != spendKeep {
		t.Fatalf("len = %d, want %d", len(got), spendKeep)
	}
	if got[0] != 5+1 || got[len(got)-1] != spendKeep+4+1 {
		t.Errorf("kept %v, want the newest %d oldest first", got, spendKeep)
	}
}

func spendEvent(turn string, prompt, out int64) msp.SessionTokenUsageParams {
	return msp.SessionTokenUsageParams{TurnID: turn, PromptTokens: prompt, Usage: msp.TokenUsage{OutputTokens: out}}
}

// usageInTurn records one completion's usage while turn is the handle's running turn, as the host
// delivers it between turn/started and turn/completed.
func usageInTurn(h *threadHandle, turn string, prompt, out int64) {
	h.mu.Lock()
	h.turnID = turn
	h.mu.Unlock()
	h.recordTokenUsage(spendEvent(turn, prompt, out))
}

// A late event for an earlier turn folds into that turn: matched against the newest turn only,
// it would split turn A in two and count its prompt twice ([110 220 155] instead of [165 220]).
func TestManagedSpendsFoldsAnInterleavedTurn(t *testing.T) {
	h := &threadHandle{}
	usageInTurn(h, "a", 100, 10)
	usageInTurn(h, "b", 200, 20)
	h.recordTokenUsage(spendEvent("a", 150, 5)) // late, while b is running
	registerHandle(t, "ov-interleave", h)
	if got, want := ManagedSpends("ov-interleave"), []int{150 + 10 + 5, 220}; !reflect.DeepEqual(got, want) {
		t.Errorf("spends = %v, want %v", got, want)
	}
}

// A late event for a turn already pushed out of the kept window stays out rather than coming
// back as the newest turn — however many turns ago it ran, so twice the window here.
func TestManagedSpendsIgnoresADroppedTurn(t *testing.T) {
	h := &threadHandle{}
	for i := 0; i < 2*spendKeep+1; i++ {
		usageInTurn(h, fmt.Sprintf("turn-%d", i), 1, 1)
	}
	h.recordTokenUsage(spendEvent("turn-0", 1000, 1))
	registerHandle(t, "ov-dropped", h)
	got := ManagedSpends("ov-dropped")
	if len(got) != spendKeep || got[len(got)-1] != 2 {
		t.Errorf("spends = %v, want %d turns of 2 with turn-0 not revived", got, spendKeep)
	}
}

// The handle outlives its host. When the stored session is gone and the slot starts a fresh
// conversation, the old conversation's fill and trend must not stand in for the new one's; a
// successful resume is the same conversation and keeps them.
func TestOpenSessionResetsUsageOnlyForANewConversation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found bool
	}{{"resumed", true}, {"fresh start", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c1"}
			host := newTestHandle(t, h)
			name := "ov-reset-" + map[bool]string{true: "resume", false: "fresh"}[tc.found]
			registerHandle(t, name, h)
			writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000c2", Path: "/tmp/old.jsonl"})
			sess := map[string]any{"sessionId": "01a0c1d6-0000-7000-8000-0000000000c3", "path": "/tmp/s.jsonl", "status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 0}
			host.Handle(msp.MethodSessionResume, func(msptest.Message) (any, *msp.Error) {
				if !tc.found {
					return nil, &msp.Error{Code: msp.ErrCodeSessionNotFound, Message: "gone"}
				}
				return map[string]any{"session": sess, "history": map[string]any{"mode": "inline"}, "pendingRequests": []any{}, "viewCursor": "c1"}, nil
			})
			host.Handle(msp.MethodSessionStart, func(msptest.Message) (any, *msp.Error) {
				return map[string]any{"session": sess, "viewCursor": "c1"}, nil
			})
			usageInTurn(h, "old", 100, 10)
			h.ctxMu.Lock()
			h.ctxUsed, h.ctxHasUsage = 5000, true
			h.ctxMu.Unlock()

			if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
				t.Fatalf("openSession: %v", err)
			}
			_, _, hasCtx := ManagedContext(name)
			spends := ManagedSpends(name)
			if tc.found && (!hasCtx || len(spends) != 1) {
				t.Errorf("resumed: context=%v spends=%v, want both kept", hasCtx, spends)
			}
			if !tc.found && (hasCtx || spends != nil) {
				t.Errorf("fresh start: context=%v spends=%v, want both reset", hasCtx, spends)
			}
		})
	}
}

// Usage can beat turn/started to the handle: the turn is then known only as the commandId AF
// minted for turn/start, and that is enough to take it.
func TestManagedSpendsTakesUsageBeforeTurnStarted(t *testing.T) {
	h := &threadHandle{starting: "cmd-1"}
	h.recordTokenUsage(spendEvent("cmd-1", 100, 10))
	h.recordTokenUsage(spendEvent("stranger", 100, 10))
	registerHandle(t, "ov-early", h)
	if got, want := ManagedSpends("ov-early"), []int{110}; !reflect.DeepEqual(got, want) {
		t.Errorf("spends = %v, want %v", got, want)
	}
}

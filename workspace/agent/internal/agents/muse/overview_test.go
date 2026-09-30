package muse

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
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
	host.Notify(msp.NotificationSessionTokenUsage, tokenUsage("turn-a", 20776, 0, 300))
	host.Notify(msp.NotificationSessionTokenUsage, tokenUsage("turn-a", 21857, 20721, 200))
	// Turn B: one completion, cache read only partly covers the prompt.
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
		h.recordTokenUsage(msp.SessionTokenUsageParams{
			TurnID:       fmt.Sprintf("turn-%d", i),
			PromptTokens: int64(i),
			Usage:        msp.TokenUsage{OutputTokens: 1},
		})
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

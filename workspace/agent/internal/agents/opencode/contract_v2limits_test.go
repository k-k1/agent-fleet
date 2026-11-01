//go:build contract

// What opencode 2 does with a model's `limit`, the rules engine.go's self-hosted provider entry rests
// on. Real opencode 2 serve, stub provider (no credentials, no billing).
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run V2ModelLimits ./internal/agents/opencode/
package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// limitsStub answers every turn "ok" and reports promptTokens of usage, so a test can put a turn
// past a model's window. It records, per model, the output cap each request asked for and how many
// compaction (summary) requests arrived.
type limitsStub struct {
	mu           sync.Mutex
	promptTokens int
	maxTokens    map[string][]float64 // the output cap each turn request asked for, -1 = none
	summaries    map[string]int
}

func newLimitsStub() *limitsStub {
	return &limitsStub{maxTokens: map[string][]float64{}, summaries: map[string]int{}}
}

func (s *limitsStub) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	model, _ := body["model"].(string)
	raw, _ := json.Marshal(body["messages"])
	summary := strings.Contains(string(raw), "You MUST summarize")
	s.mu.Lock()
	if summary {
		s.summaries[model]++
	} else {
		// The cap's key varies (measured on 2.2.5: max_completion_tokens without a limit); its value
		// is what counts.
		cap := -1.0
		for _, k := range []string{"max_tokens", "max_completion_tokens"} {
			if v, ok := body[k].(float64); ok {
				cap = v
			}
		}
		s.maxTokens[model] = append(s.maxTokens[model], cap)
	}
	pt := s.promptTokens
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(v map[string]any) {
		v["id"], v["object"], v["created"], v["model"] = "c", "chat.completion.chunk", 0, model
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	text := "ok"
	if summary {
		text = stubSummary
	}
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}}}})
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	chunk(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": pt, "completion_tokens": 10, "total_tokens": pt + 10}})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (s *limitsStub) seen(model string) (caps []float64, summaries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float64(nil), s.maxTokens[model]...), s.summaries[model]
}

func limitsModel(name string, limit map[string]any) map[string]any {
	m := map[string]any{"name": name, "tool_call": true}
	if limit != nil {
		m["limit"] = limit
	}
	return m
}

// listedLimits waits for the directory's instance to finish activating (it answers 503 until its
// plugins are up) and returns the stub models it lists, with their limits.
func listedLimits(t *testing.T, addr, dir string) map[string]map[string]float64 {
	t.Helper()
	client := &http.Client{Timeout: 60 * time.Second, Transport: authTransport{}}
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(time.Second) {
		res, err := client.Get(addr + "/api/model?directory=" + dir)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			if time.Now().After(deadline) {
				t.Fatalf("GET /api/model: %d %s", res.StatusCode, raw)
			}
			continue
		}
		var listed struct {
			Data []struct {
				ID         string             `json:"id"`
				ProviderID string             `json:"providerID"`
				Limit      map[string]float64 `json:"limit"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &listed); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]float64{}
		for _, m := range listed.Data {
			if m.ProviderID == "stub" {
				out[m.ID] = m.Limit
			}
		}
		return out
	}
}

// limitsTurns runs two turns of a new session on stub/<id>, each reporting promptTokens of usage,
// and returns the turns' errors (provider.no-route for a model opencode does not offer).
func limitsTurns(t *testing.T, stub *limitsStub, addr, dir, id string, promptTokens int) []string {
	t.Helper()
	stub.mu.Lock()
	stub.promptTokens = promptTokens
	stub.mu.Unlock()
	ses, err := serveCreateSession(addr, dir, id)
	if err != nil {
		t.Fatal(err)
	}
	var errs []string
	for _, p := range []string{"first", "second"} {
		req, err := newTurnRequest(addr, ses, agents.TurnInput{Prompt: p})
		if err != nil {
			t.Fatal(err)
		}
		res, err := sendTurn(addr, ses, req, agents.ThreadSettings{Model: "stub/" + id})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if e, has := decodeTurnError(res.Body); has {
			errs = append(errs, e.Name)
		}
		res.Body.Close()
		waitIdle(addr, ses, 30*time.Second)
	}
	time.Sleep(3 * time.Second) // a compaction after the last turn lands here
	return errs
}

func TestContractV2ModelLimits(t *testing.T) {
	stub := newLimitsStub()
	addr, dir := startV2Serve(t, http.HandlerFunc(stub.handler), map[string]any{
		"nolimit":   limitsModel("nolimit", nil),
		"quiet":     limitsModel("quiet", nil),
		"both":      limitsModel("both", map[string]any{"context": 20000, "output": 2000}),
		"bothquiet": limitsModel("bothquiet", map[string]any{"context": 20000, "output": 2000}),
	}, nil)
	v := opencodeVersion(t)

	// No limit is not context 0 (opencode 1: auto-compaction off). opencode 2 gives the model a
	// 200000 window with a 32000 output cap and compacts at about 170000 tokens. A declared window
	// compacts before its context is reached. The exact point is opencode's own: measured on
	// 2.2.5 it is 85% of 200000 and of 20000, but already 82000-84000 of 100000 and under 40000 of
	// 50000, so only measured pairs on either side are pinned here.
	listed := listedLimits(t, addr, dir)
	if l := listed["nolimit"]; l["context"] != 200000 || l["output"] != 32000 {
		t.Fatalf("%s: a model without limit is listed with %v, want context 200000 output 32000", v, l)
	}
	if l := listed["both"]; l["context"] != 20000 || l["output"] != 2000 {
		t.Fatalf("%s: limit {20000, 2000} is listed as %v", v, l)
	}
	for _, c := range []struct {
		id         string
		tokens     int
		wantCap    float64
		wantSummit bool
	}{
		{"nolimit", 170_100, 32000, true},
		{"quiet", 169_800, 32000, false},
		{"both", 17_100, 2000, true},
		{"bothquiet", 16_800, 2000, false},
	} {
		if errs := limitsTurns(t, stub, addr, dir, c.id, c.tokens); len(errs) > 0 {
			t.Fatalf("%s: %s turns failed: %v", v, c.id, errs)
		}
		caps, summaries := stub.seen(c.id)
		if len(caps) == 0 {
			t.Fatalf("%s: %s: the stub saw no turn request", v, c.id)
		}
		// The first turn asks for the full cap; later ones may ask for less as the window fills
		// (measured: 28187 after a 169800-token turn without a limit).
		if caps[0] != c.wantCap {
			t.Fatalf("%s: %s asked for max_tokens %v, want %v first", v, c.id, caps, c.wantCap)
		}
		for _, got := range caps {
			if got <= 0 || got > c.wantCap {
				t.Fatalf("%s: %s asked for max_tokens %v, want at most %v", v, c.id, caps, c.wantCap)
			}
		}
		if (summaries > 0) != c.wantSummit {
			t.Fatalf("%s: %s at %d prompt tokens: %d compaction requests, want compaction=%v", v, c.id, c.tokens, summaries, c.wantSummit)
		}
		t.Logf("%s: %s at %d prompt tokens: max_tokens %v, compaction requests %d", v, c.id, c.tokens, caps, summaries)
	}
}

// A limit with a context and no output does not fall back to a default output any more (opencode
// 1: 32000): opencode 2 rejects the entry, and with it every model of that provider, including a
// valid one next to it. This is why engine.go writes both numbers or neither.
func TestContractV2ModelLimitContextOnlyDropsTheProvider(t *testing.T) {
	stub := newLimitsStub()
	addr, dir := startV2Serve(t, http.HandlerFunc(stub.handler), map[string]any{
		"ctxonly": limitsModel("ctxonly", map[string]any{"context": 20000}),
		"valid":   limitsModel("valid", map[string]any{"context": 20000, "output": 2000}),
	}, nil)
	v := opencodeVersion(t)
	if listed := listedLimits(t, addr, dir); len(listed) != 0 {
		t.Fatalf("%s: a context-only limit left the provider listed: %v", v, listed)
	}
	for _, id := range []string{"ctxonly", "valid"} {
		errs := limitsTurns(t, stub, addr, dir, id, 100)
		if len(errs) == 0 || errs[0] != "provider.no-route" {
			t.Fatalf("%s: %s next to a context-only limit: turn errors %v, want provider.no-route", v, id, errs)
		}
	}
	if caps, _ := stub.seen("valid"); len(caps) != 0 {
		t.Fatalf("%s: the valid model reached the provider: %v", v, caps)
	}
	t.Logf("%s: a context-only limit drops the whole provider (both models: provider.no-route)", v)
}

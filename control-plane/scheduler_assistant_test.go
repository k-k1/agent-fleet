package main

// Fake-Agent integration tests for session_mode=assistant: conversation resolution
// (reuse_target, falling back to owner_conv), a successful fire, a 409 while a turn is
// running mapping to skipped_overlap, a 404 for a vanished conversation mapping to
// skipped_target_missing, and the semantics of a synchronous run with no source.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeAssistantAgent serves POST /assistant-turns with a scripted outcome and records
// what the fire sent.
type fakeAssistantAgent struct {
	mu     sync.Mutex
	convs  []string
	prompt string
	// outcome: "ok" (200 + slug), "busy" (409 turn_in_progress), "gone" (404 conv_not_found)
	outcome string
	slug    string
}

func (a *fakeAssistantAgent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/assistant-turns" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Conv   string `json:"conv"`
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.convs = append(a.convs, body.Conv)
		a.prompt = body.Prompt
		switch a.outcome {
		case "busy":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "turn_in_progress"})
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "conv_not_found"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"conv": "uuid-1", "slug": a.slug, "reply": "done"})
		}
	})
}

func newAssistantFixture(t *testing.T, a *fakeAssistantAgent) (*wakeFirer, *resolved) {
	t.Helper()
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	return &wakeFirer{}, &resolved{rt: stubRuntime{endpoint: srv.URL}}
}

func TestFireAssistantPinnedConv(t *testing.T) {
	a := &fakeAssistantAgent{outcome: "ok", slug: "a3k7f2q"}
	f, res := newAssistantFixture(t, a)
	sch := store.Schedule{ID: "sch_a", SessionMode: "assistant", ReuseTarget: "a3k7f2q", OwnerConv: "conv-uuid", Prompt: "毎朝の報告を {{date}} 分まとめて"}

	status, link, err := f.fireAssistant(t.Context(), res, sch, time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC))
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if link != "a3k7f2q" {
		t.Errorf("run link = %q, want the conversation slug", link)
	}
	if len(a.convs) != 1 || a.convs[0] != "a3k7f2q" {
		t.Fatalf("agent got convs=%v, want the pinned slug", a.convs)
	}
	if a.prompt != "毎朝の報告を 2026-07-25 分まとめて" {
		t.Errorf("prompt = %q (template vars must expand)", a.prompt)
	}
}

func TestFireAssistantDefaultsToOwnerConv(t *testing.T) {
	a := &fakeAssistantAgent{outcome: "ok", slug: "aowner77"[:7]}
	f, res := newAssistantFixture(t, a)
	sch := store.Schedule{ID: "sch_b", SessionMode: "assistant", OwnerConv: "owner-conv-uuid", Prompt: "p"}

	status, _, err := f.fireAssistant(t.Context(), res, sch, time.Now().UTC())
	if err != nil || status != "fired" {
		t.Fatalf("status=%q err=%v, want fired/nil", status, err)
	}
	if len(a.convs) != 1 || a.convs[0] != "owner-conv-uuid" {
		t.Fatalf("agent got convs=%v, want the owner conversation fallback", a.convs)
	}
}

func TestFireAssistantBusyIsOverlapSkip(t *testing.T) {
	a := &fakeAssistantAgent{outcome: "busy"}
	f, res := newAssistantFixture(t, a)
	sch := store.Schedule{ID: "sch_c", SessionMode: "assistant", OwnerConv: "conv-uuid", Prompt: "p"}

	status, _, err := f.fireAssistant(t.Context(), res, sch, time.Now().UTC())
	if err != nil || status != "skipped_overlap" {
		t.Fatalf("status=%q err=%v, want skipped_overlap/nil (busy conversation is the reuse-busy analog)", status, err)
	}
}

func TestFireAssistantGoneIsTargetMissing(t *testing.T) {
	a := &fakeAssistantAgent{outcome: "gone"}
	f, res := newAssistantFixture(t, a)
	sch := store.Schedule{ID: "sch_d", SessionMode: "assistant", ReuseTarget: "a999999", OwnerConv: "conv-uuid", Prompt: "p"}

	status, _, err := f.fireAssistant(t.Context(), res, sch, time.Now().UTC())
	if err != nil || status != "skipped_target_missing" {
		t.Fatalf("status=%q err=%v, want skipped_target_missing/nil", status, err)
	}
}

func TestFireAssistantNoConvAtAll(t *testing.T) {
	a := &fakeAssistantAgent{outcome: "ok"}
	f, res := newAssistantFixture(t, a)
	sch := store.Schedule{ID: "sch_e", SessionMode: "assistant", Prompt: "p"} // no target, no owner

	status, _, err := f.fireAssistant(t.Context(), res, sch, time.Now().UTC())
	if err != nil || status != "skipped_target_missing" {
		t.Fatalf("status=%q err=%v, want skipped_target_missing/nil", status, err)
	}
	if len(a.convs) != 0 {
		t.Fatalf("agent must not be called without a target (convs=%v)", a.convs)
	}
}

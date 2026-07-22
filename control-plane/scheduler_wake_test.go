package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWakeDecision(t *testing.T) {
	cases := []struct {
		state, policy string
		wantWake      bool
		wantSoft      string
	}{
		{"running", "wake", false, ""},    // running always proceeds
		{"running", "skip", false, ""},    // policy irrelevant when already up
		{"stopped", "wake", true, ""},     // default: bring it up
		{"stopped", "", true, ""},         // blank policy == wake
		{"stopped", "catch_up", true, ""}, // catch_up also wakes
		{"stopped", "skip", false, "skipped_stopped"},
		{"none", "skip", false, "skipped_stopped"},
		{"starting", "wake", true, ""},
	}
	for _, tc := range cases {
		gotWake, gotSoft := wakeDecision(tc.state, tc.policy)
		if gotWake != tc.wantWake || gotSoft != tc.wantSoft {
			t.Errorf("wakeDecision(%q,%q) = (%v,%q), want (%v,%q)",
				tc.state, tc.policy, gotWake, gotSoft, tc.wantWake, tc.wantSoft)
		}
	}
}

func TestExpandSchedulePrompt(t *testing.T) {
	slot := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC) // 09:00 JST
	sch := Schedule{
		ID: "sch_ab12", SpecLabel: "毎朝9時レビュー", TZ: "Asia/Tokyo",
		LastRun: "2026-07-22T00:00:00Z", // 09:00 JST previous day
		Prompt: "date={{date}} time={{time}} dt={{datetime}} tz={{tz}} " +
			"id={{schedule_id}} label={{schedule_label}} last={{last_run}} keep={{unknown}}",
	}
	got := expandSchedulePrompt(sch, slot)
	want := "date=2026-07-23 time=09:00 dt=2026-07-23 09:00 JST tz=Asia/Tokyo " +
		"id=sch_ab12 label=毎朝9時レビュー last=2026-07-22 09:00 keep={{unknown}}"
	if got != want {
		t.Fatalf("expandSchedulePrompt:\n got=%q\nwant=%q", got, want)
	}
}

func TestExpandSchedulePromptEmptyMeta(t *testing.T) {
	// Blank tz defaults to UTC; a never-run schedule renders {{last_run}} as empty.
	slot := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	sch := Schedule{ID: "sch_x", Prompt: "tz={{tz}} last=[{{last_run}}] t={{time}}"}
	got := expandSchedulePrompt(sch, slot)
	want := "tz=UTC last=[] t=09:00"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestScheduleIdempotencyKey(t *testing.T) {
	slot := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	// Deterministic and slot-scoped: same (id, slot) => same key.
	k1 := scheduleIdempotencyKey("sch_1", slot)
	k2 := scheduleIdempotencyKey("sch_1", slot)
	if k1 != k2 {
		t.Fatalf("non-deterministic: %q vs %q", k1, k2)
	}
	// A different slot produces a different key (so a real next fire is not deduped).
	if k3 := scheduleIdempotencyKey("sch_1", slot.Add(24*time.Hour)); k3 == k1 {
		t.Fatalf("distinct slots collided: %q", k3)
	}
	if want := "sch_sch_1@2026-07-23T00:00:00Z"; k1 != want {
		t.Fatalf("key = %q, want %q", k1, want)
	}
}

func TestBuildInjectBody(t *testing.T) {
	slot := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	sch := Schedule{
		ID: "sch_1", AgentKind: "codex", Model: "gpt-x", Repo: "/home/dev/repos/x",
		OwnerConv: "conv1", TZ: "UTC", Prompt: "run at {{time}}",
	}
	var m map[string]any
	if err := json.Unmarshal(buildInjectBody(sch, slot), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["kind"] != "codex" || m["driver"] != "managed" {
		t.Errorf("kind/driver = %v/%v, want codex/managed", m["kind"], m["driver"])
	}
	if m["report_to"] != "conv1" {
		t.Errorf("report_to = %v", m["report_to"])
	}
	if m["initial_prompt"] != "run at 00:00" {
		t.Errorf("initial_prompt = %v", m["initial_prompt"])
	}
	if m["idempotency_key"] != "sch_sch_1@2026-07-23T00:00:00Z" {
		t.Errorf("idempotency_key = %v", m["idempotency_key"])
	}
	if m["dir"] != "/home/dev/repos/x" {
		t.Errorf("dir = %v", m["dir"])
	}
}

func TestBuildInjectBodyDefaultsKind(t *testing.T) {
	// Blank kind defaults to claude and yields the tui (empty) driver.
	var m map[string]any
	_ = json.Unmarshal(buildInjectBody(Schedule{ID: "s"}, time.Now()), &m)
	if m["kind"] != "claude" || m["driver"] != "" {
		t.Fatalf("kind/driver = %v/%v, want claude/empty", m["kind"], m["driver"])
	}
}

// TestInjectSession verifies the create_session POST reaches the Agent with the auth
// header, path and body the Agent expects.
func TestInjectSession(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"s7","state":"working"}`))
	}))
	defer srv.Close()

	f := &wakeFirer{}
	sch := Schedule{ID: "sch_1", AgentKind: "claude", OwnerConv: "conv1", Prompt: "hi"}
	body := buildInjectBody(sch, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	if err := f.injectSession(context.Background(), stubRuntime{endpoint: srv.URL, token: "tok"}, body); err != nil {
		t.Fatalf("injectSession: %v", err)
	}
	if gotPath != "/sessions" {
		t.Errorf("path = %q, want /sessions", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if gotBody["report_to"] != "conv1" || gotBody["initial_prompt"] != "hi" {
		t.Errorf("body = %v", gotBody)
	}
}

// TestInjectSessionAgentError surfaces a non-2xx Agent response as an error.
func TestInjectSessionAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"quota_sessions"}`))
	}))
	defer srv.Close()
	f := &wakeFirer{}
	err := f.injectSession(context.Background(), stubRuntime{endpoint: srv.URL}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error on 409, got nil")
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		OwnerConv: "conv1", TZ: "UTC", Prompt: "run at {{time}}", Report: true,
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
	// Without the report opt-in (the default) the owner conv must NOT ride as report_to.
	sch.Report = false
	_ = json.Unmarshal(buildInjectBody(sch, slot), &m)
	if m["report_to"] != "" {
		t.Errorf("report_to = %v, want empty when report is off (default)", m["report_to"])
	}
	sch.Report = true
	if m["initial_prompt"] != "run at 00:00" {
		t.Errorf("initial_prompt = %v", m["initial_prompt"])
	}
	if m["idempotency_key"] != "sch_sch_1@2026-07-23T00:00:00Z" {
		t.Errorf("idempotency_key = %v", m["idempotency_key"])
	}
	if m["dir"] != "/home/dev/repos/x" {
		t.Errorf("dir = %v", m["dir"])
	}
	// The mirror badges schedule-driven prompts (docs/log/38): a timed fire tags "schedule",
	// a run-now（手動発火・ManualFirePending）tags "schedule-manual".
	if m["source"] != "schedule" {
		t.Errorf("source = %v, want schedule", m["source"])
	}
	sch.ManualFirePending = true
	_ = json.Unmarshal(buildInjectBody(sch, slot), &m)
	if m["source"] != "schedule-manual" {
		t.Errorf("manual-fire source = %v, want schedule-manual", m["source"])
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
	sch := Schedule{ID: "sch_1", AgentKind: "claude", OwnerConv: "conv1", Prompt: "hi", Report: true}
	body := buildInjectBody(sch, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC))
	name, err := f.injectSession(context.Background(), stubRuntime{endpoint: srv.URL, token: "tok"}, body)
	if err != nil {
		t.Fatalf("injectSession: %v", err)
	}
	if name != "s7" {
		t.Errorf("session name = %q, want s7 (parsed from create_session response)", name)
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
	_, err := f.injectSession(context.Background(), stubRuntime{endpoint: srv.URL}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error on 409, got nil")
	}
}

// --- unattended-start hardening (8:00 スカウト取りこぼしの回帰) --------------------
//
// The failure: a scheduled wake started the container, the entrypoint's synchronous
// agent-CLI self-update ran before `exec workspace-agent`, and the fixed 15s health
// budget elapsed while it was still installing — so the fire was recorded as
// "error:wake: agent did not become healthy within 15s" and the prompt never landed.

// TestStartHealthWaitSelfUpdate: the docker start budget.
//
// It used to stretch to 300s exactly for the boot that can run the long pre-agent
// update — because overrunning it was a FAILURE. It is not a failure any more
// (runtime_health.go): the budget only decides whether Start answers "running" or
// "starting", so a self-updating boot no longer needs a longer one, and a 300s block
// inside an HTTP request is what the Runtime port forbids (docs/log/62 §62.5 = a 504).
// What must stay pinned is the unattended carve-out: the scheduler's tick goroutine
// polls the Agent itself afterwards, so making it sit here buys nothing.
func TestStartHealthWaitSelfUpdate(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want time.Duration
	}{
		{"no self-update", []string{"FOO=1"}, dockerStartGrace},
		{"opt-in allowed but off", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1"}, dockerStartGrace},
		{"self-update on", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1", "AF_AGENT_SELF_UPDATE=1"}, dockerStartGrace},
		// The unattended marker wins even alongside the opt-in: nobody is waiting on
		// this answer, and no update runs either.
		{"unattended overrides", []string{"AF_AGENT_SELF_UPDATE=1", unattendedStartEnv}, 15 * time.Second},
		{"unattended alone", []string{unattendedStartEnv}, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dockerRuntime{extraEnv: tc.env}
			if got := d.startHealthWait(); got != tc.want {
				t.Fatalf("startHealthWait(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
	// 同期で待つ猶予は、必ず「Agent を要する API が待つ総量」の内側に収める。逆転すると
	// ensureWorkspaceReady が入口で決めた期限を Start だけで食い潰し、到達待ちが 0 になる。
	if dockerStartGrace >= agentReadyWait() {
		t.Fatalf("start grace %v must stay under the ready budget %v", dockerStartGrace, agentReadyWait())
	}
}

// TestUnattendedStartEnvIsNotOptOut pins the distinction that makes the per-boot skip
// safe: it must NOT be spelled as the member opt-in being off, whose entrypoint branch
// also tears down the ~/.local shadow and reverts to the baked pin.
func TestUnattendedStartEnvIsNotOptOut(t *testing.T) {
	if unattendedStartEnv == "AF_AGENT_SELF_UPDATE=0" {
		t.Fatal("unattended start must not reuse the opt-in-off env: that branch uninstalls the ~/.local CLI shadow")
	}
	if !strings.HasSuffix(unattendedStartEnv, "=1") || !strings.HasPrefix(unattendedStartEnv, "AF_") {
		t.Fatalf("unexpected unattended marker %q", unattendedStartEnv)
	}
}

// TestAwaitAgentReadyToleratesSlowBoot is the tolerance the fire path now falls through
// to when a wake overruns its health budget: an Agent that is not answering yet (still
// installing / still booting) is polled, not failed. This is why a slow start must no
// longer abort the fire — the very next step already waits patiently.
func TestAwaitAgentReadyToleratesSlowBoot(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 { // still coming up
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer srv.Close()

	f := &wakeFirer{mgr: &manager{}, readyTimeout: 30 * time.Second, readyInterval: time.Millisecond}
	if err := f.awaitAgentReady(context.Background(), stubRuntime{endpoint: srv.URL}); err != nil {
		t.Fatalf("awaitAgentReady should tolerate a slow boot, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected retries until ready, got %d calls", calls)
	}
}

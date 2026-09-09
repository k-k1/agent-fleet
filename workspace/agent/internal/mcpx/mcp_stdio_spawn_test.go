package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Session steering (ADR 0073): the eight tools opened by `--self-report --fleet-spawn`.
var fleetSpawnToolNames = []string{
	"create_session", "list_repos", "list_models", "get_agent_usage",
	"get_session_output", "stop_session", "stop_session_after_turn", "resume_session",
}

func withFleetSpawn(t *testing.T, on bool) {
	t.Helper()
	oldSpawn, oldSelfReport, oldPeer := mcpFleetSpawnEnabled, selfReportOnly(), mcpPeerMessagingEnabled
	oldSource := mcpSourceSession
	t.Cleanup(func() {
		mcpFleetSpawnEnabled = oldSpawn
		setSelfReportOnly(oldSelfReport)
		mcpPeerMessagingEnabled = oldPeer
		mcpSourceSession = oldSource
	})
	setSelfReportOnly(true)
	mcpFleetSpawnEnabled = on
	mcpSourceSession = "parent1"
}

// The conjunction is the scope boundary between the two surfaces: an accidental or guessed
// --fleet-spawn on an assistant invocation must not widen that assistant.
func TestFleetSpawnRequiresSelfReport(t *testing.T) {
	oldWrite, oldSelfReport, oldSpawn := writeEnabled(), selfReportOnly(), mcpFleetSpawnEnabled
	t.Cleanup(func() {
		setWriteEnabled(oldWrite)
		setSelfReportOnly(oldSelfReport)
		mcpFleetSpawnEnabled = oldSpawn
	})
	for _, args := range [][]string{
		{"--fleet-spawn"},
		{"--write", "--fleet-spawn"},
	} {
		mcpFleetSpawnEnabled = false
		parseStdioFlags(args)
		if mcpFleetSpawnEnabled {
			t.Errorf("%v enabled session steering without --self-report", args)
		}
	}
	parseStdioFlags([]string{"--self-report", "--fleet-spawn"})
	if !mcpFleetSpawnEnabled {
		t.Error("--self-report --fleet-spawn did not enable session steering")
	}
}

// The advertised set IS the authorization boundary, so the eight appear only with the opt-in —
// and the opt-in adds those eight and NOTHING else. The second half is the half that catches a
// future edit reaching for a neighbouring operator tool while it is in the area.
func TestFleetSpawnAddsExactlyItsEightTools(t *testing.T) {
	withFleetSpawn(t, false)
	before := advertisedNames(t)
	for _, name := range fleetSpawnToolNames {
		if before[name] {
			t.Errorf("%s is advertised to a session without the opt-in", name)
		}
	}

	withFleetSpawn(t, true)
	after := advertisedNames(t)
	for _, name := range fleetSpawnToolNames {
		if !after[name] {
			t.Errorf("%s is missing although session steering is on", name)
		}
	}
	for name := range after {
		if !before[name] && !contains(fleetSpawnToolNames, name) {
			t.Errorf("%s appeared with --fleet-spawn but is not one of its eight tools", name)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The steering tools name a target, so the advertised set alone is not enough: a session may
// drive only what it started. Two conditions, both load-bearing — a fork of a child keeps the
// lineage but is origin=handoff, and a person made it.
func TestSessionDriveAllowedOnlyForOwnChildren(t *testing.T) {
	withFleetSpawn(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	for _, m := range []session.Meta{
		{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser},
		{Name: "mine", Kind: session.KindClaude, Origin: session.OriginSession, OriginSession: "parent1"},
		{Name: "theirs", Kind: session.KindClaude, Origin: session.OriginSession, OriginSession: "parent2"},
		{Name: "peer", Kind: session.KindClaude, Origin: session.OriginUser},
		{Name: "forked", Kind: session.KindClaude, Origin: session.OriginHandoff, OriginSession: "parent1"},
	} {
		session.WriteMeta(m)
	}
	for _, tc := range []struct {
		name  string
		allow bool
	}{
		{"mine", true},
		{"theirs", false},
		{"peer", false},
		{"forked", false},
		{"parent1", false}, // not even itself: a session drives its children, not its own slot
		{"ghost", false},
	} {
		err := sessionDriveAllowed(tc.name)
		if tc.allow && err != nil {
			t.Errorf("%s: refused (%v), want allowed", tc.name, err)
		}
		if !tc.allow && err == nil {
			t.Errorf("%s: allowed, want refused", tc.name)
		}
	}

	// The operator surface is unrestricted — including read-only, which also advertises
	// get_session_output.
	setSelfReportOnly(false)
	if err := sessionDriveAllowed("theirs"); err != nil {
		t.Errorf("the operator surface was put through the child check: %v", err)
	}
}

// A route test: the pieces above are only worth anything if tools/call actually reaches them.
// This one goes through mcpStdioCall with a stubbed Agent and reads what was POSTed.
func TestCreateSessionFromSessionStampsLineageAndDefaults(t *testing.T) {
	withFleetSpawn(t, true)
	mcpPeerMessagingEnabled = true
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"name":"slot09"}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	call := func(args map[string]any) {
		t.Helper()
		a, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": "create_session", "arguments": json.RawMessage(a)})
		if resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}); strings.Contains(string(resp), `"isError":true`) {
			t.Fatalf("create_session failed: %s", resp)
		}
	}

	call(map[string]any{"dir": "/repos/app", "initial_prompt": "rebase onto develop"})
	if body["origin"] != session.OriginSession || body["origin_session"] != "parent1" {
		t.Fatalf("origin = %v / %v, want session / parent1", body["origin"], body["origin_session"])
	}
	if body["origin_conv"] != "" || body["report_to"] != "" {
		t.Fatalf("a session create carried a conversation: conv=%v report_to=%v", body["origin_conv"], body["report_to"])
	}
	// Decision 7: a session's default is a NEW worktree, so parent and child never share one
	// working copy.
	if body["worktree"] != true {
		t.Fatalf("worktree = %v, want true by default from a session", body["worktree"])
	}
	// Decision 9: the report-back line is written by the server, not left to the caller.
	prompt, _ := body["initial_prompt"].(string)
	if !strings.Contains(prompt, "send_to_peer_session") || !strings.Contains(prompt, "parent1") {
		t.Fatalf("no report-back instruction in the launch task: %q", prompt)
	}
	firstKey, _ := body["idempotency_key"].(string)

	// An explicit worktree=false is honoured (the Agent decides whether that directory is free).
	call(map[string]any{"dir": "/repos/app", "initial_prompt": "rebase onto develop", "worktree": false})
	if body["worktree"] != false {
		t.Fatalf("explicit worktree=false was overridden: %v", body["worktree"])
	}

	// report_back=false drops the line but keeps the task.
	call(map[string]any{"dir": "/repos/app", "initial_prompt": "rebase onto develop", "report_back": false})
	if prompt, _ := body["initial_prompt"].(string); strings.Contains(prompt, "send_to_peer_session") {
		t.Fatalf("report_back=false still asked for a report: %q", prompt)
	}

	// Decision 2: the idempotency key is namespaced by the CALLER. Two sessions launching the
	// same thing must not collapse onto one child.
	mcpSourceSession = "parent2"
	session.WriteMeta(session.Meta{Name: "parent2", Kind: session.KindClaude, Origin: session.OriginUser})
	call(map[string]any{"dir": "/repos/app", "initial_prompt": "rebase onto develop"})
	if key, _ := body["idempotency_key"].(string); key == firstKey {
		t.Fatalf("two sessions produced the same idempotency key (%s): their launches would collapse into one", key)
	}
}

// Decision 10: a parent folding up a child does not withdraw the operator's instruction, so a
// session-issued stop leaves the arm alone. The operator's own stop still disarms.
func TestStopSessionDisarmsOnlyForTheOperator(t *testing.T) {
	withFleetSpawn(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	session.WriteMeta(session.Meta{Name: "mine", Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1"})

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	stop := func() {
		t.Helper()
		a, _ := json.Marshal(map[string]any{"name": "mine"})
		params, _ := json.Marshal(map[string]any{"name": "stop_session", "arguments": json.RawMessage(a)})
		if resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}); strings.Contains(string(resp), `"isError":true`) {
			t.Fatalf("stop_session failed: %s", resp)
		}
	}

	// The gate has to be WIRED, not merely written: a stop aimed at a session this one did not
	// start must not reach the Agent at all. (A unit test of sessionDriveAllowed cannot see a
	// handler that forgot to call it.)
	body = nil
	a, _ := json.Marshal(map[string]any{"name": "somebody-else"})
	params, _ := json.Marshal(map[string]any{"name": "stop_session", "arguments": json.RawMessage(a)})
	if resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`9`), Params: params}); !strings.Contains(string(resp), `"isError":true`) {
		t.Fatalf("stopping a session that is not this one's child was allowed: %s", resp)
	}
	if body != nil {
		t.Fatal("the refused stop still reached the Agent")
	}

	stop()
	if body["disarm_report"] != false {
		t.Fatalf("a session's stop sent disarm_report=%v; it must leave the instruction ledger alone", body["disarm_report"])
	}

	setSelfReportOnly(false)
	setWriteEnabled(true)
	t.Cleanup(func() { setWriteEnabled(false) })
	stop()
	if body["disarm_report"] != true {
		t.Fatalf("the operator's stop no longer disarms its own report: %v", body["disarm_report"])
	}
}

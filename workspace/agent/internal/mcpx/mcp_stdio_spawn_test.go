package mcpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Session steering (ADR 0073): the nine tools opened by `--self-report --fleet-spawn`.
var fleetSpawnToolNames = []string{
	"create_session", "list_child_sessions", "list_repos", "list_models", "get_agent_usage",
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

// The advertised set IS the authorization boundary, so the nine appear only with the opt-in —
// and the opt-in adds those nine and NOTHING else. The second half is the half that catches a
// future edit reaching for a neighbouring operator tool while it is in the area.
func TestFleetSpawnAddsExactlyItsNineTools(t *testing.T) {
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
			t.Errorf("%s appeared with --fleet-spawn but is not one of its nine tools", name)
		}
	}
}

// withSpawnChildLimit forces the configured child limit for one test.
//
// It sets the hook directly rather than writing a prefs file: this package does not link
// uiprefs (mcpx reads prefs through deps.ReadUIPrefs), so there is nothing here to run the init
// that wires it. That the wiring exists at all is fixed one package over —
// uiprefs.TestSpawnChildLimitReachesTheSessionPackage — and the two together are the route.
func withSpawnChildLimit(t *testing.T, n int) {
	t.Helper()
	old := session.SpawnChildLimitPref
	t.Cleanup(func() { session.SpawnChildLimitPref = old })
	session.SpawnChildLimitPref = func() int { return n }
}

// The child limit is a user setting, and this is the surface that SAYS it: create_session's
// description states the ceiling so a caller learns it before planning around one it does not
// have (ADR 0073 decision 6).
//
// A baked-in "3" passes every test of the advertised SET — the tool is there, its name is right,
// its schema is right — while telling every session a number that is not in force. So the
// assertion is on the sentence, at two configured values, and the second is what rules out a
// coincidence with the default.
func TestCreateSessionDescriptionStatesTheConfiguredLimit(t *testing.T) {
	withFleetSpawn(t, true)
	for _, limit := range []int{1, session.SpawnChildLimitMax} {
		withSpawnChildLimit(t, limit)
		desc := ""
		for _, tool := range mcpStdioToolList() {
			if name, _ := tool["name"].(string); name == "create_session" {
				desc, _ = tool["description"].(string)
			}
		}
		if desc == "" {
			t.Fatal("create_session is not advertised")
		}
		want := fmt.Sprintf("at most %d children at a time", limit)
		if !strings.Contains(desc, want) {
			t.Errorf("description does not state the limit in force (%q): %s", want, desc)
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

// Advertising a tool and being able to CALL it are different things, and the gap between them
// is invisible to a tools/list test: list_models shipped advertised but refusing, and
// create_session's own description sends the caller there first.
//
// So: call all eight on the session surface and refuse to accept a permission error from any of
// them. The Agent is stubbed, so what is under test is the gate, not the backend.
func TestFleetSpawnToolsAreCallableNotJustAdvertised(t *testing.T) {
	withFleetSpawn(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	session.WriteMeta(session.Meta{Name: "mine", Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"models":[],"repos":[],"output":"x","cursor":1}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	// Arguments good enough to get past validation; the target is always this session's child.
	args := map[string]map[string]any{
		"create_session":          {"dir": "/repos/app", "initial_prompt": "task"},
		"list_child_sessions":     {},
		"list_repos":              {},
		"list_models":             {"kind": "claude"},
		"get_agent_usage":         {},
		"get_session_output":      {"name": "mine"},
		"stop_session":            {"name": "mine"},
		"stop_session_after_turn": {"name": "mine"},
		"resume_session":          {"name": "mine"},
	}
	for _, name := range fleetSpawnToolNames {
		a, _ := json.Marshal(args[name])
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(a)})
		resp := string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
		// The refusals this catches all read "許可されていません" — a tool advertised to this
		// surface answering "you are not allowed" is the contradiction.
		if strings.Contains(resp, "許可されていません") {
			t.Errorf("%s is advertised to a session but refuses the call: %s", name, resp)
		}
		// The other way the same hole opens, and the one the first check misses entirely: the
		// tool is advertised and gated correctly but has no case at all, so the dispatch falls
		// through to "unknown tool". Found by breaking it (docs/log/89) — renaming the new
		// tool's case left this test green.
		if strings.Contains(resp, "unknown tool") {
			t.Errorf("%s is advertised to a session but has no handler: %s", name, resp)
		}
	}
}

// A child the user archived is out of the parent's hands. Reviving one would put a live agent on
// the host with no row in the active list — and would make archiving a way around ADR 0073
// decision 13, which keeps archive and delete closed even for one's own children.
func TestSessionDriveRefusesArchivedChild(t *testing.T) {
	withFleetSpawn(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	session.WriteMeta(session.Meta{Name: "live", Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1"})
	session.WriteMeta(session.Meta{Name: "shelved", Kind: session.KindClaude, Archived: true,
		Origin: session.OriginSession, OriginSession: "parent1"})

	if err := sessionDriveAllowed("live"); err != nil {
		t.Fatalf("a live child was refused: %v", err)
	}
	if err := sessionDriveAllowed("shelved"); err == nil {
		t.Fatal("an archived child could still be driven (resume would revive it unseen)")
	}
}

// The output cursor has to have a scope on the session surface, or "omit since to continue from
// where you last read" is false exactly where it is advertised — and every poll re-reads the
// whole tail into the caller's context.
func TestOutputCursorScopedToTheSessionWithoutAConversation(t *testing.T) {
	withFleetSpawn(t, true)
	setConvID("")
	if got := outputCursorScope(); got != "parent1" {
		t.Fatalf("session-side cursor scope = %q, want the session's own name", got)
	}
	// The operator keeps its conversation as the scope.
	setSelfReportOnly(false)
	setConvID("conv-1")
	t.Cleanup(func() { setConvID("") })
	if got := outputCursorScope(); got != "conv-1" {
		t.Fatalf("operator cursor scope = %q, want conv-1", got)
	}
}

// list_child_sessions (docs/log/89) is the answer to "a parent cannot enumerate its own
// children": every steering tool takes a name, and the only place a name was ever handed out
// was one create_session result, which a compaction throws away.
//
// Driven through tools/call against a stub Agent, so the routing, the lineage filter and the
// row shape are all exercised on the path a model actually takes.
func TestListChildSessionsReturnsOnlyOwnChildrenAndTheSlotCount(t *testing.T) {
	withFleetSpawn(t, true) // caller is parent1
	// Deliberately NOT the default: slotLimit is the other place the budget is stated out loud,
	// and against a limit of three a hardcoded three would look identical to a read one.
	withSpawnChildLimit(t, 5)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Errorf("list_child_sessions asked for %q, want the sessions listing", r.URL.Path)
		}
		// The listing itself already drops archived rows and prunes expired stopped ones, so
		// what arrives here is what still holds a slot.
		_, _ = w.Write([]byte(`{"sessions":[
			{"name":"mine","kind":"claude","dir":"/repos/app","title":"the split-out task",
			 "createdAt":"2026-09-09T10:00:00+09:00","state":"idle","alive":true,
			 "lastTurnEndAt":"2026-09-09T11:30:00+09:00",
			 "origin":"session","originSession":"parent1"},
			{"name":"folded","kind":"codex","dir":"/repos/app","createdAt":"2026-09-09T09:00:00+09:00",
			 "state":"","alive":false,"origin":"session","originSession":"parent1"},
			{"name":"theirs","kind":"claude","dir":"/repos/b","createdAt":"2026-09-09T08:00:00+09:00",
			 "state":"working","alive":true,"origin":"session","originSession":"parent2"},
			{"name":"forked","kind":"claude","dir":"/repos/c","createdAt":"2026-09-09T07:00:00+09:00",
			 "state":"idle","alive":true,"origin":"handoff","originSession":"parent1"},
			{"name":"theusers","kind":"claude","dir":"/repos/d","createdAt":"2026-09-09T06:00:00+09:00",
			 "state":"idle","alive":true,"origin":"user"}
		]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	params, _ := json.Marshal(map[string]any{"name": "list_child_sessions", "arguments": json.RawMessage(`{}`)})
	raw := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})

	var resp struct {
		Result struct {
			StructuredContent struct {
				Sessions  []map[string]any `json:"sessions"`
				SlotsLeft int              `json:"slotsLeft"`
				SlotLimit int              `json:"slotLimit"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	got := resp.Result.StructuredContent
	names := []string{}
	for _, row := range got.Sessions {
		names = append(names, row["name"].(string))
	}
	// theirs = another parent's child; forked = a fork of a child, which keeps the lineage but
	// was made by a person in the Console (the same two conditions sessionDriveAllowed uses);
	// theusers = not a spawned session at all.
	if strings.Join(names, ",") != "mine,folded" {
		t.Fatalf("rows = %v, want only this session's own children", names)
	}
	limit := session.SpawnChildLimit()
	if got.SlotLimit != limit || got.SlotsLeft != limit-2 {
		t.Fatalf("slots = %d/%d, want %d left of %d", got.SlotsLeft, got.SlotLimit, limit-2, limit)
	}

	mine, folded := got.Sessions[0], got.Sessions[1]
	if mine["title"] != "the split-out task" || mine["dir"] != "/repos/app" ||
		mine["kind"] != "claude" || mine["createdAt"] != "2026-09-09T10:00:00+09:00" {
		t.Errorf("row lost a field a parent decides on: %v", mine)
	}
	// The one field the whole design turns on: without it "idle" cannot tell a child that
	// finished from one that never started (ADR 0073 decision 9).
	if mine["lastTurnEndAt"] != "2026-09-09T11:30:00+09:00" {
		t.Errorf("lastTurnEndAt did not reach the row: %v", mine)
	}
	// The listing carries "stopped" as alive=false with an empty state; a blank state next to
	// a name reads as "unknown", which is the one thing this row must not say.
	if folded["state"] != "stopped" {
		t.Errorf("stopped child's state = %v, want stopped", folded["state"])
	}
	if _, present := folded["lastTurnEndAt"]; present {
		t.Errorf("an absent turn end was filled in anyway: %v", folded)
	}
}

// The one tool of the nine that is NOT also an operator tool, so it is the one whose gate
// cannot be inherited from the write set. Both surfaces have to refuse it, and they refuse it
// at different layers: a session by the advertised-set check that fronts every call, an
// assistant by the case's own flag test (that check does not run on the operator surface, so
// without the flag test a --write assistant would reach the listing and get an empty answer
// keyed on a session name it does not have).
func TestListChildSessionsRefusedWithoutTheOptIn(t *testing.T) {
	params, _ := json.Marshal(map[string]any{"name": "list_child_sessions", "arguments": json.RawMessage(`{}`)})
	// A reachable Agent and a resolvable caller, so that a missing gate FAILS here instead of
	// falling over on something incidental: without them the write-assistant case would refuse
	// itself for having no session name and the control would look like it passed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	for _, tc := range []struct {
		surface     string
		write, self bool
	}{
		{"session without --fleet-spawn", false, true},
		{"write assistant", true, false},
	} {
		oldWrite, oldSelf, oldSpawn := writeEnabled(), selfReportOnly(), mcpFleetSpawnEnabled
		oldSource := mcpSourceSession
		setWriteEnabled(tc.write)
		setSelfReportOnly(tc.self)
		mcpFleetSpawnEnabled = false
		mcpSourceSession = "parent1"
		resp := string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
		setWriteEnabled(oldWrite)
		setSelfReportOnly(oldSelf)
		mcpFleetSpawnEnabled = oldSpawn
		mcpSourceSession = oldSource
		if !strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s: list_child_sessions answered without the opt-in: %s", tc.surface, resp)
		}
	}
}

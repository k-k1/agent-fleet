package sessionx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestToBrowseRel checks SendUserFile path normalization: absolute paths under the browse
// root become root-relative (forward-slashed), cwd-relative paths are anchored on the
// turn's cwd first, and anything outside the root (or a relative path with no cwd) is left
// untouched so it still lists in the panel even if it can't be opened.
func TestToBrowseRel(t *testing.T) {
	root := "/home/dev"
	cases := []struct {
		name, p, cwd, want string
	}{
		{"abs under root", "/home/dev/repos/x/report.md", "", "repos/x/report.md"},
		{"rel joined on cwd", "report.md", "/home/dev/repos/x", "repos/x/report.md"},
		{"rel dotted on cwd", "./out/a.png", "/home/dev/repos/x", "repos/x/out/a.png"},
		{"abs outside root", "/tmp/claude/scratch/a.png", "", "/tmp/claude/scratch/a.png"},
		{"rel no cwd", "report.md", "", "report.md"},
		{"cwd outside root", "a.png", "/tmp/work", "/tmp/work/a.png"},
	}
	for _, c := range cases {
		if got := toBrowseRel(c.p, c.cwd, root); got != c.want {
			t.Errorf("%s: toBrowseRel(%q,%q,%q) = %q, want %q", c.name, c.p, c.cwd, root, got, c.want)
		}
	}
}

func TestGenericMutableTail(t *testing.T) {
	all := []transcript.Turn{
		{Role: "user", Idx: 0, Text: "調べて"},
		{Role: "assistant", Idx: 1, Text: "最終回答"},
	}

	got := genericMutableTail(all, len(all))
	if len(got) != 1 || got[0].Idx != 1 || got[0].Text != "最終回答" {
		t.Fatalf("mutable tail = %+v, want the completed assistant turn", got)
	}
	if got := genericMutableTail(all, 1); got != nil {
		t.Fatalf("behind cursor tail = %+v, want nil", got)
	}
	if got := genericMutableTail(all[:1], 1); got != nil {
		t.Fatalf("user tail = %+v, want nil", got)
	}
}

const testPlan = "# D-1 改稿計画\n\n代償設計を見直す"

func planTurns() []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", Idx: 10, Text: "計画を出して"},
		{Role: "assistant", Idx: 11, Text: "こうします", Parts: []transcript.Part{{Kind: "text", Text: "こうします"}}},
		{Role: "assistant", Idx: 12, Parts: []transcript.Part{{Kind: "plan", Tool: "ExitPlanMode", Plan: testPlan, QID: "toolu_1"}}},
	}
}

// A plan awaiting approval exists twice: as the transcript's tool_use (written when it asks)
// and as the pending payload the hook captured. One of the two — the inline one — is dropped,
// and the cursor is rewound to that line, so this also covers that it can be shown again once
// the plan is decided.
func TestHidePendingInteractionPlan(t *testing.T) {
	pending := map[string]any{"pendingPlan": testPlan}
	turns, hold := hidePendingInteraction(planTurns(), pending, map[string]claude.InteractionAnswer{})

	if hold != 12 {
		t.Fatalf("hold = %d, want 12 (the plan's line — it must stay unconsumed)", hold)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %+v, want the plan-only turn dropped (no empty bubble)", turns)
	}
	if _, ok := pending["pendingPlan"]; !ok {
		t.Error("pendingPlan was withdrawn — the actionable card is the one that must survive")
	}
}

// The reverse when the payload is still pending although the plan was decided (the hook
// failed to clear it): withdraw the ghost card, not the history. Removing the history would
// rewind the cursor for ever.
func TestHidePendingInteractionStalePayload(t *testing.T) {
	pending := map[string]any{"pendingPlan": testPlan}
	answers := map[string]claude.InteractionAnswer{"toolu_1": {Text: "approved"}}
	turns, hold := hidePendingInteraction(planTurns(), pending, answers)

	if hold != -1 {
		t.Errorf("hold = %d, want -1 (nothing hidden)", hold)
	}
	if len(turns) != 3 {
		t.Errorf("turns = %d, want 3 (history untouched)", len(turns))
	}
	if _, ok := pending["pendingPlan"]; ok {
		t.Error("stale pendingPlan survived — it would show an awaiting-approval card for a decided plan")
	}
}

// A rejected plan may be presented again with the very same Markdown. What is pending is
// always the last presentation, and the older, decided card has to stay in the history.
func TestHidePendingInteractionRepresentedPlan(t *testing.T) {
	turns := append(planTurns(), transcript.Turn{
		Role: "assistant", Idx: 20,
		Parts: []transcript.Part{{Kind: "plan", Tool: "ExitPlanMode", Plan: testPlan, QID: "toolu_2"}},
	})
	answers := map[string]claude.InteractionAnswer{"toolu_1": {Text: "[Request interrupted by user for tool use]"}}
	got, hold := hidePendingInteraction(turns, map[string]any{"pendingPlan": testPlan}, answers)

	if hold != 20 {
		t.Fatalf("hold = %d, want 20 (the newest presentation)", hold)
	}
	if len(got) != 3 || got[2].Idx != 12 || got[2].Parts[0].QID != "toolu_1" {
		t.Fatalf("turns = %+v, want the decided first presentation kept", got)
	}
}

// An AUQ is duplicated the same way. The pending payload is tool_input.questions itself, so
// the two are matched in their parsed form (the hook side has no tool_use id).
func TestHidePendingInteractionQuestion(t *testing.T) {
	raw := json.RawMessage(`[{"header":"方式","question":"どれにしますか？","options":[{"label":"案A"},{"label":"案B"}]}]`)
	var qs []transcript.Question
	if err := json.Unmarshal(raw, &qs); err != nil {
		t.Fatal(err)
	}
	turns := []transcript.Turn{
		{Role: "assistant", Idx: 5, Text: "前置き", Parts: []transcript.Part{
			{Kind: "text", Text: "前置き"},
			{Kind: "question", Tool: "AskUserQuestion", Questions: qs, QID: "toolu_q"},
		}},
	}
	pending := map[string]any{"pendingQuestions": raw, "pendingText": "前置き"}
	got, hold := hidePendingInteraction(turns, pending, map[string]claude.InteractionAnswer{})

	if hold != 5 {
		t.Fatalf("hold = %d, want 5", hold)
	}
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].Kind != "text" {
		t.Fatalf("parts = %+v, want the question stripped and the prose kept", got)
	}
	if _, ok := pending["pendingQuestions"]; !ok {
		t.Error("pendingQuestions was withdrawn — the answerable card must survive")
	}
	// The prose right before the question is already in the transcript by the time the
	// question's tool_use is (claude writes the prose message first). Stacking it on the card
	// as well shows the same paragraph twice.
	if _, ok := pending["pendingText"]; ok {
		t.Error("pendingText survived — the same prose is already inline")
	}
}

// While a different question is pending, an unrelated question part that happens to be nearby
// must not be caught in the sweep.
func TestHidePendingInteractionNoMatch(t *testing.T) {
	turns := planTurns()
	pending := map[string]any{"pendingPlan": "# 別の計画"}
	got, hold := hidePendingInteraction(turns, pending, map[string]claude.InteractionAnswer{})

	if hold != -1 || len(got) != 3 {
		t.Fatalf("hold = %d, turns = %d, want -1 / 3 (unrelated payload changes nothing)", hold, len(got))
	}
}

// TestSweepSettledPending pins that a pending payload whose modal is gone gets swept, and
// that a modal still on screen does not.
//
// The real bug (user report 2026-08-31, "a cancelled AUQ keeps being asked again"): a cancel
// is a tool rejection, so AskUserQuestion's PostToolUse never fires and the pending-question
// stays. On the next poll after the settled line leaves the window, it is offered again as a
// live card with an answer form, which then responds to neither answering nor cancelling.
func TestSweepSettledPending(t *testing.T) {
	const sid = "sid-sweep"
	ask := []byte(`{"type":"assistant","timestamp":"2026-08-31T12:00:00.000Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]}}]}}`)
	// The real wording of a cancel, taken from a live transcript: it arrives as "the tool was
	// rejected", not as an answer.
	decided := []byte(`{"type":"user","timestamp":"2026-08-31T12:05:00.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"q1","is_error":true,"content":"The user doesn't want to proceed with this tool use. The tool use was rejected"}]}}`)
	raw := json.RawMessage(`[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]`)

	t.Run("a payload captured before the decision is swept", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.AppendPendingText(sid, "前置き")
		// Set up the relation that the payload was written when the question appeared, i.e.
		// before the decision.
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")

		// The sweep runs inside surfacePendingPayloads (the same place as the surfacing
		// path), so the surfacing side is called here too, pinning at once that the payload
		// is removed and that it is not surfaced.
		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "question", [][]byte{ask, decided})

		if _, ok := resp["pendingQuestions"]; ok {
			t.Error("a settled question was surfaced — the Console shows it as a live, unanswerable card")
		}
		if _, ok := status.ReadPendingQuestion(sid); ok {
			t.Error("pending question survived a settled modal — the next poll offers it again")
		}
		if _, ok := status.ReadPendingText(sid); ok {
			t.Error("pending text survived — it is only ever shown with the question card")
		}
	})

	t.Run("a payload captured after the decision is kept", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		// The hook wrote first and the tool_use line is not flushed yet (measured 106-122ms).
		// The decision visible in the transcript belongs to the PREVIOUS modal, and a live
		// question must not be removed because of it.
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"), "2026-08-31T12:05:00.100Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "question", [][]byte{ask, decided})

		if _, ok := resp["pendingQuestions"]; !ok {
			t.Error("a live question was not surfaced")
		}
		if _, ok := status.ReadPendingQuestion(sid); !ok {
			t.Error("a live question was swept — its payload was captured after the last decision")
		}
	})

	// An AUQ fires its own permission_prompt between Pre- and PostToolUse (measured:
	// state=permission 6 seconds after the question). That permission payload was hidden for
	// one reason only, that a question is pending, so unless it is dropped together with the
	// question, a permission dialog for an already-decided tool pops up right after a cancel
	// (user report; a regression introduced together with the sweep).
	t.Run("a permission the question was hiding goes with it", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.WritePendingPermission(sid, "Claude needs your permission")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".txt"), "2026-08-31T12:00:06.000Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, decided})

		if _, ok := resp["pendingPermission"]; ok {
			t.Error("a permission prompt for an already-decided tool was surfaced — the dialog pops right after a cancel")
		}
		if _, ok := status.ReadPendingPermission(sid); ok {
			t.Error("stale permission payload survived; the next poll pops the dialog again")
		}
	})

	// The other direction: a permission that arrived AFTER the decision is genuine (an Edit or
	// Bash awaiting approval) and must not be swept.
	t.Run("a permission after the decision is genuine, so keep it", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingPermission(sid, "Edit · /tmp/a.go")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".txt"), "2026-08-31T12:09:00.000Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, decided})

		if _, ok := resp["pendingPermission"]; !ok {
			t.Error("a live permission prompt was swept — it was captured after the last decision")
		}
	})

	// Removing the payload removes the state as well. Doing only one of the two leaves
	// "sending is refused although there is no card left to decide on" (user report
	// 2026-09-04: "after cancelling an AUQ, messages can no longer be sent"). The card
	// (display) and the state (decision) are folded up together by the same decision.
	t.Run("the permission state that was hidden is swept too", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.WritePendingPermission(sid, "Claude needs your permission")
		// The AUQ's own permission_prompt has overwritten the state with permission (measured:
		// 6 seconds after the question). A cancel fires no PostToolUse, so nobody rewrites it.
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".txt"), "2026-08-31T12:00:06.000Z")
		backdate(t, filepath.Join(paths.AgentStateDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); ok {
			t.Errorf("state %q survived the sweep — sending is refused with permission_pending while no card is left to decide on", st.State)
		}
		if got := status.LiveState(sid); got != "idle" {
			t.Errorf("LiveState = %q, want idle (if it is running, the next poll's reverse-heal puts it back to working)", got)
		}
	})

	// The other direction: while a live permission is still there (a genuine Edit/Bash
	// approval captured after the decision), the state has to keep refusing too, or free text
	// is swallowed by the permission menu and Enter presses "allow".
	t.Run("keep the state while a live permission remains", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingPermission(sid, "Edit · /tmp/a.go")
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".txt"), "2026-08-31T12:09:00.000Z")
		backdate(t, filepath.Join(paths.AgentStateDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "permission" {
			t.Errorf("state = %q/%v, want permission (sending must not be let through in front of a live approval dialog)", st.State, ok)
		}
	})

	// A state written AFTER the decision belongs to a new modal (including the 106-122ms in
	// which the hook writes first and the tool_use is flushed later). It must not be swept.
	t.Run("a state after the decision is kept", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentStateDir(), "session-status", sid+".json"), "2026-08-31T12:05:00.100Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "permission" {
			t.Errorf("state = %q/%v, want permission (a modal raised after the decision)", st.State, ok)
		}
	})

	// working / idle are out of the sweep's jurisdiction — they are about the turn, not about
	// a modal. Clearing them makes the turn right after an answer (PostToolUse→working) claim
	// to be waiting for input.
	t.Run("a state that is not a modal is left alone", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.Persist(sid, "working")
		backdate(t, filepath.Join(paths.AgentStateDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "working", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "working" {
			t.Errorf("state = %q/%v, want working", st.State, ok)
		}
	})

	t.Run("nothing is touched when there is no decision at all", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		backdate(t, filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")

		surfacePendingPayloads(map[string]any{}, sid, "question", [][]byte{ask})

		if _, ok := status.ReadPendingQuestion(sid); !ok {
			t.Error("pending question swept with no tool_result in the transcript")
		}
	})
}

// Pinned across the layers: after the sweep (the display side) has run, the send guard (the
// decision side) is actually open. A test on only one side misses the very fact that the two
// disagreed — the real bug (2026-09-04) was "the card is gone, yet sending is refused with
// permission_pending".
func TestCancelledInteractionFreesComposer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "auq_cancel"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)

	ask := []byte(`{"type":"assistant","timestamp":"2026-09-04T12:00:00.000Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]}}]}}`)
	cancelled := []byte(`{"type":"user","timestamp":"2026-09-04T12:05:00.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"q1","is_error":true,"content":"The user doesn't want to proceed with this tool use. The tool use was rejected"}]}}`)

	// The state just before the cancel: the question payload, the permission payload it was
	// hiding, and the state written by the AUQ's own permission_prompt.
	status.WritePendingQuestion(sid, json.RawMessage(`[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]`))
	status.WritePendingPermission(sid, "Claude needs your permission")
	status.Persist(sid, "permission")
	for path, ts := range map[string]string{
		filepath.Join(paths.AgentStateDir(), "pending-question", sid+".json"): "2026-09-04T12:00:00.100Z",
		filepath.Join(paths.AgentStateDir(), "pending-perm", sid+".txt"):      "2026-09-04T12:00:06.000Z",
		filepath.Join(paths.AgentStateDir(), "session-status", sid+".json"):   "2026-09-04T12:00:06.000Z",
	} {
		backdate(t, path, ts)
	}
	if got := promptBlocker(name); got != "question" {
		t.Fatalf("before the cancel it must steer to the question card: promptBlocker = %q", got)
	}

	resp := map[string]any{}
	surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, cancelled})

	if len(resp) != 0 {
		t.Fatalf("an already-decided modal was surfaced: %v", resp)
	}
	if got := promptBlocker(name); got != "" {
		t.Fatalf("promptBlocker = %q — sending is refused although not one card is left to decide on", got)
	}
}

// backdate sets a pending payload's mtime to when it was captured. The sweep decides on the
// capture time rather than the contents, so that is all the test sets up.
func backdate(t *testing.T, path, ts string) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// An unchanged poll must answer with the SAME BYTES.
//
// The mirror polls this route for as long as it is on screen, and the response carries
// whole-transcript aggregates (files / tasks / answers) whether or not they moved. What keeps
// that off a phone's radio is the Control Plane's conditional-GET layer (control-plane/etag.go):
// it hashes the proxied body and answers a matching If-None-Match with an empty 304. That layer
// is only ever as good as this handler's determinism — one map iterated in output order, one
// timestamp taken at request time, and every poll becomes a full body again, silently and
// forever (an ETag cannot tell "changed" from "differently ordered").
func TestUnchangedPollIsByteIdentical(t *testing.T) {
	home := withTempHome(t)
	// The container that runs this suite has CLAUDE_CONFIG_DIR pointing at the real state tree,
	// and HOME alone does not redirect it — without this the fixture would be written next to
	// the user's own conversations (and read from there).
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := t.TempDir()
	const name = "poll_ident"
	// Titled on purpose: an untitled session would reach the title-suggestion branch, whose
	// business is precisely to change the answer between two polls.
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude, Title: "既に題のある会話"})
	sid := session.UUID(dir, name)
	jsonl := filepath.Join(home, ".claude", "projects", "p", sid+".jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-09-12T10:00:00Z","cwd":"/home/dev/repos/x","message":{"role":"user","content":"直して"}}`,
		`{"type":"assistant","timestamp":"2026-09-12T10:00:05Z","cwd":"/home/dev/repos/x","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/home/dev/repos/x/a.ts","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"assistant","timestamp":"2026-09-12T10:00:09Z","cwd":"/home/dev/repos/x","message":{"role":"assistant","content":[{"type":"text","text":"直しました"}]}}`,
	}
	writeFile(t, jsonl, strings.Join(lines, "\n")+"\n")

	// The steady-state poll: the client's cursor is already at the end, so only the aggregates
	// and the status fields ride along.
	poll := func() []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/messages?since=3", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionMessages(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		return append([]byte(nil), rec.Body.Bytes()...)
	}

	first, second := poll(), poll()
	// Without this the test could pass on a response that carries no aggregates at all, which is
	// the one case where determinism is free.
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(first, &body); err != nil {
		t.Fatalf("the response is not JSON: %v (%s)", err, first)
	}
	if len(body.Files) == 0 {
		t.Fatalf("the fixture produced no file aggregate, so this proves nothing: %s", first)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two unchanged polls differ, so every poll is a full body over the wire:\n1: %s\n2: %s", first, second)
	}

	// …and the other half: a transcript that DID move must not be answered with the same bytes,
	// or the 304 above would hide new turns instead of saving them.
	writeFile(t, jsonl, strings.Join(append(lines,
		`{"type":"user","timestamp":"2026-09-12T10:01:00Z","cwd":"/home/dev/repos/x","message":{"role":"user","content":"ありがとう"}}`,
	), "\n")+"\n")
	if third := poll(); bytes.Equal(second, third) {
		t.Fatalf("a grown transcript answered identically: %s", third)
	}
}

// fakeApprovalAgent is a registered kind whose Transcript reports a pending approval, so the
// generic /messages path can be exercised without a live runtime.
type fakeApprovalAgent struct{ agents.Agent }

func (fakeApprovalAgent) Kind() string { return session.KindMuse }
func (fakeApprovalAgent) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, ManagedOnly: true}
}
func (fakeApprovalAgent) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, errors.New("no terminal route")
}
func (fakeApprovalAgent) WireLive(session.Meta, bool) agents.LiveInfo { return agents.LiveInfo{} }
func (fakeApprovalAgent) ClearResume(string)                          {}
func (fakeApprovalAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{
		Turns:             []transcript.Turn{{Role: "user", Text: "go", Idx: 0}},
		PendingApprovalID: "approval-ap-1",
		PendingApproval: &agents.ApprovalRequest{
			Summary: "rm -rf build", Command: "rm -rf build", Tool: "shell", ProtectedWrite: true,
		},
	}, true
}

// The approval has to reach the Console under its OWN key. `pendingPermission` is the TUI
// route's message string, answered by driving the pane with keystrokes; a managed session has
// no pane, so a surface that read the two as the same thing would render allow/deny buttons
// that send keys into nothing.
func TestGenericMessagesSurfacesAPendingApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := agentRegistry[session.KindMuse]
	agentRegistry[session.KindMuse] = fakeApprovalAgent{}
	t.Cleanup(func() { agentRegistry[session.KindMuse] = prev })

	m := session.Meta{Name: "slot-approval-wire", Dir: t.TempDir(), Kind: session.KindMuse,
		Driver: session.DriverManaged}
	session.WriteMeta(m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil)
	handleGenericMessages(rec, req, m, true, "question")

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	got, ok := resp["pendingApproval"].(map[string]any)
	if !ok {
		t.Fatalf("no pendingApproval in the response: %s", rec.Body.String())
	}
	if got["id"] != "approval-ap-1" {
		t.Errorf("id = %v", got["id"])
	}
	reqObj, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("request = %v", got["request"])
	}
	if reqObj["command"] != "rm -rf build" || reqObj["protectedWrite"] != true {
		t.Errorf("request = %v", reqObj)
	}
	// It must NOT double as a question: the question card renders option buttons, and an
	// approval has none.
	if _, ok := resp["pendingQuestions"]; ok {
		t.Error("the approval also went out as a question")
	}
}

// A stopped session cannot be answered, so the card must not be offered at all — the same rule
// pendingQuestions already follows.
func TestAStoppedSessionOffersNoApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := agentRegistry[session.KindMuse]
	agentRegistry[session.KindMuse] = fakeApprovalAgent{}
	t.Cleanup(func() { agentRegistry[session.KindMuse] = prev })

	m := session.Meta{Name: "slot-approval-dead", Dir: t.TempDir(), Kind: session.KindMuse,
		Driver: session.DriverManaged}
	session.WriteMeta(m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil)
	handleGenericMessages(rec, req, m, false, "")

	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if _, ok := resp["pendingApproval"]; ok {
		t.Error("a stopped session offered an approval nobody can answer")
	}
}

// fakeEditingAgent is a registered kind whose transcript contains one edit-family tool part
// and one plain trace. The kind slot it borrows is just a seat in the registry — what is
// under test is the GENERIC /messages path every store-backed kind (codex, opencode, lcpp,
// muse) shares, not that kind's own parser.
type fakeEditingAgent struct {
	agents.Agent
	cwd string
}

func (fakeEditingAgent) Kind() string { return session.KindMuse }
func (fakeEditingAgent) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, ManagedOnly: true}
}
func (fakeEditingAgent) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, errors.New("no terminal route")
}
func (fakeEditingAgent) WireLive(session.Meta, bool) agents.LiveInfo { return agents.LiveInfo{} }
func (fakeEditingAgent) ClearResume(string)                          {}
func (a fakeEditingAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: []transcript.Turn{
		{Role: "user", Text: "直して", Idx: 0, TS: "2026-09-22T10:00:00Z", Cwd: a.cwd},
		{Role: "assistant", Idx: 1, TS: "2026-09-22T10:00:05Z", Cwd: a.cwd, Parts: []transcript.Part{
			{Kind: "tool", Tool: "edit", File: "a.go",
				Edits: []transcript.Edit{{Old: "old\n", New: "new\n"}}},
			{Kind: "tool", Tool: "bash", Info: "go build ./..."},
		}},
		{Role: "assistant", Idx: 2, TS: "2026-09-22T10:00:09Z", Cwd: a.cwd, Text: "直しました"},
	}}, true
}

// The changed-files strip (decisions/0049) is fed by `files` on /messages and by nothing else,
// so a kind that fills Part.File must see it come out the other end of the generic path. The
// bash part is the negative control: only edit-family parts count, or the list names files
// nobody touched. The turn's Cwd is what anchors the relative path — without it the row is
// dropped silently, which looks exactly like a session that edited nothing.
func TestGenericMessagesEmitsChangedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	prev := agentRegistry[session.KindMuse]
	agentRegistry[session.KindMuse] = fakeEditingAgent{cwd: dir}
	t.Cleanup(func() { agentRegistry[session.KindMuse] = prev })

	m := session.Meta{Name: "slot-changed-files", Dir: dir, Kind: session.KindMuse,
		Driver: session.DriverManaged}
	session.WriteMeta(m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil)
	handleGenericMessages(rec, req, m, true, "idle")

	var resp struct {
		Files []transcript.FileTouch `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %+v, want exactly the one edited file: %s", resp.Files, rec.Body.String())
	}
	got := resp.Files[0]
	if !strings.HasSuffix(got.Path, "a.go") {
		t.Errorf("path = %q, want the edited file", got.Path)
	}
	if got.Verb != "edit" || got.Count != 1 || got.Added != 1 || got.Removed != 1 {
		t.Errorf("row = %+v, want one edit call, +1 -1", got)
	}
}

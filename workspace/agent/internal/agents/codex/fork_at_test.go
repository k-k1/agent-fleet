package codex

// Unit tests for the codex side of forking at a message (docs/log/55).
//
// codex is the only kind that does not send the anchor as is. The Console's meaning is
// exclusive (up to just before this message) while `thread/fork`'s lastTurnId is inclusive
// (keep through this turn), so the answer is the previous turn. Get that backwards and the
// new conversation carries the very message the user wanted to redo — and the mirror still
// looks like the fork worked.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// rollout3Turns writes a rollout with 3 turns under an isolated HOME and maps the slot to
// it, so ResolveForkAt resolves exactly like it does at runtime.
func rollout3Turns(t *testing.T) (session.Meta, []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cxid = "019f9830-6b4f-7a70-9834-6d5247150090"
	ids := []string{
		"019f9830-aaaa-7b43-a606-f61767644baa",
		"019f9830-bbbb-7b43-a606-f61767644bbb",
		"019f9830-cccc-7b43-a606-f61767644ccc",
	}
	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"cwd":"` + dir + `"}}` + "\n")
	for i, id := range ids {
		b.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + id + `"}}` + "\n")
		b.WriteString(`{"type":"turn_context","payload":{"turn_id":"` + id + `","model":"gpt-5.5"}}` + "\n")
		b.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt ` + string(rune('A'+i)) + `"}]}}` + "\n")
		b.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reply ` + string(rune('A'+i)) + `"}]}}` + "\n")
		b.WriteString(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + id + `"}}` + "\n")
	}
	sessDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "08")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sessDir, "rollout-2026-08-08T00-00-00-"+cxid+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fork point can only be passed through app-server, i.e. managed; ResolveForkAt
	// checks the route too.
	m := session.Meta{Dir: dir, Name: "cx", Kind: session.KindCodex, Driver: session.DriverManaged}
	sids.Write(session.UUID(dir, "cx"), cxid)
	return m, ids
}

// The CLI (TUI) route has no way to pass a fork point. The reason is "not possible on this
// route", not "bad anchor", so it answers with ErrForkAtRoute and the handler can tell the
// two apart.
func TestResolveForkAtRefusesCLIRoute(t *testing.T) {
	m, ids := rollout3Turns(t)
	m.Driver = session.DriverTUI
	_, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[1]})
	if err == nil {
		t.Fatal("ResolveForkAt on the CLI route = nil error; want a refusal")
	}
	if !errors.Is(err, agents.ErrForkAtRoute) {
		t.Fatalf("error = %v; want it to wrap ErrForkAtRoute", err)
	}
}

// Every transcript turn carries the turn id of the rollout it belongs to as its anchor. The
// Console picks that up and sends it back, so an empty one means the entry point never
// appears.
func TestTranscriptCarriesTurnAnchor(t *testing.T) {
	m, ids := rollout3Turns(t)
	td, _ := readTranscript(m)
	if len(td.Turns) == 0 {
		t.Fatal("no turns parsed")
	}
	var userAnchors []string
	for _, tn := range td.Turns {
		if tn.Role == "user" {
			userAnchors = append(userAnchors, tn.AnchorID)
		}
	}
	if len(userAnchors) != 3 {
		t.Fatalf("user turns = %d, want 3", len(userAnchors))
	}
	for i, got := range userAnchors {
		if got != ids[i] {
			t.Errorf("user turn %d anchor = %q, want %q", i, got, ids[i])
		}
	}
}

// The main point: the anchor is converted to the previous turn id, because lastTurnId is
// inclusive.
func TestResolveForkAtReturnsPreviousTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[2]})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if got != ids[1] {
		t.Fatalf("ResolveForkAt(turn3) = %q; want turn2 (%q) — lastTurnId is inclusive", got, ids[1])
	}
	if got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[1]}); err != nil || got != ids[0] {
		t.Fatalf("ResolveForkAt(turn2) = %q, %v; want turn1 (%q)", got, err, ids[0])
	}
}

// "continue from this message" (Include): lastTurnId is inclusive in codex, so the turn
// itself is what gets passed. Only the exclusive side needs the shift.
func TestResolveForkAtIncludeKeepsAnchorTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	for i, id := range ids {
		got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: id, Include: true})
		if err != nil {
			t.Fatalf("ResolveForkAt(turn%d, include): %v", i+1, err)
		}
		if got != id {
			t.Errorf("ResolveForkAt(turn%d, include) = %q; want the turn itself (%q)", i+1, got, id)
		}
	}
	// The first exchange works too when it is "continue from"; only the exclusive form
	// cannot be expressed there.
	if _, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[0], Include: true}); err != nil {
		t.Errorf("include on the first turn should be representable: %v", err)
	}
}

// Forking at the first exchange leaves lastTurnId empty, which to codex means the WHOLE
// conversation — the exact opposite. Refuse rather than send it.
func TestResolveForkAtRefusesFirstTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[0]})
	if err == nil {
		t.Fatalf("ResolveForkAt(first turn) = %q, nil; want a refusal (empty lastTurnId = the whole conversation)", got)
	}
	if got != "" {
		t.Fatalf("ResolveForkAt(first turn) returned %q alongside the error", got)
	}
}

func TestResolveForkAtRejectsUnknownAnchor(t *testing.T) {
	m, _ := rollout3Turns(t)
	for _, anchor := range []string{"", "019f9830-9999-7b43-a606-f61767644999"} {
		if _, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: anchor}); err == nil {
			t.Errorf("ResolveForkAt(%q) = nil error; want one", anchor)
		}
	}
}

// lastTurnId is present in thread/fork's params, and absent when there is no fork point.
// Sending it empty means the whole conversation to codex, so the key itself must be gone.
func TestThreadForkSendsLastTurnID(t *testing.T) {
	m, cl := newMockCodexServer(t)

	_, _ = threadFork(cl, "src-thread", "/dir", "turn-2", "")
	p, ok := m.lastCall("thread/fork")
	if !ok {
		t.Fatal("thread/fork was never called")
	}
	var got map[string]any
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatalf("params: %v", err)
	}
	if got["threadId"] != "src-thread" {
		t.Errorf("threadId = %v", got["threadId"])
	}
	if got["lastTurnId"] != "turn-2" {
		t.Errorf("lastTurnId = %v; want turn-2", got["lastTurnId"])
	}

	_, _ = threadFork(cl, "src-thread", "/dir", "", "")
	p, _ = m.lastCall("thread/fork")
	got = nil
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, present := got["lastTurnId"]; present {
		t.Errorf("whole-conversation fork sent lastTurnId=%v; want the key absent", got["lastTurnId"])
	}
}

func TestBuildLaunchRefusesForkAtOnCLIRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Dir: dir, Name: "cx2", Kind: session.KindCodex, ForkFrom: "src-id", ForkAt: "turn-id"}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch with ForkAt on the CLI route = nil error; want a refusal")
	}
}

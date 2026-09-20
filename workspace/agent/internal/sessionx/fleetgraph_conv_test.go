package sessionx

// ADR 0096 decision 13's "became resolvable for the first time" case (S-BE review finding
// B1): a birth row's own `conv` is usually empty (no conversation exists yet at create
// time), and for kinds other than claude's own relaunch-drift, nothing ever wrote a
// convid line once one became resolvable. fleetGraphResolveConv + fleetgraph.ObserveConv,
// piggybacked on HandleListSessions (the same poll ObserveState already uses), close that
// gap for every Forker kind — not just claude.

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeTmuxListSessionsAlive makes `tmux list-sessions` report tn alive — what
// tmuxx.LiveSessionNames() (HandleListSessions' liveness source) actually shells out to.
// fakeTmuxOnlyFor (session_carried_test.go) only stubs has-session/list-panes/
// capture-pane/load-buffer, which is enough for the handlers that call HasSession
// directly, but HandleListSessions never does — it always goes through the aggregate
// list-sessions call.
func fakeTmuxListSessionsAlive(t *testing.T, tn string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  list-sessions) printf '%s\\n' \"$TMUX_TEST_ALIVE\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_ALIVE", tn)
	isolateAgentConfigDirs(t)
}

// fakeForkerAgent overrides ForkSource on top of a real kind's agent (WireLive, Caps, …
// keep working unchanged), so a test can control exactly what conv id becomes
// "resolvable" without standing up a real rollout/jsonl file.
type fakeForkerAgent struct {
	agents.Agent
	conv string
}

func (f fakeForkerAgent) ForkSource(session.Meta) (string, error) {
	if f.conv == "" {
		return "", errors.New("no conversation yet")
	}
	return f.conv, nil
}

func lineageConvEvents(t *testing.T, name string) []string {
	t.Helper()
	p := filepath.Join(paths.AgentStateDir(), "fleet-graph", "lineage.jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var convs []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) != nil {
			continue
		}
		if m["ev"] == "convid" && m["name"] == name {
			convs = append(convs, m["conv"].(string))
		}
	}
	return convs
}

// TestHandleListSessions_ObservesConvIDForNonClaudeKind is B1: codex (a Forker kind whose
// ForkSource resolves nothing at create time) must get a convid line the first time the
// list handler observes a resolvable conversation, with no relaunch-drift tracker involved.
func TestHandleListSessions_ObservesConvIDForNonClaudeKind(t *testing.T) {
	const name = "slot_conv_codex"
	fakeTmuxListSessionsAlive(t, session.TmuxName(name))
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)
	withFakeAgent(t, session.KindCodex, fakeForkerAgent{Agent: AgentOf(session.KindCodex), conv: "codex-conv-1"})

	// First poll: the conversation just became resolvable.
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest("GET", "/sessions", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := lineageConvEvents(t, name); len(got) != 1 || got[0] != "codex-conv-1" {
		t.Fatalf("convid events after first poll = %v, want exactly [codex-conv-1]", got)
	}

	// Second poll, same conv: must NOT duplicate (the two observers — Console 4s / CP
	// reaper 1m — hit this same handler for the same session).
	rec2 := httptest.NewRecorder()
	HandleListSessions(rec2, httptest.NewRequest("GET", "/sessions", nil))
	if got := lineageConvEvents(t, name); len(got) != 1 {
		t.Fatalf("convid events after second (unchanged) poll = %v, want still exactly 1", got)
	}
}

// TestHandleListSessions_ObservesConvIDChange: a THIRD poll with a genuinely different
// conv (the process relaunched onto a new one) must append a second convid line.
func TestHandleListSessions_ObservesConvIDChange(t *testing.T) {
	const name = "slot_conv_codex_change"
	fakeTmuxListSessionsAlive(t, session.TmuxName(name))
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)
	withFakeAgent(t, session.KindCodex, fakeForkerAgent{Agent: AgentOf(session.KindCodex), conv: "codex-conv-1"})
	HandleListSessions(httptest.NewRecorder(), httptest.NewRequest("GET", "/sessions", nil))

	withFakeAgent(t, session.KindCodex, fakeForkerAgent{Agent: AgentOf(session.KindCodex), conv: "codex-conv-2"})
	HandleListSessions(httptest.NewRecorder(), httptest.NewRequest("GET", "/sessions", nil))

	got := lineageConvEvents(t, name)
	if len(got) != 2 || got[0] != "codex-conv-1" || got[1] != "codex-conv-2" {
		t.Fatalf("convid events = %v, want [codex-conv-1 codex-conv-2]", got)
	}
}

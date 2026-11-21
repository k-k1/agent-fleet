package sessionx

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The kind lists that used to be literal branches now come from the kinds' own capabilities.
// These pin the lists, so a kind that gains or loses a capability changes a test, not a screen.
func TestKindCapabilityDispatchMatchesTheOldLiterals(t *testing.T) {
	var hooked, mirror, full, guarded []string
	for _, k := range RegisteredKinds() {
		if terminalNotifyHasHooks(k) {
			hooked = append(hooked, k)
		}
		if managedMoveIsMirror(session.Meta{Kind: k, Driver: session.DriverManaged}) {
			mirror = append(mirror, k)
		}
		if _, ok := AgentOf(k).(agents.FullTranscriptReader); ok {
			full = append(full, k)
		}
		if terminalSwitchNeedsStopped(k) {
			guarded = append(guarded, k)
		}
	}
	for name, c := range map[string]struct{ got, want []string }{
		"attention hooks": {hooked, []string{session.KindClaude, session.KindCodex, session.KindOpencode}},
		"managed mirror":  {mirror, []string{session.KindCursor}},
		"full transcript": {full, []string{session.KindClaude}},
		"terminal switch": {guarded, []string{session.KindCodex}},
	} {
		g := slices.Clone(c.got)
		slices.Sort(g)
		slices.Sort(c.want)
		if !slices.Equal(g, c.want) {
			t.Errorf("%s: got %v, want %v", name, g, c.want)
		}
	}
	if managedMoveIsMirror(session.Meta{Kind: session.KindCursor}) {
		t.Error("a Terminal cursor session is not a mirror")
	}
}

// claudeFixture writes a claude jsonl for a session of the given kind and returns its meta.
func claudeFixture(t *testing.T, kind string) session.Meta {
	t.Helper()
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	dir := t.TempDir()
	m := session.Meta{Name: "capfx", Dir: dir, Kind: kind}
	session.WriteMeta(m)
	lines := []string{
		`{"type":"user","timestamp":"2026-10-17T10:00:00Z","message":{"role":"user","content":"refactor the parser"}}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-17T10:00:03Z","content":"and keep the old API"}`,
		`{"type":"assistant","timestamp":"2026-10-17T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"Starting."},{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"` + filepath.Join(dir, "parser.go") + `","content":"package p\n"}}]}}`,
	}
	writeFile(t, filepath.Join(home, ".claude", "projects", "p", session.UUID(dir, m.Name)+".jsonl"), strings.Join(lines, "\n")+"\n")
	return m
}

func turnsText(m session.Meta) string {
	b, _ := json.Marshal(exportTurns(m))
	return string(b)
}

// A claude session's export and move snapshot read the whole log through the capability.
func TestClaudeFullTranscriptReachesExportAndMoveSnapshot(t *testing.T) {
	m := claudeFixture(t, session.KindClaude)
	if s := turnsText(m); !strings.Contains(s, "refactor the parser") || !strings.Contains(s, "Starting.") {
		t.Fatalf("export turns = %s", s)
	}
	in := readMoveSource(m)
	if len(in.Turns) == 0 || len(in.Queued) != 1 || in.Queued[0] != "and keep the old API" {
		t.Fatalf("snapshot turns=%d queued=%q", len(in.Turns), in.Queued)
	}
	if len(in.ChangedFiles) == 0 {
		t.Fatalf("snapshot lost the file edits: %+v", in)
	}
}

// An empty or unregistered stored kind gets no capability, as the literal comparison gave it
// none: AgentOf would answer claude's, and the claude log would leak into export and the move.
func TestUnknownKindInheritsNoClaudeCapability(t *testing.T) {
	for _, kind := range []string{"", "nosuchkind"} {
		m := claudeFixture(t, kind)
		if s := turnsText(m); strings.Contains(s, "refactor the parser") {
			t.Errorf("kind %q: export read the claude log: %s", kind, s)
		}
		if in := readMoveSource(m); len(in.Queued) != 0 || len(in.ChangedFiles) != 0 {
			t.Errorf("kind %q: snapshot read the claude log: %+v", kind, in)
		}
		if terminalNotifyHasHooks(kind) {
			t.Errorf("kind %q: terminal notifications would be dropped", kind)
		}
		if managedMoveIsMirror(session.Meta{Kind: kind, Driver: session.DriverManaged}) {
			t.Errorf("kind %q: treated as a mirror", kind)
		}
	}
}

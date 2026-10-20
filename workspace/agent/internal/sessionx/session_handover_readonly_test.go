package sessionx

import (
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// wantReadOnly pins each kind's list exactly: adding a name is a decision to review, not a
// side effect of an edit elsewhere.
var wantReadOnly = map[string][]string{
	session.KindClaude:   {"Read", "Grep", "Glob", "LS", "NotebookRead", "WebFetch", "WebSearch", "ToolSearch"},
	session.KindCodex:    {"view_image"},
	session.KindOpencode: {"read", "glob", "grep", "list", "webfetch", "websearch"},
	session.KindCursor:   {"Read", "Grep"},
	session.KindKiro:     {"fsRead", "read"},
	session.KindAgy:      {"VIEW_FILE", "LIST_DIRECTORY"},
	session.KindCopilot:  {"view", "grep"},
	session.KindLcpp:     {"read", "ls", "glob", "grep"},
	session.KindMuse:     {"read_file"},
}

func TestReadOnlyToolListsAreExact(t *testing.T) {
	if len(readOnlyTools) != len(wantReadOnly) {
		t.Fatalf("kinds in readOnlyTools = %d, want %d", len(readOnlyTools), len(wantReadOnly))
	}
	for kind, want := range wantReadOnly {
		got := slices.Clone(readOnlyTools[kind])
		w := slices.Clone(want)
		slices.Sort(got)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s: read-only tools = %v, want %v", kind, got, w)
		}
	}
}

// Nothing that runs a command, writes, or has side effects the name cannot show may be
// listed: a dropped call is invisible to the target agent.
func TestReadOnlyToolListsNeverHoldMutators(t *testing.T) {
	never := []string{
		"Bash", "bash", "shell", "execute", "exec", "exec_command", "RUN_COMMAND",
		"Write", "Edit", "NotebookEdit", "write", "edit", "write_file", "edit_file", "fsWrite", "fs_write",
		"apply_patch", "patch", "Skill", "TaskStop", "Workflow", "Task", "Agent",
	}
	for kind, names := range readOnlyTools {
		for _, n := range names {
			if slices.Contains(never, n) || strings.HasPrefix(n, "mcp__") {
				t.Errorf("%s lists %q, which can change something", kind, n)
			}
		}
		for _, n := range never {
			if slices.Contains(names, n) {
				t.Errorf("%s lists %q", kind, n)
			}
		}
	}
}

// A listed call is gone outside the last turns, an unlisted one stays as a one-line call
// without its output.
func TestHandoverDropsListedKeepsUnlistedToolCalls(t *testing.T) {
	cases := []struct {
		kind    string
		dropped []string
		kept    []string
	}{
		{session.KindClaude, []string{"Read", "LS", "ToolSearch"}, []string{"Bash", "Skill", "mcp__x__unknown"}},
		{session.KindMuse, []string{"read_file"}, []string{"bash", "mcp__x__send"}},
		{session.KindCodex, []string{"view_image"}, []string{"exec_command", "apply_patch"}},
		{session.KindOpencode, []string{"websearch", "webfetch"}, []string{"shell", "execute", "write"}},
	}
	for _, c := range cases {
		var parts []transcript.Part
		for _, n := range append(slices.Clone(c.dropped), c.kept...) {
			parts = append(parts, transcript.Part{Kind: "tool", Tool: n, Info: "x", Output: "OUT-" + n})
		}
		turns := []transcript.Turn{hoUser("a"), hoAsst(parts...), hoUser("b"), hoUser("c"), hoUser("d")}
		h := hoBuild(t, turns, func(in *HandoverInput) { in.Source.Kind = c.kind })
		for _, n := range c.dropped {
			if _, ok := findTool(h, n, "x"); ok {
				t.Errorf("%s: %s should be dropped outside the window", c.kind, n)
			}
		}
		for _, n := range c.kept {
			p, ok := findTool(h, n, "x")
			if !ok {
				t.Errorf("%s: %s must be kept as a line", c.kind, n)
			} else if p.Output != "" {
				t.Errorf("%s: %s outside the window keeps only its line, got output %q", c.kind, n, p.Output)
			}
		}
	}
}

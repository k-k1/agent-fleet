package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeProc writes a /proc-shaped tree: each entry is pid → (ppid, comm).
func fakeProc(t *testing.T, procs map[int][2]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, pc := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// The command field may hold spaces and parentheses; procParent must count past it.
		stat := fmt.Sprintf("%d (%s) S %s 1 1 0 -1", pid, pc[1]+" (x)", pc[0])
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(pc[1]+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const panePID = 100

// tmux server → pane shell (100) → claude (101), the layout startSessionTmux builds.
func paneTree(extra map[int][2]string) map[int][2]string {
	m := map[int][2]string{
		50:  {"1", "tmux: server"},
		100: {"50", "bash"},
		101: {"100", "claude"},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestClassifyHookChain(t *testing.T) {
	cases := []struct {
		name  string
		procs map[int][2]string
		hook  int
		want  hookOrigin
	}{
		{
			// The pane's own claude runs its hook through a shell. A self-relaunch is an exec in
			// the same PID, so it looks exactly like this and must keep recording the drift.
			name:  "pane claude",
			procs: paneTree(map[int][2]string{200: {"101", "sh"}, 201: {"200", "workspace-agent"}}),
			hook:  201,
			want:  hookOriginPane,
		},
		{
			// `claude -p` from the Bash tool: the nested claude sits under the pane's claude.
			name: "claude -p from the Bash tool",
			procs: paneTree(map[int][2]string{
				300: {"101", "bash"}, 301: {"300", "claude"}, 302: {"301", "sh"}, 303: {"302", "workspace-agent"},
			}),
			hook: 303,
			want: hookOriginNested,
		},
		{
			// A probe in a private `tmux -L` server: its chain ends at init without the pane.
			name: "claude in a private tmux server",
			procs: paneTree(map[int][2]string{
				400: {"1", "tmux: server"}, 401: {"400", "claude"}, 402: {"401", "sh"}, 403: {"402", "workspace-agent"},
			}),
			hook: 403,
			want: hookOriginNested,
		},
		{
			// Native claude.exe nesting a claude whose comm reads differently: still claude.
			name: "claude.exe nested under claude",
			procs: paneTree(map[int][2]string{
				300: {"101", "bash"}, 301: {"300", "claude.exe"}, 302: {"301", "sh"}, 303: {"302", "workspace-agent"},
			}),
			hook: 303,
			want: hookOriginNested,
		},
		{
			// An npm claude (comm node) started from the native one, recognised by its script.
			name: "node claude nested under native claude",
			procs: paneTree(map[int][2]string{
				300: {"101", "bash"}, 301: {"300", "node"}, 302: {"301", "sh"}, 303: {"302", "workspace-agent"},
			}),
			hook: 303,
			want: hookOriginNested,
		},
		{
			// A plain node process (an MCP server, a build) between the hook and claude is not
			// a claude.
			name: "unrelated node under the pane claude",
			procs: paneTree(map[int][2]string{
				300: {"101", "node"}, 302: {"300", "sh"}, 303: {"302", "workspace-agent"},
			}),
			hook: 303,
			want: hookOriginPane,
		},
		{
			// A process vanished mid-walk: not enough evidence to overrule AF_SESSION_NAME.
			name:  "broken chain",
			procs: paneTree(map[int][2]string{201: {"999", "workspace-agent"}}),
			hook:  201,
			want:  hookOriginUnknown,
		},
		{
			// The pane runs the CLI through a wrapper shell: the shell claude spawns the hook
			// through would match it, so no verdict.
			name: "wrapper shell under the pane",
			procs: map[int][2]string{
				50: {"1", "tmux: server"}, 100: {"50", "bash"}, 101: {"100", "bash"},
				102: {"101", "claude"}, 200: {"102", "bash"}, 201: {"200", "workspace-agent"},
			},
			hook: 201,
			want: hookOriginUnknown,
		},
	}
	prevArgv := procArgv
	procArgv = func(pid int) []string {
		if pid == 301 {
			return []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}
		}
		return []string{"node", "/srv/build.js"}
	}
	t.Cleanup(func() { procArgv = prevArgv })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := fakeProc(t, c.procs)
			if got := classifyHookChain(root, c.hook, panePID); got != c.want {
				t.Fatalf("classifyHookChain = %d, want %d", got, c.want)
			}
		})
	}
}

func setHookOrigin(t *testing.T, o hookOrigin) {
	t.Helper()
	prev := hookOriginOf
	hookOriginOf = func(string) hookOrigin { return o }
	t.Cleanup(func() { hookOriginOf = prev })
}

// A nested claude's hook keeps its own id and leaves the ledger alone. Recording it pointed the
// mirror, the status badge and the next --resume at the nested conversation (#3355).
func TestNormalizeHookSIDPassesNestedClaudeThrough(t *testing.T) {
	isolateSlot(t)
	m := session.Meta{Name: "s3355ab", Dir: "/tmp/repo", Kind: session.KindClaude}
	session.WriteMeta(m)
	t.Setenv("AF_SESSION_NAME", m.Name)
	slot := session.UUID(m.Dir, m.Name)
	setHookOrigin(t, hookOriginNested)

	if got := NormalizeHookSID(testLiveSID); got != testLiveSID {
		t.Fatalf("NormalizeHookSID = %q, want the nested claude's own id %q", got, testLiveSID)
	}
	if got := sids.Read(slot); got != "" {
		t.Fatalf("ledger = %q, want no entry for a nested claude", got)
	}
}

// Without a verdict (tmux or /proc unreadable) the drift is still recorded: a hiccup must not
// strand a real relaunch on an id nobody reads.
func TestNormalizeHookSIDRecordsDriftWhenOriginUnknown(t *testing.T) {
	for _, o := range []hookOrigin{hookOriginUnknown, hookOriginPane} {
		t.Run(strconv.Itoa(int(o)), func(t *testing.T) {
			isolateSlot(t)
			m := session.Meta{Name: "s3355cd", Dir: "/tmp/repo", Kind: session.KindClaude}
			session.WriteMeta(m)
			t.Setenv("AF_SESSION_NAME", m.Name)
			slot := session.UUID(m.Dir, m.Name)
			setHookOrigin(t, o)

			if got := NormalizeHookSID(testLiveSID); got != slot {
				t.Fatalf("NormalizeHookSID = %q, want the slot sid %q", got, slot)
			}
			if got := sids.Read(slot); got != testLiveSID {
				t.Fatalf("ledger = %q, want %q", got, testLiveSID)
			}
		})
	}
}

// The launch pane is found by the exit recorder in its start command, across windows, not by
// which pane is active: a split must not make the real claude's hooks look nested.
func TestLaunchPaneIn(t *testing.T) {
	launch := `"export PATH=x; claude --resume abc; __af_ec=$?; ` + session.ExitRecorder("s1") + ` \"$__af_ec\""`
	cases := []struct {
		name string
		out  string
		want int
		ok   bool
	}{
		{"single pane", "100 " + launch + "\n", 100, true},
		{"split off and active", "200 bash\n100 " + launch + "\n300 claude\n", 100, true},
		{"another session's recorder", "100 " + strings.Replace(launch, "'s1'", "'s12'", 1) + "\n", 0, false},
		{"two launch panes", "100 " + launch + "\n101 " + launch + "\n", 0, false},
		{"no launch pane", "200 bash\n", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := launchPaneIn(c.out, "s1")
			if got != c.want || ok != c.ok {
				t.Fatalf("launchPaneIn = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

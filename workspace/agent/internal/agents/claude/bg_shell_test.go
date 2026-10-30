package claude

import "testing"

// TestBackgroundShellBusyIn drives the background-shell signature against fixtured
// proc tables that mirror the real trees observed in the fleet:
//
//	pane-root(login bash) → claude(node) → …
//
// A Monitor / run_in_background shell hangs off claude(node) as a bash in S state,
// which BackgroundBusy (R/D only) and SubagentBusy (transcripts only) both miss.
func TestBackgroundShellBusyIn(t *testing.T) {
	const root = 100 // pane root = login shell tmux launched
	const node = 101 // claude(node), child of the login shell

	// base is the quiescent tree: login shell → claude(node), nothing else.
	base := func() map[int]procInfo {
		return map[int]procInfo{
			root: {PPID: 1, State: 'S', Comm: "bash"},
			node: {PPID: root, State: 'S', Comm: "claude"},
		}
	}

	t.Run("idle claude, no background shell", func(t *testing.T) {
		if backgroundShellBusyIn(root, base()) {
			t.Fatal("quiescent tree must be false")
		}
	})

	t.Run("monitor poll loop (S-state bash under claude)", func(t *testing.T) {
		tab := base()
		// The Monitor's `while …; sleep 30; done` bash, sleeping between polls.
		tab[200] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("S-state background shell under claude must be true")
		}
	})

	t.Run("monitor mid-poll (transient gh child)", func(t *testing.T) {
		tab := base()
		tab[200] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[201] = procInfo{PPID: 200, State: 'S', Comm: "gh"} // network-waiting child
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("background shell with a poll child must be true")
		}
	})

	t.Run("node comm stayed 'node' (isClaudeCLI fallback path)", func(t *testing.T) {
		// procIsClaude falls back to an argv read for pid `node` here; that pid does
		// not exist, so the parent is NOT recognized as claude and the shell is not
		// flagged. Guards that we do not flag a shell under a non-claude parent.
		tab := base()
		tab[node] = procInfo{PPID: root, State: 'S', Comm: "node"}
		tab[200] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		if backgroundShellBusyIn(root, tab) {
			t.Fatal("without a comm=claude parent, a synthetic tree must not flag")
		}
	})

	t.Run("login shell itself is not flagged", func(t *testing.T) {
		// root is a bash, but it is the BFS root (excluded). Its only child is claude.
		if backgroundShellBusyIn(root, base()) {
			t.Fatal("pane root shell must never be the flagged shell")
		}
	})

	t.Run("nested claude launcher is skipped", func(t *testing.T) {
		// bash(300) under claude, but it wraps another claude(301) — a launcher, not
		// work. subtreeHasClaude must veto it.
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'S', Comm: "claude"}
		if backgroundShellBusyIn(root, tab) {
			t.Fatal("a shell wrapping a nested claude must not be flagged")
		}
	})

	t.Run("non-shell worker under claude is left to BackgroundBusy", func(t *testing.T) {
		// A bare compiler process (not a shell) hanging off claude is BackgroundBusy's
		// job (R/D); this detector only owns the shell signature.
		tab := base()
		tab[400] = procInfo{PPID: node, State: 'R', Comm: "cc1"}
		if backgroundShellBusyIn(root, tab) {
			t.Fatal("non-shell worker is out of this detector's scope")
		}
	})

	t.Run("unresolved pane root is false", func(t *testing.T) {
		if backgroundShellBusyIn(0, base()) {
			t.Fatal("root 0 must be false")
		}
	})
}

// withArgv fixtures procArgv for the duration of the test.
func withArgv(t *testing.T, m map[int][]string) {
	t.Helper()
	old := procArgv
	procArgv = func(pid int) []string { return m[pid] }
	t.Cleanup(func() { procArgv = old })
}

// TestWorkspaceAgentWrapperIsWork: af-aws-exec & co. exec workspace-agent, which must
// count as work under a background shell, while the MCP helper and nested claude do not.
func TestWorkspaceAgentWrapperIsWork(t *testing.T) {
	const root, node = 100, 101
	base := func() map[int]procInfo {
		return map[int]procInfo{
			root: {PPID: 1, State: 'S', Comm: "bash"},
			node: {PPID: root, State: 'S', Comm: "claude"},
		}
	}
	wa := []string{"/usr/local/bin/workspace-agent", "aws-exec", "--profile", "x", "--", "./resume.sh"}

	t.Run("claude -> bash -> workspace-agent aws-exec -> bash -> aws is busy", func(t *testing.T) {
		withArgv(t, map[int][]string{
			300: {"bash", "-c", "af-aws-exec --profile x -- ./resume.sh"},
			301: wa,
			302: {"bash", "./resume.sh"},
			303: {"aws", "rds", "wait"},
		})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'S', Comm: "workspace-agent"}
		tab[302] = procInfo{PPID: 301, State: 'S', Comm: "bash"}
		tab[303] = procInfo{PPID: 302, State: 'S', Comm: "aws"}
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("background shell running workspace-agent aws-exec must be busy")
		}
	})

	t.Run("claude -> bash -> workspace-agent aws-exec -> aws (no inner shell) is busy", func(t *testing.T) {
		withArgv(t, map[int][]string{301: wa, 303: {"aws", "rds", "wait"}})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'S', Comm: "workspace-agent"}
		tab[303] = procInfo{PPID: 301, State: 'S', Comm: "aws"}
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("background shell running workspace-agent aws-exec must be busy")
		}
	})

	t.Run("claude -> bash -> claude (nested launch) is still skipped", func(t *testing.T) {
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'S', Comm: "node"}
		withArgv(t, map[int][]string{301: {"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}})
		if backgroundShellBusyIn(root, tab) {
			t.Fatal("a shell wrapping a nested node-claude must not be flagged")
		}
	})

	t.Run("shell-snapshot subshell is not mistaken for claude", func(t *testing.T) {
		// A forked subshell of claude's Bash wrapper has the snapshot path under the
		// claude config dir in its cmdline; a substring match would veto the shell.
		withArgv(t, map[int][]string{
			301: {"bash", "-c", "source /var/lib/af/claude/shell-snapshots/snapshot-bash-1.sh && eval 'sleep 300'"},
		})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'S', Comm: "bash"}
		tab[302] = procInfo{PPID: 301, State: 'S', Comm: "sleep"}
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("snapshot-path cmdline must not veto a background shell")
		}
	})

	t.Run("MCP helper in R under claude does not flag BackgroundBusy", func(t *testing.T) {
		withArgv(t, map[int][]string{300: {"/usr/local/bin/workspace-agent", "mcp-stdio"}})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'R', Comm: "workspace-agent"}
		if backgroundBusyIn(root, tab) {
			t.Fatal("the MCP helper must not count as background work")
		}
	})

	t.Run("workspace-agent aws-exec in R flags BackgroundBusy", func(t *testing.T) {
		withArgv(t, map[int][]string{300: wa})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'R', Comm: "workspace-agent"}
		if !backgroundBusyIn(root, tab) {
			t.Fatal("a running aws-exec is work")
		}
	})

	t.Run("mcp-stdio not under claude is not exempt", func(t *testing.T) {
		withArgv(t, map[int][]string{301: {"/usr/local/bin/workspace-agent", "mcp-stdio"}})
		tab := base()
		tab[300] = procInfo{PPID: node, State: 'S', Comm: "bash"}
		tab[301] = procInfo{PPID: 300, State: 'R', Comm: "workspace-agent"}
		if !backgroundBusyIn(root, tab) {
			t.Fatal("only the direct child of claude is the helper")
		}
	})
}

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
			root: {ppid: 1, state: 'S', comm: "bash"},
			node: {ppid: root, state: 'S', comm: "claude"},
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
		tab[200] = procInfo{ppid: node, state: 'S', comm: "bash"}
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("S-state background shell under claude must be true")
		}
	})

	t.Run("monitor mid-poll (transient gh child)", func(t *testing.T) {
		tab := base()
		tab[200] = procInfo{ppid: node, state: 'S', comm: "bash"}
		tab[201] = procInfo{ppid: 200, state: 'S', comm: "gh"} // network-waiting child
		if !backgroundShellBusyIn(root, tab) {
			t.Fatal("background shell with a poll child must be true")
		}
	})

	t.Run("node comm stayed 'node' (isClaudeProc fallback path)", func(t *testing.T) {
		// procIsClaude falls back to a /proc read for pid `node` here; that pid does
		// not exist, so the parent is NOT recognized as claude and the shell is not
		// flagged. Guards that we do not flag a shell under a non-claude parent.
		tab := base()
		tab[node] = procInfo{ppid: root, state: 'S', comm: "node"}
		tab[200] = procInfo{ppid: node, state: 'S', comm: "bash"}
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
		tab[300] = procInfo{ppid: node, state: 'S', comm: "bash"}
		tab[301] = procInfo{ppid: 300, state: 'S', comm: "claude"}
		if backgroundShellBusyIn(root, tab) {
			t.Fatal("a shell wrapping a nested claude must not be flagged")
		}
	})

	t.Run("non-shell worker under claude is left to BackgroundBusy", func(t *testing.T) {
		// A bare compiler process (not a shell) hanging off claude is BackgroundBusy's
		// job (R/D); this detector only owns the shell signature.
		tab := base()
		tab[400] = procInfo{ppid: node, state: 'R', comm: "cc1"}
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

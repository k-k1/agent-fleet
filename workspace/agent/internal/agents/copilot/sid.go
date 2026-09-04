package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// copilot is an "imposed id" kind — we pass the UUID we allocated via `--session-id` and
// from then on read both the transcript and the state assuming it is the one in use. Once the
// CLI stops using that id (dropping the flag on a self-restart, or starting a new session
// inside the TUI) the mirror silently freezes empty. That breakage really happened with
// claude, and copilot has no status hook, so there is no way to ask it what it calls itself
// (internal/agents/imposedsid.go). Disk is the only clue.
//
// resolveSid re-collects this cwd's conversations and replaces the ledger only when not a
// single session-state exists for the imposed id. It stays out of the read hot path
// (SessionID, called by Transcript/LiveState) — it walks ListMetas, so it rides only on
// launch and on the liveness poll.
func resolveSid(m session.Meta) string {
	return agents.ResolveImposedSID(sids, m, cliSessions)
}

// cliSessions enumerates copilot's own sessions launched in dir.
//
// Ownership and creation time come from session-state/<sid>/workspace.yaml. Measured on
// v1.0.73 it carries `id:` / `cwd:` / `created_at:` (RFC3339, milliseconds, Z), in the same
// shape across all 14 sessions left in this environment. It is also the file we touch when
// preparing fork material (retargetFile in forkat.go), so the assumption about its shape is
// shared with that code.
//
// A naive line scan is enough: only the three top-level keys are needed, and nothing here
// justifies a YAML dependency. Values can themselves contain a colon (name: …), so only the
// first colon splits a line.
func cliSessions(dir string) []agents.CLISession {
	root := filepath.Join(Home(), "session-state")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	want := filepath.Clean(dir)
	var out []agents.CLISession
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id, cwd, created := readWorkspaceYAML(filepath.Join(root, e.Name(), "workspace.yaml"))
		if id == "" {
			id = e.Name() // no workspace.yaml yet, or a broken one: the dir name is the id
		}
		if filepath.Clean(cwd) != want {
			continue
		}
		out = append(out, agents.CLISession{ID: id, Created: created})
	}
	return out
}

// readWorkspaceYAML pulls id / cwd / created_at out of a copilot workspace.yaml.
// If an unknown version changes the shape, cwd comes out empty and that session drops out of
// the candidates: failing to pick one up is preferred over adopting the wrong one (staying
// frozen only keeps the status quo, whereas mirroring someone else's conversation does harm).
func readWorkspaceYAML(path string) (id, cwd string, created time.Time) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", time.Time{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // nested values are ignored (only the three top-level keys are needed)
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "id":
			id = val
		case "cwd":
			cwd = val
		case "created_at":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				created = t
			}
		}
	}
	return id, cwd, created
}

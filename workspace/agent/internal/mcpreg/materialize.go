package mcpreg

// Session materialize (docs/log/48 §8 / P3): write the effective registry into each
// CLI's OWN global config file, so an interactive session picks the servers up the
// way it would pick up a hand-written entry.
//
// Why not launch flags. claude takes --mcp-config, but only together with
// --strict-mcp-config, which also shuts out the user's project .mcp.json and their
// own ~/.claude.json entries. codex has no per-exec config file at all. Writing the
// native config is the only shape that ADDS to what the user already has instead of
// replacing it.
//
// The bargain that makes writing someone else's config file safe:
//
//   - af remembers, per kind, exactly which server NAMES it wrote
//     (~/.config/agent-fleet/mcp-managed.json). A name that leaves the registry is
//     removed only if it is on that list — anything the user added by hand, or with
//     `claude mcp add` / `codex mcp add`, is never touched.
//   - every write is read → merge → temp file → rename, at 0600.
//   - nothing is written when nothing changed. Both files are ALSO written by their
//     CLI (claude rewrites .claude.json constantly), so a no-op launch must not
//     rewrite them and must not reformat them.
//
// Secrets become plaintext here. That is unavoidable and accepted by docs/log/48 §5.1:
// the CLI has to be able to read them. The mitigation is the file mode and the
// location (home only, never a repo).
//
// P3 implemented claude and codex; P5 added opencode, copilot, cursor, kiro and agy —
// every kind that runs an agent CLI. A kind with no CLI behind it (shell, ssm) returns
// a skipped result rather than an error: "nothing to write here" is not a failure.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// MaterializedKinds are the agent kinds whose native MCP config af writes — since P5,
// every kind that runs an agent CLI. Ordered (session.go's declaration order) so
// MaterializeAll's results read deterministically.
var MaterializedKinds = []string{
	session.KindClaude, session.KindOpencode, session.KindCodex,
	session.KindCursor, session.KindKiro, session.KindAgy, session.KindCopilot,
}

// MaterializeResult is one kind's outcome, shaped for a log line and for a future
// Console surface (docs/log/48 §11.3).
type MaterializeResult struct {
	Kind string `json:"kind"`
	// Written are the server names af now owns in that CLI's config.
	Written []string `json:"written,omitempty"`
	// Removed are names af previously wrote and has now taken back out.
	Removed []string `json:"removed,omitempty"`
	// Changed reports whether the file was actually rewritten.
	Changed bool `json:"changed,omitempty"`
	// Skipped marks a kind with no materializer yet.
	Skipped bool   `json:"skipped,omitempty"`
	Err     string `json:"error,omitempty"`
}

// writer is one kind's native-config editor. It receives the definitions to install
// and the names af wrote last time, and reports what it ended up owning. A writer
// must not touch any name outside (defs ∪ prev).
type writer func(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error)

func writerFor(kind string) writer {
	switch kind {
	case session.KindClaude:
		return materializeClaude
	case session.KindCodex:
		return materializeCodex
	case session.KindOpencode:
		return materializeOpencode
	case session.KindCursor:
		return materializeCursor
	case session.KindKiro:
		return materializeKiro
	case session.KindAgy:
		return materializeAgy
	case session.KindCopilot:
		return materializeCopilot
	}
	// Kinds that run no agent CLI (shell, ssm) have no config to write. Skipped, not
	// an error — see MaterializeResult.Skipped.
	return nil
}

// materializeMu serializes materialize passes. Every pass is read → merge → rename over
// a shared config file AND a read-modify-write of the ownership ledger, and there are
// several concurrent callers (registry CRUD over HTTP, each session launch, and since P4
// a 5-minute tenant poll). Two interleaved passes would lose one side's write; losing it
// on mcp-managed.json is the worse half — af would forget it owns a name, and that name
// becomes an orphan in the user's config that nothing is allowed to remove, which is the
// exact failure the ledger exists to prevent (docs/log/48 §8.2).
//
// A global lock rather than one per kind: passes are short, run at most a few times a
// minute, and MaterializeAll's whole point is that the kinds share the same ledger file.
var materializeMu sync.Mutex

// Materialize writes the registry into one kind's native config.
func Materialize(kind string) MaterializeResult {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	return materializeLocked(kind)
}

func materializeLocked(kind string) MaterializeResult {
	res := MaterializeResult{Kind: kind}
	w := writerFor(kind)
	if w == nil {
		res.Skipped = true
		return res
	}
	defs, err := ForSession(kind)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	managed, err := loadManagedNames()
	if err != nil {
		res.Err = err.Error()
		return res
	}
	written, removed, changed, err := w(defs, managed.Kinds[kind])
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.Written, res.Removed, res.Changed = written, removed, changed
	// Record the new ownership only after the config write succeeded. Losing this
	// file would strand af's rows in the CLI config (they would never be cleaned up),
	// so it is the second half of the write and never the first.
	if err := managed.set(kind, written); err != nil {
		res.Err = err.Error()
	}
	return res
}

// MaterializeAll writes every implemented kind. Callers log the results; one kind
// failing must not stop the others (a broken ~/.codex/config.toml should not cost a
// claude session its servers).
func MaterializeAll() []MaterializeResult {
	// One lock for the whole sweep, not per kind: the kinds share the ownership ledger, so
	// releasing between them would reopen the lost-update window this lock closes.
	materializeMu.Lock()
	defer materializeMu.Unlock()
	out := make([]MaterializeResult, 0, len(MaterializedKinds))
	for _, k := range MaterializedKinds {
		out = append(out, materializeLocked(k))
	}
	return out
}

// --- af's ownership record ---------------------------------------------------

// managedNames is the per-kind list of server names af wrote into that CLI's config.
// It is the ONLY thing that authorizes a deletion from a user's config file
// (docs/log/48 §8.2).
type managedNames struct {
	Kinds map[string][]string `json:"kinds"`
}

func managedNamesPath() string {
	return filepath.Join(paths.AgentConfigDir(), "mcp-managed.json")
}

func loadManagedNames() (*managedNames, error) {
	m := &managedNames{Kinds: map[string][]string{}}
	b, err := os.ReadFile(managedNamesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		// A corrupt record would otherwise mean "af owns nothing", silently orphaning
		// every row it wrote. Refusing is the safer half: the config keeps working and
		// the failure is visible instead of turning into a slow leak.
		return nil, fmt.Errorf("%s is unreadable: %w", managedNamesPath(), err)
	}
	if m.Kinds == nil {
		m.Kinds = map[string][]string{}
	}
	return m, nil
}

func (m *managedNames) set(kind string, names []string) error {
	if len(names) == 0 {
		delete(m.Kinds, kind)
	} else {
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		m.Kinds[kind] = sorted
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(managedNamesPath()), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(managedNamesPath(), append(b, '\n'), 0o600)
}

// --- shared helpers ----------------------------------------------------------

// defNames lists the server names in defs, in the order they will be written.
func defNames(defs []ServerDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// goneFrom returns the previously-owned names that are no longer in the registry —
// the exact set a writer is allowed to delete.
func goneFrom(prev []string, defs []ServerDef) []string {
	keep := map[string]bool{}
	for _, d := range defs {
		keep[d.Name] = true
	}
	var out []string
	for _, n := range prev {
		if !keep[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

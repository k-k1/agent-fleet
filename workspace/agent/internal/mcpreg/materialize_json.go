package mcpreg

// Every CLI except codex keeps its MCP servers in a JSON document holding ONE map of
// server entries — claude, opencode, copilot, kiro, cursor and agy differ only in the
// file, the member that holds the map, and how a single entry is spelled. That is the
// whole of this file: the shared edit, plus the shape each kind fills in
// (materialize_<kind>.go). codex is the exception (TOML, line-edited —
// materialize_codex.go).
//
// The rules materializeClaude established in P3 apply to all of them, because the file
// is always the USER's file and af is a guest in it:
//
//   - a file that will not parse is REFUSED, never overwritten. Rewriting it would
//     cost whatever af cannot read — claude's onboarding and trust flags being the
//     expensive case (claude/settings.go), an opencode.jsonc's comments the common one.
//   - only names on af's ledger are removed (materialize.go), so a hand-written or
//     `<cli> mcp add`-ed server is never touched.
//   - a name af is about to write replaces whatever sits there, so the two cannot
//     disagree about one key.
//   - nothing is written when the DECODED structure is unchanged. Several of these
//     files are also written by their own CLI, and a no-op session launch must neither
//     churn them nor widen the window where af's rename drops a concurrent CLI write.
//
// Secret env and header VALUES land here in plaintext at 0600, which is exactly what
// docs/48 §5.1 promises: the CLI has to be able to read them, and the mitigation is
// the mode and the location (home only, never a repo).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// jsonConfig is one CLI's native MCP config, described declaratively.
type jsonConfig struct {
	// path resolves the config file. A func, not a string, because HOME moves under
	// tests and the drift harness.
	path func() string
	// key is the top-level member holding the server map — "mcpServers" everywhere
	// except opencode, which calls it "mcp".
	key string
	// entries spells the definitions the way this CLI reads them.
	entries func([]ServerDef) map[string]any
	// seed is written into a file af creates from scratch (opencode's "$schema"). It is
	// never applied to a file that already exists, and never on its own: a kind with no
	// servers to write leaves a missing file missing.
	seed map[string]any
}

func (c jsonConfig) materialize(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	path := c.path()
	root := map[string]any{}
	b, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if err := json.Unmarshal(b, &root); err != nil {
			return nil, nil, false, fmt.Errorf("%s is not plain JSON, leaving it alone: %w", path, err)
		}
	case !os.IsNotExist(rerr):
		return nil, nil, false, rerr
	default:
		for k, v := range c.seed {
			root[k] = v
		}
	}

	servers, _ := root[c.key].(map[string]any)
	hadKey := servers != nil
	if servers == nil {
		servers = map[string]any{}
	}

	keep := map[string]bool{}
	for _, d := range defs {
		keep[d.Name] = true
	}
	for _, name := range goneFrom(prev, defs) {
		if _, ok := servers[name]; ok {
			delete(servers, name)
			removed = append(removed, name)
			changed = true
		}
	}
	// af's own name rotates every boot, so last boot's entry has to go even when the
	// ownership ledger no longer names it — otherwise every boot leaves another live
	// server behind (see StaleAFServerName).
	for name := range servers {
		if StaleAFServerName(name, keep) {
			delete(servers, name)
			removed = append(removed, name)
			changed = true
		}
	}
	// Round-trip through JSON before comparing: the file's decoded entry is
	// map[string]any with []any args, while the serializers build []any too but with
	// map[string]string for env — DeepEqual would report a difference every launch.
	for name, entry := range c.entries(defs) {
		norm, err := jsonRoundTrip(entry)
		if err != nil {
			return nil, nil, false, err
		}
		if !reflect.DeepEqual(servers[name], norm) {
			servers[name] = norm
			changed = true
		}
	}
	written = defNames(defs)

	if !changed {
		return written, removed, false, nil
	}
	if len(servers) == 0 {
		// Leave the file exactly as a CLI that never had MCP servers would: no key at
		// all, rather than an empty object af introduced.
		if hadKey {
			delete(root, c.key)
		}
	} else {
		root[c.key] = servers
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, false, err
	}
	if err := writeFileAtomic(path, append(out, '\n'), 0o600); err != nil {
		return nil, nil, false, err
	}
	return written, removed, true, nil
}

// jsonRoundTrip renders v the way it will look once it has been through the file, so
// a comparison against the decoded file answers "is this already installed" rather
// than "are these two Go values the same type".
func jsonRoundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

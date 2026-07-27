package mcpreg

// claude's native MCP config: the `mcpServers` object in
// $CLAUDE_CONFIG_DIR/.claude.json (the user scope — `claude mcp add -s user` writes
// exactly here).
//
// Measured on claude 2.1.220 in an isolated CLAUDE_CONFIG_DIR:
//
//	"mcpServers": {
//	  "srv": {"type":"stdio","command":"/bin/echo","args":["a"],"env":{"K":"v"}},
//	  "rem": {"type":"http","url":"https://…","headers":{"Authorization":"Bearer …"}}
//	}
//
// which is the shape ClaudeServers already builds for the assistant chat — one
// serializer feeds both consumers.
//
// .claude.json is claude's own live state file (onboarding flags, per-project trust,
// history). Two rules follow from that:
//
//   - unparseable file → refuse. Overwriting it would cost the user their trust
//     dialogs and re-trigger the setup wizard (see claude/settings.go).
//   - write ONLY on a real change. claude rewrites this file continuously, so a
//     no-op launch that re-serialized it would both churn a large file and widen the
//     window where our rename could drop a concurrent claude write.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func claudeJSONPath() string {
	return filepath.Join(paths.ClaudeConfigDir(), ".claude.json")
}

func materializeClaude(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	path := claudeJSONPath()
	root := map[string]any{}
	b, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if err := json.Unmarshal(b, &root); err != nil {
			return nil, nil, false, err
		}
	case !os.IsNotExist(rerr):
		return nil, nil, false, rerr
	}

	servers, _ := root["mcpServers"].(map[string]any)
	hadKey := servers != nil
	if servers == nil {
		servers = map[string]any{}
	}

	for _, name := range goneFrom(prev, defs) {
		if _, ok := servers[name]; ok {
			delete(servers, name)
			removed = append(removed, name)
			changed = true
		}
	}
	// Round-trip through JSON before comparing: the file's decoded entry is
	// map[string]any with []any args, while ClaudeServers builds []any too but with
	// map[string]string for env — DeepEqual would report a difference every launch.
	for name, entry := range ClaudeServers(defs) {
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
		// Leave the file exactly as a claude that never had MCP servers would: no key
		// at all, rather than an empty object we introduced.
		if hadKey {
			delete(root, "mcpServers")
		}
	} else {
		root["mcpServers"] = servers
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

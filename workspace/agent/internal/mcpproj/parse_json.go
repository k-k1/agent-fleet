package mcpproj

// Parsing for every kind whose project-scope MCP config is "a JSON document holding
// one map of entries" — the mirror image of mcpreg's jsonConfig writer
// (materialize_json.go), reading instead of writing. It consults
// mcpreg.JSONEntrySpellings so a key name is never re-typed by hand a second time
// (docs/56 §4.2).

import (
	"encoding/json"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// parseJSONFile reads path as plain JSON and returns the server map named by
// sp.ServersKey, in the order they appear (encoding/json's map decode does not
// preserve order, so callers that need one — none do in P0 — would have to switch
// to a token-level decode; docs/56 §6 already earmarks that for P1's span tracking).
func parseJSONServers(raw map[string]any, sp mcpreg.JSONEntrySpelling) (map[string]Server, error) {
	rawServers, _ := raw[sp.ServersKey].(map[string]any)
	out := map[string]Server{}
	for name, v := range rawServers {
		entry, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %q under %q is not an object", name, sp.ServersKey)
		}
		s, err := parseJSONEntry(name, entry, sp)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
}

// parseJSONEntry normalizes one raw JSON object into a Server per sp's spelling.
func parseJSONEntry(name string, raw map[string]any, sp mcpreg.JSONEntrySpelling) (Server, error) {
	s := Server{Name: name}
	used := map[string]bool{}
	mark := func(k string) { used[k] = true }

	_, hasURL := raw[sp.URLKey]
	var command string
	var args []string
	if sp.ArgsFolded {
		if arr, ok := raw[sp.CommandKey].([]any); ok && len(arr) > 0 {
			all := stringSlice(arr)
			if len(all) > 0 {
				command, args = all[0], all[1:]
			}
		}
	} else {
		command, _ = raw[sp.CommandKey].(string)
		if arr, ok := raw["args"]; ok {
			args = stringSlice(arr)
			mark("args")
		}
	}
	_, hasCommand := raw[sp.CommandKey]
	if hasCommand {
		mark(sp.CommandKey)
	}

	isHTTP := false
	switch {
	case sp.TypeKey != "":
		t, _ := raw[sp.TypeKey].(string)
		mark(sp.TypeKey)
		switch t {
		case sp.TypeHTTP:
			isHTTP = true
		case sp.TypeStdio:
			isHTTP = false
		default:
			// Unrecognized/missing discriminator: fall back to structural sniffing
			// like the discriminator-free kinds, rather than guessing wrong.
			isHTTP = hasURL && !hasCommand
		}
	default:
		isHTTP = hasURL && !hasCommand
	}

	if isHTTP {
		s.Transport = TransportHTTP
		s.URL, _ = raw[sp.URLKey].(string)
		mark(sp.URLKey)
		if h, ok := raw[sp.HeadersKey]; ok {
			s.Headers = stringMap(h)
			mark(sp.HeadersKey)
		}
	} else {
		s.Transport = TransportStdio
		s.Command = command
		if len(args) > 0 {
			s.Args = args
		}
		if e, ok := raw[sp.EnvKey]; ok {
			s.Env = stringMap(e)
			mark(sp.EnvKey)
		}
	}

	if extra := extraKeys(raw, used); len(extra) > 0 {
		s.Extra = extra
	}
	return s, nil
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil // a non-string element means this isn't the shape we expect; drop rather than guess
		}
		out = append(out, s)
	}
	return out
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extraKeys(raw map[string]any, used map[string]bool) map[string]any {
	var out map[string]any
	for k, v := range raw {
		if used[k] {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return out
}

// decodeJSONObject parses b as a JSON object (not array/scalar) at the top level —
// every kind's project MCP file is an object, so anything else is treated the same
// as a parse failure (docs/57 憲章3, "読めないファイルは触らない").
func decodeJSONObject(b []byte) (map[string]any, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top level is not a JSON object")
	}
	return obj, nil
}

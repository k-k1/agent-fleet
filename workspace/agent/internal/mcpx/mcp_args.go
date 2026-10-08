package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// mcpOwnDecoderTools decode p.Args themselves (or forward it verbatim), so their argument
// TYPES are not the union struct's business (it is still filled, best effort, for the fields
// their handlers do read): running the union decode over them would refuse a
// legitimate call whose field shares a name with a union field of another type.
var mcpOwnDecoderTools = map[string]bool{
	"get_image_studio": true, "set_image_draft": true, "run_image_trial": true, "add_image_knowledge": true,
	mcpToolBranchName: true, mcpToolSearchSessions: true,
	mcpToolMemoryIndex: true, mcpToolMemorySearch: true, mcpToolMemoryRead: true,
	mcpToolMemorySave: true, mcpToolMemoryForget: true,
	// Forwarded to the Control Plane as written, which owns their shape.
	"add_memo": true, "update_memo": true, "flush_memos": true,
	"create_schedule": true, "update_schedule": true,
}

// mcpSchemaProps returns the property names of a tool's advertised inputSchema, and false when
// the schema does not enumerate properties (then there is nothing to hold the caller to).
func mcpSchemaProps(tool map[string]any) (map[string]bool, bool) {
	schema, ok := tool["inputSchema"].(map[string]any)
	if !ok {
		return nil, false
	}
	out := map[string]bool{}
	switch props := schema["properties"].(type) {
	case map[string]any:
		for k := range props {
			out[k] = true
		}
	case map[string]map[string]any:
		for k := range props {
			out[k] = true
		}
	default:
		return nil, false
	}
	return out, true
}

// advertisedProps maps tool name to the property set it was advertised with.
func advertisedProps(tools []map[string]any) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(tools))
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if props, ok := mcpSchemaProps(tool); ok && name != "" {
			out[name] = props
		}
	}
	return out
}

// mcpToolProps is the property set the caller was told for this tool: the one the last
// tools/list returned, or, before any tools/list, the one it would return now. ok=false when
// the tool has no enumerated schema (an unadvertised name is refused elsewhere).
func mcpToolProps(name string) (map[string]bool, bool) {
	mcpAdvertised.mu.Lock()
	remembered, listed := mcpAdvertised.props, mcpAdvertised.names != nil
	mcpAdvertised.mu.Unlock()
	if !listed {
		remembered = advertisedProps(mcpStdioToolList())
	}
	props, ok := remembered[name]
	return props, ok
}

// mcpCheckCallArgs holds a tools/call to what its tool advertised and decodes it into a.
// A non-empty result is the tool error text; nothing has been done by then. Without it a
// misspelt key (create_session with `prompt` for `initial_prompt`) decodes to a zero value and
// the call succeeds with no task.
func mcpCheckCallArgs(tool string, args json.RawMessage, a *mcpCallArgs) string {
	if t := strings.TrimSpace(string(args)); t == "" || t == "null" {
		return ""
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(args, &keys); err != nil {
		return fmt.Sprintf("invalid arguments for %s: arguments must be a JSON object", tool)
	}
	if props, ok := mcpToolProps(tool); ok {
		var bad []string
		for k := range keys {
			if !props[k] {
				bad = append(bad, k)
			}
		}
		sort.Strings(bad)
		if len(bad) > 0 {
			msgs := make([]string, 0, len(bad))
			for _, k := range bad {
				m := fmt.Sprintf("unknown argument %q for %s", k, tool)
				if guess := mcpClosestProp(k, props); guess != "" {
					m += fmt.Sprintf(" (did you mean %q?)", guess)
				}
				msgs = append(msgs, m)
			}
			return strings.Join(msgs, "; ")
		}
	}
	err := json.Unmarshal(args, a)
	if err != nil && !mcpOwnDecoderTools[tool] {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return fmt.Sprintf("invalid argument %q for %s: got a JSON %s, want %s", te.Field, tool, te.Value, te.Type)
		}
		return fmt.Sprintf("invalid arguments for %s: %v", tool, err)
	}
	return ""
}

// mcpClosestProp suggests the advertised property a misspelt key most likely meant: one that
// contains it or is contained by it (`prompt` -> `initial_prompt`), else the nearest by edit
// distance when that is small. "" when nothing is an obvious match.
func mcpClosestProp(key string, props map[string]bool) string {
	names := make([]string, 0, len(props))
	for p := range props {
		names = append(names, p)
	}
	sort.Strings(names)
	lk := strings.ToLower(key)
	best, bestScore := "", 1<<30
	for _, p := range names {
		lp := strings.ToLower(p)
		score := -1
		if len(lk) >= 3 && strings.Contains(lp, lk) || len(lp) >= 3 && strings.Contains(lk, lp) {
			score = abs(len(lp) - len(lk))
		} else if d := editDistance(lk, lp); d <= 2 && d*3 <= len(lk) {
			score = d
		}
		if score >= 0 && score < bestScore {
			best, bestScore = p, score
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

const noTaskWarning = "This child was started with no task (initial_prompt was empty), so it will sit idle " +
	"until it is sent one. Send it its instructions now with send_to_peer_session."

// withNoTaskWarning adds noTaskWarning to a create_session result: as a "warning" member when the
// Agent's answer is a JSON object, else appended as a line.
func withNoTaskWarning(out string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &m) == nil && m != nil {
		w, _ := json.Marshal(noTaskWarning)
		m["warning"] = w
		if b, err := json.Marshal(m); err == nil {
			return string(b)
		}
	}
	return out + "\n" + noTaskWarning
}

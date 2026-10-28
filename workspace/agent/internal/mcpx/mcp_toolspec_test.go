package mcpx

import "testing"

// Every spec is one tool: a unique name, the same name inside each schema it advertises, a
// surface to be advertised on and a handler. The dispatch finds a spec by name, so a second
// spec with the same name would be unreachable; a schema spelling another name would be
// advertised under one name and dispatched under another.
func TestToolSpecsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, family := range toolFamilies() {
		for _, spec := range family {
			if spec.name == "" || seen[spec.name] {
				t.Errorf("tool name %q is empty or declared twice", spec.name)
			}
			seen[spec.name] = true
			if spec.handle == nil {
				t.Errorf("%s: no handler", spec.name)
			}
			if spec.operator == nil && spec.session == nil {
				t.Errorf("%s: advertised on no surface", spec.name)
			}
			if spec.operator == nil && spec.operatorWrite {
				t.Errorf("%s: operatorWrite without an operator schema", spec.name)
			}
			for surface, schema := range map[string]func() map[string]any{"operator": spec.operator, "session": spec.session} {
				if schema == nil {
					continue
				}
				if got := schema()["name"]; got != spec.name {
					t.Errorf("%s: %s schema is named %v", spec.name, surface, got)
				}
			}
		}
	}
}

// The family lists that name tools of other families must name ones with a session schema:
// sessionToolsNamed skips anything else, which would silently shrink a session's surface.
func TestSessionListsNameSessionTools(t *testing.T) {
	for list, names := range map[string][]string{"fleet observe": fleetObserveSessionTools, "fleet spawn": fleetSpawnSessionTools} {
		for _, name := range names {
			if spec, ok := toolNamed(name); !ok || spec.session == nil {
				t.Errorf("%s list: %s has no session schema", list, name)
			}
		}
		if got := len(sessionToolsNamed(names...)); got != len(names) {
			t.Errorf("%s list: %d of %d tools resolved", list, got, len(names))
		}
	}
}

package mcpsrv

import "testing"

// toolsFor lists the member tools before the admin tools and mcpToolCall runs the
// first name match, so an admin tool that reuses a member tool's name is advertised
// twice and can never be called.
func TestToolNamesAreUniqueAcrossMemberAndAdmin(t *testing.T) {
	seen := map[string]string{}
	check := func(set string, tools []mcpTool) {
		for _, tool := range tools {
			if prev, dup := seen[tool.name]; dup {
				t.Errorf("tool %q is defined in both %s and %s", tool.name, prev, set)
				continue
			}
			seen[tool.name] = set
		}
	}
	check("memberTools", memberTools())
	check("adminTools", adminTools())
}

func TestAdminStopUserSessionIsTheAdminTool(t *testing.T) {
	for _, tool := range adminTools() {
		if tool.name == "stop_user_session" {
			if !tool.admin || tool.runAdmin == nil {
				t.Fatalf("stop_user_session must be an admin tool with runAdmin set")
			}
			return
		}
	}
	t.Fatal("adminTools has no stop_user_session")
}

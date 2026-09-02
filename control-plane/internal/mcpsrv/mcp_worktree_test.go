package mcpsrv

import "testing"

func TestCreateSessionToolAdvertisesWorktreeOptions(t *testing.T) {
	for _, tool := range memberTools() {
		if tool.name != "create_session" {
			continue
		}
		props, ok := tool.schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("create_session properties = %#v", tool.schema["properties"])
		}
		for _, name := range []string{"worktree", "branch", "new_branch"} {
			if _, ok := props[name]; !ok {
				t.Fatalf("create_session schema is missing %q", name)
			}
		}
		if typ, _ := props["worktree"].(map[string]any)["type"].(string); typ != "boolean" {
			t.Fatalf("worktree type = %q, want boolean", typ)
		}
		return
	}
	t.Fatal("create_session tool not found")
}

func TestCreateSessionToolHasLiveModelCatalog(t *testing.T) {
	for _, tool := range memberTools() {
		if tool.name == "list_models" {
			return
		}
	}
	t.Fatal("list_models tool not found")
}

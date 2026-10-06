package claude

import (
	"encoding/json"
	"reflect"
	"testing"
)

func commandHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

func TestRTKTogglePreservesUserHooks(t *testing.T) {
	user := map[string]any{"matcher": "Bash", "extra": "keep", "hooks": []any{commandHook("guard-production"), map[string]any{"type": "prompt", "prompt": "rtk hook claude"}}}
	mixed := map[string]any{"matcher": "Bash", "extra": "mixed", "hooks": []any{commandHook(rtkHookCommand), commandHook("log-command")}}
	unrelated := map[string]any{"matcher": "AskUserQuestion", "hooks": []any{commandHook("notify-question")}}
	m := map[string]any{"other": true, "hooks": map[string]any{"PreToolUse": []any{user, mixed, unrelated}, "Stop": []any{"keep"}}}
	wantUser, _ := json.Marshal(user)
	for _, on := range []bool{true, false, false, true, true} {
		setRTK(m, on)
		if rtkEnabled(m) != on {
			t.Fatalf("rtkEnabled after %v", on)
		}
		hooks := hooksMap(m)
		arr := hooks["PreToolUse"].([]any)
		gotUser, _ := json.Marshal(arr[0])
		if string(gotUser) != string(wantUser) {
			t.Fatalf("user hook changed: %s", gotUser)
		}
		count := 0
		for _, e := range arr {
			em := e.(map[string]any)
			for _, h := range em["hooks"].([]any) {
				if hookCommandMatches(h, rtkHookCommand) {
					count++
				}
			}
		}
		wantCount := 0
		if on {
			wantCount = 1
		}
		if count != wantCount {
			t.Fatalf("RTK count = %d, want %d", count, wantCount)
		}
		if !reflect.DeepEqual(mixed["hooks"], []any{commandHook("log-command")}) && !on {
			t.Fatal("mixed user command lost")
		}
		if mixed["extra"] != "mixed" || m["other"] != true || !reflect.DeepEqual(hooks["Stop"], []any{"keep"}) {
			t.Fatal("unrelated settings changed")
		}
	}
}

func TestHookCommandMatches(t *testing.T) {
	for _, tc := range []struct {
		command, want string
		match         bool
	}{
		{"rtk hook claude", rtkHookCommand, true},
		{"echo rtk hook claude", rtkHookCommand, false},
		{"rtk hook claude && guard", rtkHookCommand, false},
		{"/installed/agent session-status question", statusHookCmd("question"), true},
		{"echo session-status question", statusHookCmd("question"), false},
		{"/installed/agent session-status plan", statusHookCmd("question"), false},
	} {
		if got := hookCommandMatches(commandHook(tc.command), tc.want); got != tc.match {
			t.Errorf("%q: got %v", tc.command, got)
		}
	}
}

func TestEnsureStatusHooksCoexistsWithUserMatchers(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	users := []any{}
	for _, matcher := range []string{"AskUserQuestion", "ExitPlanMode", permToolMatcher} {
		users = append(users, map[string]any{"matcher": matcher, "hooks": []any{commandHook("user-guard")}, "extra": "keep"})
	}
	m := map[string]any{"hooks": map[string]any{"PreToolUse": users}}
	if err := writeSettings(m); err != nil {
		t.Fatal(err)
	}
	EnsureStatusHooks()
	first := readSettings()
	EnsureStatusHooks()
	if !reflect.DeepEqual(first, readSettings()) {
		t.Fatal("installation not idempotent")
	}
	arr := hooksMap(first)["PreToolUse"].([]any)
	if !reflect.DeepEqual(arr[:len(users)], users) {
		t.Fatal("user entries changed")
	}
	for matcher, state := range map[string]string{"AskUserQuestion": "question", "ExitPlanMode": "plan", permToolMatcher: "permtool"} {
		if !preToolUseHasCommand(hooksMap(first), matcher, statusHookCmd(state)) {
			t.Errorf("missing %s hook", state)
		}
	}
	if len(arr) != 6 {
		t.Fatalf("got %d entries, want 6", len(arr))
	}
}

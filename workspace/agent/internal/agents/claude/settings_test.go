package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRTKToggleAcrossMatchers(t *testing.T) {
	for _, matcher := range []string{"Bash|Write", "", "Write"} {
		t.Run(matcher, func(t *testing.T) {
			entry := map[string]any{"matcher": matcher, "hooks": []any{commandHook(rtkHookCommand), commandHook("user-guard")}, "extra": "keep"}
			m := map[string]any{"hooks": map[string]any{"PreToolUse": []any{entry}}}
			if !rtkEnabled(m) {
				t.Fatal("RTK command not detected")
			}
			setRTK(m, false)
			if rtkEnabled(m) {
				t.Fatal("RTK remains enabled")
			}
			if !reflect.DeepEqual(entry["hooks"], []any{commandHook("user-guard")}) || entry["extra"] != "keep" {
				t.Fatal("user hook or attributes lost")
			}
		})
	}
}

func TestEnsureStatusHooksPreservesWorkingAlternateExe(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// The current executable exists outside paths treated as volatile by repair.
	exe := "/proc/self/exe"
	entries := []any{}
	for matcher, state := range map[string]string{"AskUserQuestion": "question", "ExitPlanMode": "plan", permToolMatcher: "permtool"} {
		entries = append(entries, map[string]any{"matcher": matcher, "hooks": []any{commandHook(exe + " session-status " + state)}})
	}
	if err := writeSettings(map[string]any{"hooks": map[string]any{"PreToolUse": entries}}); err != nil {
		t.Fatal(err)
	}
	EnsureStatusHooks()
	EnsureStatusHooks()
	if !reflect.DeepEqual(hooksMap(readSettings())["PreToolUse"], entries) {
		t.Fatal("working alternate hooks changed or duplicated")
	}
}

// TestIdleCompactionSetting pins the absent-means-on reading Claude Code itself uses, and that a
// PUT of the switch writes only that key.
func TestIdleCompactionSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		HandleSettingsGet(rec, httptest.NewRequest(http.MethodGet, "/claude/settings", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET body: %v", err)
		}
		return body
	}
	// put checks the PUT answer too: the Console replaces its state with it, so a reply without
	// idleCompaction would hide the row right after a save.
	put := func(body string, want bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		HandleSettingsPut(rec, httptest.NewRequest(http.MethodPut, "/claude/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("PUT body: %v", err)
		}
		if got[idleCompactionKey] != want {
			t.Fatalf("PUT %s answered idleCompaction = %v, want %v", body, got[idleCompactionKey], want)
		}
	}
	onDisk := func() map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if got := get()[idleCompactionKey]; got != true {
		t.Fatalf("no settings.json: idleCompaction = %v, want true (the CLI default)", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"opus","agentPushNotifEnabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := get()[idleCompactionKey]; got != true {
		t.Fatalf("key absent: idleCompaction = %v, want true", got)
	}

	put(`{"idleCompaction":false}`, false)
	if got := get()[idleCompactionKey]; got != false {
		t.Fatalf("after PUT false: idleCompaction = %v", got)
	}
	m := onDisk()
	if m[idleCompactionKey] != false || m["model"] != "opus" || m["agentPushNotifEnabled"] != true {
		t.Fatalf("settings.json after PUT false = %v", m)
	}

	// A PUT of another switch must not touch the user's choice.
	put(`{"agentPushNotifEnabled":false}`, false)
	if m := onDisk(); m[idleCompactionKey] != false {
		t.Fatalf("unrelated PUT changed idleCompaction: %v", m)
	}

	put(`{"idleCompaction":true}`, true)
	if m := onDisk(); m[idleCompactionKey] != true {
		t.Fatalf("after PUT true: %v", m)
	}
}

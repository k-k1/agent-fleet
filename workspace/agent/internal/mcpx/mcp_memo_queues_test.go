package mcpx

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

var memoQueueToolNames = []string{"list_memo_queues", "get_memo_queue_runs"}

func toolNameSet(tools []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, t := range tools {
		out[t["name"].(string)] = t
	}
	return out
}

// The operator reads auto-run queues and nothing more: AF_MEMO_TOKEN reaches every session of a
// workspace, so writing settings, releasing, returning, running now, resuming and ending are the
// person's Console controls (ADR 0137 addendum 2026-11-21).
func TestMemoQueueToolsAreReadOnly(t *testing.T) {
	for _, write := range []bool{false, true} {
		got := toolNameSet(operatorTools(write))
		for _, n := range memoQueueToolNames {
			if _, ok := got[n]; !ok {
				t.Errorf("operator (write=%v) lacks %s", write, n)
			}
		}
		for name := range got {
			if slices.Contains(memoQueueToolNames, name) || name == "defer_queue_item" {
				continue
			}
			for _, bad := range []string{"memo_queue", "release", "approve", "memo_run"} {
				if strings.Contains(name, bad) {
					t.Errorf("operator tool %s drives a memo queue: only the two reads exist", name)
				}
			}
		}
		for _, n := range []string{"set_memo_queue", "update_memo_queue", "put_memo_queue", "delete_memo_queue", "release_memos",
			"return_memo_queue_run", "run_memo_queue_now", "resume_memo_queue", "stop_memo_queue_run", "end_memo_queue_run"} {
			if _, ok := got[n]; ok {
				t.Errorf("operator tool %s must not exist", n)
			}
		}
	}
	// The call-side gate: a name nobody advertises is not a tool.
	startToolAudience(t, toolAudience{args: []string{"--write", "--conv", "conv1"}})
	for _, n := range []string{"run_memo_queue_now", "resume_memo_queue", "stop_memo_queue_run"} {
		if _, known := toolNamed(n); known {
			t.Errorf("%s is a known tool", n)
		}
	}
}

// A session's tools/call of the operator's reads is refused, and nothing reaches the CP.
func TestMemoQueueToolsAreNotReachableFromSessions(t *testing.T) {
	fake := startToolAudience(t, toolAudience{})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")
	asSession(t)
	names := advertisedNames(t)
	for _, n := range memoQueueToolNames {
		if names[n] {
			t.Errorf("%s reached the session surface", n)
		}
		resp := callTool(n, map[string]any{"categoryId": "c1"})
		if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "tools/list に無い") {
			t.Errorf("%s was not refused for a session: %s", n, resp)
		}
	}
	if log := fake.take(); len(log) != 0 {
		t.Errorf("a refused call reached the CP: %v", log)
	}
}

// The descriptions say what is read-only, what waiting is, and which Console control to press.
func TestMemoQueueToolDescriptionsPointAtTheConsole(t *testing.T) {
	got := toolNameSet(operatorTools(true))
	list := got["list_memo_queues"]["description"].(string)
	for _, want := range []string{"読み取り専用", "指示の全文", "Console", "waiting は待機メモの件数ではない", "list_memos", "今すぐ実行", "終了"} {
		if !strings.Contains(list, want) {
			t.Errorf("list_memo_queues description lacks %q", want)
		}
	}
	runs := got["get_memo_queue_runs"]["description"].(string)
	for _, want := range []string{"読み取り専用", "再開", "終了", "Console"} {
		if !strings.Contains(runs, want) {
			t.Errorf("get_memo_queue_runs description lacks %q", want)
		}
	}
}

func TestMemoQueueToolsCallTheTokenFace(t *testing.T) {
	fake := startToolAudience(t, toolAudience{args: []string{"--conv", "conv1"}})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_memo_queues", map[string]any{}, "GET /internal/memo-queues"},
		{"get_memo_queue_runs", map[string]any{"categoryId": "c 1"}, "GET /internal/memo-queues/c%201/deliveries"},
	} {
		fake.take()
		if resp := callTool(c.tool, c.args); strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s refused: %s", c.tool, resp)
			continue
		}
		log := fake.take()
		if len(log) != 1 || !strings.HasPrefix(log[0], c.want) {
			t.Errorf("%s sent %v, want %s", c.tool, log, c.want)
		}
	}
	if resp := callTool("get_memo_queue_runs", map[string]any{}); !strings.Contains(resp, `"isError":true`) {
		t.Errorf("a missing categoryId was not refused: %s", resp)
	}
}

// The CP's error code reaches the operator as a sentence, the CP's own message kept.
func TestMemoQueueErrorCodesAreReadable(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"queue not found"}}`))
	}))
	defer cp.Close()
	startToolAudience(t, toolAudience{args: []string{"--conv", "conv1"}})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")
	t.Setenv("AF_CP_INTERNAL_URL", cp.URL)
	resp := callTool("get_memo_queue_runs", map[string]any{"categoryId": "c1"})
	for _, want := range []string{"見つかりません", "not_found", "queue not found"} {
		if !strings.Contains(resp, want) {
			t.Errorf("answer lacks %q: %s", want, resp)
		}
	}
	if strings.Contains(resp, `{"error"`) || strings.Contains(resp, `{\"error\"`) {
		t.Errorf("raw error JSON passed through: %s", resp)
	}
}

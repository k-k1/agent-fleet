package mcpx

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

var memoQueueToolNames = []string{"list_memo_queues", "get_memo_queue_runs", "run_memo_queue_now", "resume_memo_queue", "stop_memo_queue_run"}

func toolNameSet(tools []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, t := range tools {
		out[t["name"].(string)] = t
	}
	return out
}

// The operator gets the queue's reads and its run controls, and nothing that writes the settings,
// releases memos or returns a run's memos: those stay the person's act in the Console (ADR 0137
// section 3 and addendum 2026-11-21), and AF_MEMO_TOKEN reaches every session of a workspace.
func TestMemoQueueToolsOfferNoSettingsWriteOrRelease(t *testing.T) {
	write := toolNameSet(operatorTools(true))
	for _, n := range memoQueueToolNames {
		if _, ok := write[n]; !ok {
			t.Errorf("operator --write lacks %s", n)
		}
	}
	for name := range write {
		if strings.Contains(name, "release") || strings.Contains(name, "approve") {
			t.Errorf("operator tool %s releases memos", name)
		}
		if strings.Contains(name, "queue") && !slices.Contains(memoQueueToolNames, name) &&
			name != "defer_queue_item" {
			t.Errorf("unexpected queue tool %s: settings, release and return are not the operator's", name)
		}
	}
	for _, n := range []string{"set_memo_queue", "update_memo_queue", "put_memo_queue", "disable_memo_queue", "turn_off_memo_queue", "release_memos", "return_memo_queue_run"} {
		if _, ok := write[n]; ok {
			t.Errorf("operator tool %s must not exist", n)
		}
	}
	// No description may tell the operator it can release or change settings through a tool.
	for _, n := range memoQueueToolNames {
		d, _ := write[n]["description"].(string)
		if !strings.Contains(d, "ADR") && n == "list_memo_queues" {
			t.Errorf("list_memo_queues must point at ADR 0137")
		}
	}

	read := toolNameSet(operatorTools(false))
	for _, n := range []string{"list_memo_queues", "get_memo_queue_runs"} {
		if _, ok := read[n]; !ok {
			t.Errorf("read-only operator lacks %s", n)
		}
	}
	for _, n := range []string{"run_memo_queue_now", "resume_memo_queue", "stop_memo_queue_run"} {
		if _, ok := read[n]; ok {
			t.Errorf("read-only operator has %s", n)
		}
	}
}

// An ordinary session never sees these tools and cannot reach the handler by guessing a name.
func TestMemoQueueToolsAreNotOfferedToSessions(t *testing.T) {
	asSession(t)
	names := advertisedNames(t)
	for _, n := range memoQueueToolNames {
		if names[n] {
			t.Errorf("%s reached the session surface", n)
		}
	}
}

// The tools that start or stop unattended work, and the settings guidance, make the operator
// show the user what will happen and get an OK in chat first.
func TestMemoQueueToolDescriptionsAskBeforeActing(t *testing.T) {
	write := toolNameSet(operatorTools(true))
	for _, n := range []string{"run_memo_queue_now", "resume_memo_queue", "stop_memo_queue_run"} {
		if d := write[n]["description"].(string); !strings.Contains(d, "了解を得") {
			t.Errorf("%s does not tell the operator to get the user's OK first", n)
		}
	}
	d := write["list_memo_queues"]["description"].(string)
	for _, want := range []string{"指示の全文", "Console", "できない"} {
		if !strings.Contains(d, want) {
			t.Errorf("list_memo_queues description lacks %q", want)
		}
	}
}

func TestMemoQueueToolsCallTheTokenFace(t *testing.T) {
	fake := startToolAudience(t, toolAudience{args: []string{"--write", "--conv", "conv1"}})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_memo_queues", map[string]any{}, "GET /internal/memo-queues"},
		{"get_memo_queue_runs", map[string]any{"categoryId": "c 1"}, "GET /internal/memo-queues/c%201/deliveries"},
		{"run_memo_queue_now", map[string]any{"categoryId": "c1"}, "POST /internal/memo-queues/c1/fire"},
		{"resume_memo_queue", map[string]any{"categoryId": "c1"}, "POST /internal/memo-queues/c1/resume"},
		{"stop_memo_queue_run", map[string]any{"categoryId": "c1", "id": "d1"}, "POST /internal/memo-queues/c1/deliveries/d1/end"},
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
	if resp := callTool("run_memo_queue_now", map[string]any{}); !strings.Contains(resp, `"isError":true`) {
		t.Errorf("a missing categoryId was not refused: %s", resp)
	}
}

// Without --write the run controls refuse and nothing reaches the CP.
func TestMemoQueueRunControlsNeedWrite(t *testing.T) {
	fake := startToolAudience(t, toolAudience{args: []string{"--conv", "conv1"}})
	for _, n := range []string{"run_memo_queue_now", "resume_memo_queue"} {
		if resp := callTool(n, map[string]any{"categoryId": "c1"}); !strings.Contains(resp, `"isError":true`) {
			t.Errorf("%s ran without --write: %s", n, resp)
		}
	}
	if resp := callTool("stop_memo_queue_run", map[string]any{"categoryId": "c1", "id": "d1"}); !strings.Contains(resp, `"isError":true`) {
		t.Errorf("stop_memo_queue_run ran without --write: %s", resp)
	}
	if log := fake.take(); len(log) != 0 {
		t.Errorf("a refused call reached the CP: %v", log)
	}
}

// The CP's error code reaches the operator as a sentence, the CP's own message kept.
func TestMemoQueueErrorCodesAreReadable(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"memo_queue_nothing_pending","message":"nothing in this queue is waiting to be sent"}}`))
	}))
	defer cp.Close()
	startToolAudience(t, toolAudience{args: []string{"--write", "--conv", "conv1"}})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")
	t.Setenv("AF_CP_INTERNAL_URL", cp.URL)
	resp := callTool("run_memo_queue_now", map[string]any{"categoryId": "c1"})
	for _, want := range []string{"待機中", "承認カード", "memo_queue_nothing_pending", "nothing in this queue"} {
		if !strings.Contains(resp, want) {
			t.Errorf("answer lacks %q: %s", want, resp)
		}
	}
	if strings.Contains(resp, `{"error"`) || strings.Contains(resp, `{\"error\"`) {
		t.Errorf("raw error JSON passed through: %s", resp)
	}
}

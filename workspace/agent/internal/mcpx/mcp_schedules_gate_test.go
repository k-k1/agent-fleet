package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// The gate argument reaches the CP untouched, and an update without one sends no gate key at
// all: the CP clears a gate only when the key is present, so an Agent that invented a default
// would silently drop an existing gate.
func TestScheduleGateArgumentIsForwardedVerbatim(t *testing.T) {
	fake := startToolAudience(t, toolAudience{args: []string{"--write", "--conv", "conv1"}})
	t.Setenv("AF_CP_BASE_URL", "http://cp.invalid")

	gate := map[string]any{"kind": "work_items", "query_id": "q1", "fingerprint": "keys+updated", "max_fires_per_day": 3, "min_fire_gap_s": 3600}
	resp := callTool("create_schedule", map[string]any{"spec_kind": "cron", "spec": "0 9 * * *", "prompt": "p", "gate": gate})
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("create_schedule with a gate refused: %s", resp)
	}
	body := lastBody(t, fake.take(), "POST /internal/schedules ")
	var sent struct {
		Gate map[string]any `json:"gate"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	for k, v := range gate {
		if got, _ := json.Marshal(sent.Gate[k]); string(got) != mustJSON(v) {
			t.Errorf("gate.%s = %s, want %s", k, got, mustJSON(v))
		}
	}

	resp = callTool("update_schedule", map[string]any{"id": "s1", "prompt": "new"})
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("update_schedule refused: %s", resp)
	}
	body = lastBody(t, fake.take(), "PATCH /internal/schedules/s1 ")
	if strings.Contains(body, "gate") {
		t.Errorf("an update without gate sent one: %s", body)
	}

	callTool("update_schedule", map[string]any{"id": "s1", "gate": map[string]any{"kind": "none"}})
	if body = lastBody(t, fake.take(), "PATCH /internal/schedules/s1 "); !strings.Contains(body, `"gate":{"kind":"none"}`) {
		t.Errorf("gate removal not forwarded: %s", body)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func lastBody(t *testing.T, log []string, prefix string) string {
	t.Helper()
	for i := len(log) - 1; i >= 0; i-- {
		if strings.HasPrefix(log[i], prefix) {
			return strings.TrimPrefix(log[i], prefix)
		}
	}
	t.Fatalf("no %q request in %v", prefix, log)
	return ""
}

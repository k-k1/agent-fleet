package agents

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The Go types of ADR 0105's wire round-trip the shared fixture unchanged: a renamed field or
// a lost omitempty shows up here before the Console, which reads the same file, sees a key it
// does not know.
func TestStopQueueWireFixtureRoundTrips(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/stop-queue-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Turn map[string]struct {
			Response json.RawMessage `json:"response"`
		} `json:"turn"`
		Messages map[string]map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	checked := 0
	check := func(what string, src json.RawMessage, v any) {
		t.Helper()
		if err := json.Unmarshal(src, v); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		got, _ := json.Marshal(v)
		var g, w any
		_ = json.Unmarshal(got, &g)
		_ = json.Unmarshal(src, &w)
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s does not round-trip:\n got %s\nwant %s", what, got, src)
		}
		checked++
	}
	for name, c := range fx.Turn {
		var env map[string]json.RawMessage
		_ = json.Unmarshal(c.Response, &env)
		if d, ok := env["discard"]; ok {
			check(name+".stop/discard", mustJSON(t, map[string]json.RawMessage{"stop": env["stop"], "discard": d}), &InterruptResult{})
		}
		if r, ok := env["removed"]; ok {
			check(name+".removed", r, &QueueItem{})
		}
	}
	for name, m := range fx.Messages {
		if v, ok := m["queuedItems"]; ok {
			check(name+".queuedItems", v, &[]QueueItem{})
		}
		if v, ok := m["discardedInputs"]; ok {
			check(name+".discardedInputs", v, &[]Discard{})
		}
	}
	if checked < 8 {
		t.Fatalf("only %d fixture parts checked: the fixture's shape moved under this test", checked)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

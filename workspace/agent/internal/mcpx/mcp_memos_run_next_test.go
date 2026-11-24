package mcpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// A create that asked for the run-next mark and got a note without it says so: an older Control
// Plane drops the field, and an existing note returned unchanged is not marked. An answer with the
// mark, and a call that asked for none, say nothing.
func TestMemoRunNextProblem(t *testing.T) {
	problem := func(args, answer string) string {
		return memoRunNextProblem(json.RawMessage(args), answer)
	}
	ask := `{"kind":"text","body":"x","runNext":true}`
	if got := problem(ask, `{"id":"m1","state":"held"}`); !strings.Contains(got, "does not support") {
		t.Errorf("an answer without the mark: %q", got)
	}
	if got := problem(ask, `{"id":"m0","existing":true}`); !strings.Contains(got, "Nothing was added") {
		t.Errorf("an existing note: %q", got)
	}
	if got := problem(ask, `{"id":"m1","runNext":true}`); got != "" {
		t.Errorf("an answer with the mark: %q", got)
	}
	for _, args := range []string{`{"kind":"text"}`, `{"kind":"text","runNext":false}`, `{"runNext":"yes"}`} {
		if got := problem(args, `{"id":"m1"}`); got != "" {
			t.Errorf("%s: %q", args, got)
		}
	}
}

package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"testing"
)

func TestWithOwnerConvStamps(t *testing.T) {
	// A create_schedule body gets owner_conv stamped to the operator conv id, and any
	// client-supplied value is overridden (reports only ever go to the operator itself).
	body := mcpx.WithOwnerConv(json.RawMessage(`{"spec_kind":"cron","spec":"0 9 * * *","owner_conv":"attacker"}`), "conv-op")
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["owner_conv"] != "conv-op" {
		t.Errorf("owner_conv = %v, want conv-op (client value must be overridden)", m["owner_conv"])
	}
	if m["spec"] != "0 9 * * *" {
		t.Errorf("other fields dropped: %v", m)
	}
}

func TestWithOwnerConvBadJSON(t *testing.T) {
	// Malformed input is returned unchanged (the CP then rejects it).
	in := json.RawMessage(`not json`)
	if got := mcpx.WithOwnerConv(in, "conv-op"); string(got) != "not json" {
		t.Errorf("bad json not passed through: %q", got)
	}
}

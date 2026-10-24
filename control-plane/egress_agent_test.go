package main

import (
	"encoding/json"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressagent"
)

// The gateway agent has its own copy of the event wire type so it does not import main.
// This keeps the two in step: every field the agent sends must reach the CP intact.
func TestEgressAgentEventDecodesIntoIngest(t *testing.T) {
	sent := egressagent.Event{
		ID: "ee63f33d0236ef38@148", At: 1761300001000, Source: "10.0.1.5", Dest: "140.82.112.3",
		Host: "api.github.com", Allowed: true, Count: 1,
	}
	b, err := json.Marshal(struct {
		Events []egressagent.Event `json:"events"`
	}{[]egressagent.Event{sent}})
	if err != nil {
		t.Fatal(err)
	}
	var got egressIngest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("decoded %d events", len(got.Events))
	}
	e := got.Events[0]
	if e.ID != sent.ID || e.At != sent.At || e.Source != sent.Source || e.Dest != sent.Dest ||
		e.Host != sent.Host || e.Allowed != sent.Allowed || e.Count != sent.Count {
		t.Fatalf("decoded %+v from %+v", e, sent)
	}
	ev, ok := rawEgressEvent(e, e.Host)
	if !ok || ev.ID != sent.ID || ev.Source != sent.Source || ev.At != sent.At {
		t.Fatalf("ingest refuses or alters the agent's event: %+v ok=%v", ev, ok)
	}

	// A connection with no name is still an event the CP keeps.
	sent.Host, sent.Allowed = "", false
	b, _ = json.Marshal(sent)
	var bare egressEvent
	if err := json.Unmarshal(b, &bare); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawEgressEvent(bare, ""); !ok {
		t.Fatal("ingest refuses an event with no host")
	}
}

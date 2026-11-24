package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentNotificationRowOrigin(t *testing.T) {
	decode := func(s string) agentNotification {
		var in agentNotification
		if err := json.Unmarshal([]byte(s), &in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	for _, c := range []struct {
		name, body, root string
		child            bool
	}{
		{"old agent", `{"id":"1","kind":"question","sessionName":"s"}`, "own", false},
		{"autorun child", `{"id":"2","kind":"question","sessionName":"s","originRoot":"autorun","originChild":true}`, "autorun", true},
		{"schedule root", `{"id":"3","kind":"question","sessionName":"s","originRoot":"schedule"}`, "schedule", false},
		{"unknown root", `{"id":"4","kind":"question","sessionName":"s","originRoot":"x","originChild":true}`, "own", true},
	} {
		n := agentNotificationRow(decode(c.body), "m1")
		if n.OriginRoot != c.root || n.OriginChild != c.child {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, n.OriginRoot, n.OriginChild, c.root, c.child)
		}
	}
}

// One entry with wrongly typed origin keys must neither fail the decode of the response nor take
// its neighbours down.
func TestOutboxWithMalformedOriginStillDecodes(t *testing.T) {
	body := `{"notifications":[{"id":"1","kind":"question","sessionName":"a","originChild":"true","originRoot":7},` +
		`{"id":"2","kind":"question","sessionName":"b","originRoot":"schedule","originChild":true}]}`
	entries, _, ok := readOutboxEntries(strings.NewReader(body), 1<<20)
	if !ok || len(entries) != 2 {
		t.Fatalf("ok=%v entries=%d", ok, len(entries))
	}
	if r, c := entries[0].originOf(); r != "own" || c {
		t.Errorf("malformed = (%q,%v)", r, c)
	}
	if r, c := entries[1].originOf(); r != "schedule" || !c {
		t.Errorf("good = (%q,%v)", r, c)
	}
}

package main

import (
	"encoding/json"
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

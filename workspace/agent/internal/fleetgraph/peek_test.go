package fleetgraph

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// A peek is kept in the ledger for the audit, but stays off the wire: the Console's graph
// renders a fixed set of event kinds, and an unknown one would be a contract change.
func TestRecordPeekIsLedgerOnly(t *testing.T) {
	withTempState(t)
	RecordPeek("reader", "target")
	RecordPeek("", "target") // no reader, no line

	b, err := os.ReadFile(activityPath(utcDay(time.Now())))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"ev":"peek"`); n != 1 || !strings.Contains(string(b), `"from":"reader","to":"target"`) {
		t.Fatalf("ledger = %s, want one peek line reader→target", b)
	}
	page, err := BuildPage(0, time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(page.Activity)
	if strings.Contains(string(wire), "peek") {
		t.Fatalf("peek leaked onto the fleet-graph wire: %s", wire)
	}
}

// A stall is the Agent's own nudge to a parent; like a peek it is ledger-only.
func TestRecordStallIsLedgerOnly(t *testing.T) {
	withTempState(t)
	RecordStall("parent", "kid1, kid2 idle 15 min")
	RecordStall("", "x") // no parent, no line

	b, err := os.ReadFile(activityPath(utcDay(time.Now())))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"ev":"stall"`); n != 1 || !strings.Contains(string(b), `"to":"parent"`) {
		t.Fatalf("ledger = %s, want one stall line to parent", b)
	}
	page, err := BuildPage(0, time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(page.Activity)
	if strings.Contains(string(wire), `"stall"`) {
		t.Fatalf("stall leaked onto the fleet-graph wire: %s", wire)
	}
}

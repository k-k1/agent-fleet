package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The live harness's tie between a GKE auto-repair and the pod's end: exactly one
// AUTO_REPAIR_NODES of this node in this project, location and cluster, started between the
// cut and the pod's end.
func TestMatchRepair(t *testing.T) {
	cut := time.Date(2026, 10, 3, 12, 7, 57, 0, time.UTC)
	gone := cut.Add(14*time.Minute + 37*time.Second)
	want := kubeLiveRepairTarget{projects: []string{"p1", "1234"}, location: "r1", cluster: "c1", node: "n1"}
	op := func(typ, start, project, location, cluster, node string) string {
		return `{"name":"operation-1","operationType":"` + typ + `","startTime":"` + start + `","status":"DONE",` +
			`"targetLink":"https://container.googleapis.com/v1/projects/` + project + `/locations/` + location +
			`/clusters/` + cluster + `/nodePools/workspace/node/` + node + `"}`
	}
	list := func(ops ...string) string { return "[" + strings.Join(ops, ",") + "]" }
	const at = "2026-10-03T12:20:06.294983277Z"
	ok := op("AUTO_REPAIR_NODES", at, "1234", "r1", "c1", "n1")
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"the repair of this node, by project number", list(ok), true},
		{"by project ID", list(op("AUTO_REPAIR_NODES", at, "p1", "r1", "c1", "n1")), true},
		{"beside other operations", list(op("UPGRADE_NODES", "2026-10-03T12:10:00Z", "1234", "r1", "c1", "n1"), ok, op("AUTO_REPAIR_NODES", at, "1234", "r1", "c1", "n2")), true},
		{"none", list(), false},
		{"another node", list(op("AUTO_REPAIR_NODES", at, "1234", "r1", "c1", "n10")), false},
		{"another cluster", list(op("AUTO_REPAIR_NODES", at, "1234", "r1", "c2", "n1")), false},
		{"another project", list(op("AUTO_REPAIR_NODES", at, "9999", "r1", "c1", "n1")), false},
		{"another location", list(op("AUTO_REPAIR_NODES", at, "1234", "r2", "c1", "n1")), false},
		{"started before the cut", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:00:00Z", "1234", "r1", "c1", "n1")), false},
		{"started after the pod went", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:30:00Z", "1234", "r1", "c1", "n1")), false},
		{"two candidates", list(ok, op("AUTO_REPAIR_NODES", "2026-10-03T12:21:00Z", "1234", "r1", "c1", "n1")), false},
		{"not JSON", "operation-1 AUTO_REPAIR_NODES", false},
	} {
		_, err := matchRepair(tc.out, want, cut, gone)
		if (err == nil) != tc.want {
			t.Errorf("%s: matchRepair error = %v, want match %v", tc.name, err, tc.want)
		}
	}
}

// Only a repair that finished without an error counts; one still running is waited for.
func TestRepairSucceeded(t *testing.T) {
	failed := kubeLiveRepairOp{Status: "DONE"}
	failed.Error = &struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: 13, Message: "internal"}
	for _, tc := range []struct {
		name    string
		op      kubeLiveRepairOp
		ok      bool
		running bool
	}{
		{"done", kubeLiveRepairOp{Status: "DONE"}, true, false},
		{"done with an error", failed, false, false},
		{"running", kubeLiveRepairOp{Status: "RUNNING"}, false, true},
		{"pending", kubeLiveRepairOp{Status: "PENDING"}, false, true},
		{"aborting", kubeLiveRepairOp{Status: "ABORTING", StatusMessage: "cancelled"}, false, false},
		{"unknown status", kubeLiveRepairOp{Status: "STATUS_UNSPECIFIED"}, false, false},
	} {
		err := repairSucceeded(tc.op)
		if (err == nil) != tc.ok || errors.Is(err, errRepairRunning) != tc.running {
			t.Errorf("%s: repairSucceeded = %v, want ok %v running %v", tc.name, err, tc.ok, tc.running)
		}
	}
}

// A node read that failed is not a replacement: only a read that found nothing, or found
// another UID, is.
func TestNodeReplacedFrom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		uid      string
		err      error
		replaced bool
		wantErr  bool
	}{
		{"the same node", "u1\n", nil, false, false},
		{"another object of that name", "u2", nil, true, false},
		{"gone", "", nil, true, false},
		{"an API error", "", errors.New("kubectl get node n1: exit status 1: Unable to connect to the server"), false, true},
	} {
		got, err := nodeReplacedFrom(tc.uid, tc.err, "u1")
		if got != tc.replaced || (err != nil) != tc.wantErr {
			t.Errorf("%s: nodeReplacedFrom = %v, %v; want replaced %v, error %v", tc.name, got, err, tc.replaced, tc.wantErr)
		}
	}
}

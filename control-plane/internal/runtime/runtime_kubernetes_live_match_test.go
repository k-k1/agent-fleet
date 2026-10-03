package runtime

import (
	"strings"
	"testing"
	"time"
)

// The live harness's tie between a GKE auto-repair and the pod's end: exactly one
// AUTO_REPAIR_NODES of this node in this cluster, started between the cut and the pod's end.
func TestMatchRepair(t *testing.T) {
	cut := time.Date(2026, 10, 3, 12, 7, 57, 0, time.UTC)
	gone := cut.Add(14*time.Minute + 37*time.Second)
	op := func(typ, start, cluster, node string) string {
		return `{"name":"operation-1","operationType":"` + typ + `","startTime":"` + start + `","status":"DONE",` +
			`"targetLink":"https://container.googleapis.com/v1/projects/1/locations/r1/clusters/` + cluster + `/nodePools/workspace/node/` + node + `"}`
	}
	list := func(ops ...string) string { return "[" + strings.Join(ops, ",") + "]" }
	ok := op("AUTO_REPAIR_NODES", "2026-10-03T12:20:06.294983277Z", "c1", "n1")
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"the repair of this node", list(ok), true},
		{"beside other operations", list(op("UPGRADE_NODES", "2026-10-03T12:10:00Z", "c1", "n1"), ok, op("AUTO_REPAIR_NODES", "2026-10-03T12:10:00Z", "c1", "n2")), true},
		{"none", list(), false},
		{"another node", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:20:06Z", "c1", "n10")), false},
		{"another cluster", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:20:06Z", "c2", "n1")), false},
		{"started before the cut", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:00:00Z", "c1", "n1")), false},
		{"started after the pod went", list(op("AUTO_REPAIR_NODES", "2026-10-03T12:30:00Z", "c1", "n1")), false},
		{"two candidates", list(ok, op("AUTO_REPAIR_NODES", "2026-10-03T12:21:00Z", "c1", "n1")), false},
		{"not JSON", "operation-1 AUTO_REPAIR_NODES", false},
	} {
		_, err := matchRepair(tc.out, "c1", "n1", cut, gone)
		if (err == nil) != tc.want {
			t.Errorf("%s: matchRepair error = %v, want match %v", tc.name, err, tc.want)
		}
	}
}

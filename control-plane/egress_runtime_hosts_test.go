package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
)

// runtimeEgressHostsPath is the ledger of hosts the agent CLIs and build tools reach at run
// time; its header documents the format.
const runtimeEgressHostsPath = "testdata/runtime-egress-hosts.txt"

// TestDefaultEgressAllowlistAdmitsRuntimeHosts keeps defaultEgressAllowlist in step with the
// run-time ledger: every plain line is admitted, "!" and "-" lines are not (an admin must be
// able to block them), and each destination tag names a catalogue destination, since ADR
// 0138 M4 drops hosts by that tag.
func TestDefaultEgressAllowlistAdmitsRuntimeHosts(t *testing.T) {
	b, err := os.ReadFile(runtimeEgressHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	var dests []string
	for _, d := range agentroutes.Destinations() {
		dests = append(dests, d.ID)
	}
	kinds := agentroutes.ModelKinds()
	p := newEgressPolicy(defaultEgressAllowlist)
	n := 0
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		n++
		if len(f) != 4 {
			t.Errorf("%q: want host kinds destination evidence, got %d fields", line, len(f))
			continue
		}
		host, kindList, dest, evidence := f[0], f[1], f[2], f[3]
		if evidence != "measured" && evidence != "docs" {
			t.Errorf("%s: evidence %q is neither measured nor docs", host, evidence)
		}
		if dest != "-" && !slices.Contains(dests, dest) {
			t.Errorf("%s: destination %q is not in agentroutes catalog.json", host, dest)
		}
		for _, k := range strings.Split(kindList, ",") {
			if k != "build" && !slices.Contains(kinds, k) {
				t.Errorf("%s: kind %q is not a catalogue kind", host, k)
			}
		}
		bare := strings.TrimLeft(host, "!-")
		if bare == "" {
			t.Errorf("%q: empty host", line)
			continue
		}
		if seen[bare] {
			t.Errorf("%s is listed twice; one host maps to one destination", bare)
		}
		seen[bare] = true
		switch {
		case strings.HasPrefix(host, "!"), strings.HasPrefix(host, "-"):
			if h := host[1:]; p.Allows(h) {
				t.Errorf("%s is left out of the defaults but defaultEgressAllowlist admits it", h)
			}
		case strings.HasPrefix(host, "."), strings.Contains(host, "*"):
			t.Errorf("%s: ledger hosts are exact; the suffix belongs in defaultEgressAllowlist", host)
		case !p.Allows(host):
			t.Errorf("run-time host %s is not admitted by defaultEgressAllowlist", host)
		}
	}
	if n < 20 {
		t.Fatalf("read only %d hosts from %s", n, runtimeEgressHostsPath)
	}
}

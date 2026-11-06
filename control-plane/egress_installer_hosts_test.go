package main

import (
	"os"
	"strings"
	"testing"
)

// installerEgressHostsPath is the Workspace Agent's list of installer download hosts;
// workspace/agent's installer_egress_hosts_test.go keeps it in step with the installers.
const installerEgressHostsPath = "../workspace/agent/testdata/installer-egress-hosts.txt"

// TestDefaultEgressAllowlistAdmitsInstallers is what stops a Console install button from
// failing on the first deployment that enforces egress: every installer host is admitted
// by the defaults, except the "!" ones, which must stay admin-controlled.
func TestDefaultEgressAllowlistAdmitsInstallers(t *testing.T) {
	b, err := os.ReadFile(installerEgressHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	p := newEgressPolicy(defaultEgressAllowlist)
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		entry, _, _ := strings.Cut(line, "#")
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		n++
		if strings.HasPrefix(entry, "~") {
			continue // named by an installer source, never fetched
		}
		if host, admin := strings.CutPrefix(entry, "!"); admin {
			if p.Allows(host) {
				t.Errorf("%s is left to the admin but the default allowlist admits it, so no admin can block it", host)
			}
		} else if !p.Allows(host) {
			t.Errorf("installer host %s is not admitted by defaultEgressAllowlist", host)
		}
	}
	if n < 10 {
		t.Fatalf("read only %d hosts from %s", n, installerEgressHostsPath)
	}
}

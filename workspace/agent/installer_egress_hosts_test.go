package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// installerEgressHostsPath is shared with control-plane's egress_installer_hosts_test.go,
// which checks the same list against the default egress allowlist.
const installerEgressHostsPath = "testdata/installer-egress-hosts.txt"

// installerSourceGlobs are the files that hold installer download URLs. A new installer
// file outside these globs escapes the check, so name it install_*.go.
var installerSourceGlobs = []string{
	"install_*.go", "node_install.go", "jdk*.go", "internal/jdkvendor/*.go",
}

var httpsHostRe = regexp.MustCompile(`https://([a-z0-9][a-z0-9.-]*[a-z0-9])`)

type installerEgressHost struct {
	host     string
	admin    bool // "!" line: left to the admin, not in the default allowlist
	redirect bool // reached only through a redirect, so absent from the sources
}

func readInstallerEgressHosts(t *testing.T) []installerEgressHost {
	t.Helper()
	b, err := os.ReadFile(installerEgressHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []installerEgressHost
	for _, line := range strings.Split(string(b), "\n") {
		entry, comment, _ := strings.Cut(line, "#")
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		h := installerEgressHost{redirect: strings.Contains(comment, "redirect")}
		h.host, h.admin = strings.CutPrefix(entry, "!")
		out = append(out, h)
	}
	return out
}

// installerSourceHosts is every https:// host literal in the installer sources, comments
// included: a URL quoted in a comment is still one the code is about to fetch.
func installerSourceHosts(t *testing.T) map[string][]string {
	t.Helper()
	hosts := map[string][]string{}
	for _, g := range installerSourceGlobs {
		files, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range httpsHostRe.FindAllStringSubmatch(string(b), -1) {
				if !slices.Contains(hosts[m[1]], f) {
					hosts[m[1]] = append(hosts[m[1]], f)
				}
			}
		}
	}
	return hosts
}

// TestInstallerEgressHostsListed keeps the shared list in step with the installers: a new
// download host has to be listed (and so, via the control-plane test, admitted by the
// default egress allowlist or deliberately left to the admin), and a listed host no
// installer uses any more has to go.
func TestInstallerEgressHostsListed(t *testing.T) {
	listed := readInstallerEgressHosts(t)
	src := installerSourceHosts(t)
	if len(src) < 10 {
		t.Fatalf("found only %d hosts in %v; the scan is not reading the installers", len(src), installerSourceGlobs)
	}
	names := make([]string, 0, len(listed))
	for _, h := range listed {
		names = append(names, h.host)
	}
	for host, files := range src {
		if !slices.Contains(names, host) {
			t.Errorf("%s downloads from %s, which %s does not list", strings.Join(files, ", "), host, installerEgressHostsPath)
		}
	}
	for _, h := range listed {
		if _, used := src[h.host]; !used && !h.redirect {
			t.Errorf("%s lists %s, which no installer source names; drop it, or mark it a redirect hop in its comment", installerEgressHostsPath, h.host)
		}
	}
}

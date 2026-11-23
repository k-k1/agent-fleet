package main

import (
	"os"
	"regexp"
	"testing"
)

// The Workspace image installs the gitleaks that ci.yml's secret-scan job runs, so a session
// scans with the same rules CI will apply. Two pins that drift mean a push that is clean
// locally turns red in CI, or the reverse.
var (
	dockerGitleaksARG = regexp.MustCompile(`(?m)^ARG GITLEAKS_VERSION=(\S+)\s*$`)
	// The pin sits in the step that installs the scanner: `VERSION: x.y.z` under `env:` of the
	// step named "install gitleaks".
	ciGitleaksVersion = regexp.MustCompile(`(?s)- name: install gitleaks[^\n]*\n.*?\n\s+VERSION:\s*(\S+)\s*\n`)
)

func gitleaksPins(dockerfile, ci string) (docker, workflow string) {
	if m := dockerGitleaksARG.FindStringSubmatch(dockerfile); m != nil {
		docker = m[1]
	}
	if m := ciGitleaksVersion.FindStringSubmatch(ci); m != nil {
		workflow = m[1]
	}
	return docker, workflow
}

func TestGitleaksPinMatchesCI(t *testing.T) {
	df, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	ci, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	docker, workflow := gitleaksPins(string(df), string(ci))
	if docker == "" {
		t.Fatal("workspace/Dockerfile has no `ARG GITLEAKS_VERSION=`")
	}
	if workflow == "" {
		t.Fatal(".github/workflows/ci.yml: no VERSION in the `install gitleaks` step")
	}
	if docker != workflow {
		t.Errorf("GITLEAKS_VERSION in workspace/Dockerfile (%s) != VERSION of ci.yml's secret-scan job (%s); bump both together (docs/build/10 §10.4)", docker, workflow)
	}
}

// The check has to be able to fail: a mismatch, and a workflow whose pin moved out from under
// the pattern, must not read as "equal".
func TestGitleaksPinsParse(t *testing.T) {
	const wf = "      - name: install gitleaks (version+checksum pinned)\n        env:\n          VERSION: 8.30.1\n          SHA256: abc\n        run: |\n          x\n"
	d, w := gitleaksPins("FROM x\nARG GITLEAKS_VERSION=8.30.1\nRUN y\n", wf)
	if d != "8.30.1" || w != "8.30.1" {
		t.Fatalf("parsed docker=%q workflow=%q, want 8.30.1 for both", d, w)
	}
	d, w = gitleaksPins("ARG GITLEAKS_VERSION=8.31.0\n", wf)
	if d == w {
		t.Errorf("a different Dockerfile pin parsed equal: %q == %q", d, w)
	}
	if d, w = gitleaksPins("ARG OTHER=1\n", "- name: something else\n  env:\n    VERSION: 1\n"); d != "" || w != "" {
		t.Errorf("unrelated text parsed as a pin: docker=%q workflow=%q", d, w)
	}
}

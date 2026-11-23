package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Workspace image installs the gitleaks that CI's secret scans run, so a session scans
// with the same rules CI will apply. Two pins that drift mean a push that is clean locally
// turns red in CI, or the reverse. CI's pin lives in the install-gitleaks composite action,
// which ci.yml's secret-scan job (and secret-sweep.yml) use.
var (
	dockerGitleaksARG = regexp.MustCompile(`(?m)^ARG GITLEAKS_VERSION=(\S+)\s*$`)
	// The step that installs the scanner, up to the next list item (a named step or a bare
	// `- uses:`): the pin is searched only there, so a VERSION in a later step or job is never
	// read as the scanner's.
	ciGitleaksStep = regexp.MustCompile(`(?s)- name: install gitleaks[^\n]*\n(.*?)(?:\n\s*- |\n\S|\z)`)
	// A step line that uses the action, exactly (not a comment, not a longer path).
	gitleaksActionStep = regexp.MustCompile(`(?m)^\s*(?:- )?uses: \./\.github/actions/install-gitleaks\s*$`)
	// ci.yml's secret-scan job, up to the next job (a two-space key) or the end.
	ciSecretScanJob   = regexp.MustCompile(`(?s)\n  secret-scan:\n(.*?)(?:\n  [A-Za-z0-9_-]+:\n|\z)`)
	ciGitleaksVersion = regexp.MustCompile(`(?m)^\s+VERSION:\s*(\S+)\s*$`)
)

const (
	gitleaksActionUse  = "./.github/actions/install-gitleaks"
	gitleaksActionFile = ".github/actions/install-gitleaks/action.yml"
)

func gitleaksPins(dockerfile, ci string) (docker, workflow string) {
	if m := dockerGitleaksARG.FindStringSubmatch(dockerfile); m != nil {
		docker = m[1]
	}
	if step := ciGitleaksStep.FindStringSubmatch(ci); step != nil {
		if m := ciGitleaksVersion.FindStringSubmatch(step[1]); m != nil {
			workflow = m[1]
		}
	}
	return docker, workflow
}

func TestGitleaksPinMatchesCI(t *testing.T) {
	df, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	// The action is only CI's pin while the scans install the scanner through it: a job that
	// went back to an inline step would leave this test comparing against a file CI ignores.
	ci, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if job := ciSecretScanJob.FindStringSubmatch(string(ci)); job == nil || !gitleaksActionStep.MatchString(job[1]) {
		t.Fatalf(".github/workflows/ci.yml: the secret-scan job no longer installs gitleaks with `uses: %s`; point this test at where the pin is now", gitleaksActionUse)
	}
	sweep, err := os.ReadFile("../../.github/workflows/secret-sweep.yml")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(gitleaksActionStep.FindAllString(string(sweep), -1)); n != 2 {
		t.Fatalf(".github/workflows/secret-sweep.yml: %d steps use %s, want 2 (sweep-main and sweep-branch)", n, gitleaksActionUse)
	}
	action, err := os.ReadFile("../../" + gitleaksActionFile)
	if err != nil {
		t.Fatal(err)
	}
	docker, workflow := gitleaksPins(string(df), string(action))
	if docker == "" {
		t.Fatal("workspace/Dockerfile has no `ARG GITLEAKS_VERSION=`")
	}
	if workflow == "" {
		t.Fatalf("%s: no VERSION in the `install gitleaks` step", gitleaksActionFile)
	}
	if docker != workflow {
		t.Errorf("GITLEAKS_VERSION in workspace/Dockerfile (%s) != VERSION in %s (%s); bump both together (docs/build/10 §10.4)", docker, gitleaksActionFile, workflow)
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
	// The composite action's shape: a top-level runs.steps list, indented by four.
	const action = "runs:\n  using: composite\n  steps:\n    - name: install gitleaks (version+checksum pinned)\n      shell: bash\n      env:\n        VERSION: 8.30.1\n        SHA256: abc\n      run: |\n        x\n"
	if _, w = gitleaksPins("", action); w != "8.30.1" {
		t.Errorf("composite action pin parsed as %q, want 8.30.1", w)
	}
	// Nor from a later unnamed `- uses:` step.
	const bare = "    - name: install gitleaks (x)\n      run: |\n        x\n    - uses: ./other\n      env:\n        VERSION: 8.30.1\n"
	if _, w = gitleaksPins("", bare); w != "" {
		t.Errorf("read a later unnamed step's VERSION as the scanner's: %q", w)
	}
	// The action guard matches a real step line only.
	for in, want := range map[string]bool{
		"      - uses: ./.github/actions/install-gitleaks\n":     true,
		"      # uses: ./.github/actions/install-gitleaks\n":     false,
		"      - uses: ./.github/actions/install-gitleaks-old\n": false,
	} {
		if got := gitleaksActionStep.MatchString(in); got != want {
			t.Errorf("action guard on %q = %v, want %v", in, got, want)
		}
	}
	// A VERSION that moved out of the step must not be picked up from a later step.
	const moved = "      - name: install gitleaks (x)\n        run: |\n          x\n      - name: other\n        env:\n          VERSION: 8.30.1\n"
	if _, w = gitleaksPins("ARG GITLEAKS_VERSION=8.30.1\n", moved); w != "" {
		t.Errorf("read a later step's VERSION as the scanner's: %q", w)
	}
	if d, w = gitleaksPins("ARG OTHER=1\n", "- name: something else\n  env:\n    VERSION: 1\n"); d != "" || w != "" {
		t.Errorf("unrelated text parsed as a pin: docker=%q workflow=%q", d, w)
	}
}

// The checksum check in the Dockerfile is what keeps a tampered tarball out of the image, so
// it is run for real: the line is taken from the Dockerfile itself (a change to it is a change
// to what is tested) and pointed at a scratch directory. A hash mismatch, an asset the
// checksums file does not list, and one it lists twice must each fail; only a single matching
// line passes.
func TestGitleaksChecksumStep(t *testing.T) {
	df, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var step string
	for _, line := range strings.Split(string(df), "\n") {
		if strings.Contains(line, "/tmp/gitleaks && awk") && strings.Contains(line, "sha256sum -c") {
			step = strings.TrimSuffix(strings.TrimSpace(line), "; \\")
		}
	}
	if step == "" {
		t.Fatal("no `cd /tmp/gitleaks && awk ... sha256sum -c` line in workspace/Dockerfile")
	}
	const asset = "gitleaks_1.2.3_linux_x64.tar.gz"
	run := func(t *testing.T, checksums func(hash string) string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, asset), []byte("payload\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("sha256sum", filepath.Join(dir, asset)).Output()
		if err != nil {
			t.Skipf("sha256sum unavailable: %v", err)
		}
		hash := strings.Fields(string(out))[0]
		if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums(hash)), 0o644); err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf("set -eux; asset=%q; %s", asset, strings.ReplaceAll(step, "/tmp/gitleaks", dir))
		cmd := exec.Command("bash", "-c", script)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Logf("output: %s", b)
			return err
		}
		return nil
	}
	other := strings.Repeat("0", 64)
	cases := []struct {
		name      string
		checksums func(hash string) string
		wantFail  bool
	}{
		{"one matching line", func(h string) string { return h + "  other.tar.gz\n" + h + "  " + asset + "\n" }, false},
		{"hash mismatch", func(string) string { return other + "  " + asset + "\n" }, true},
		{"asset not listed", func(h string) string { return h + "  other.tar.gz\n" }, true},
		{"asset listed twice", func(h string) string { return h + "  " + asset + "\n" + h + "  " + asset + "\n" }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := run(t, c.checksums)
			if c.wantFail && err == nil {
				t.Error("the checksum step passed; it must fail")
			}
			if !c.wantFail && err != nil {
				t.Errorf("the checksum step failed: %v", err)
			}
		})
	}
}

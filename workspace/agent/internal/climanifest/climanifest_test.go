package climanifest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestEntriesParse(t *testing.T) {
	es := Entries()
	if len(es) != 20 {
		t.Fatalf("entries = %d, want 20 (8 agent CLIs, rtk and 11 image tools)", len(es))
	}
	claude, ok := Lookup("claude")
	if !ok || claude.Arg != "CLAUDE_CODE_VERSION" || claude.Contract != "claude-tui-contract.yml" ||
		claude.Install != "npm" || claude.Input != "cli_version" || len(claude.Secrets) != 2 {
		t.Fatalf("claude = %+v", claude)
	}
	if oc, _ := Lookup("opencode"); !oc.HasFlag("major-manual") {
		t.Fatalf("opencode lost its major-manual flag: %+v", oc)
	}
	if rtk, _ := Lookup("rtk"); rtk.Contract != "" || rtk.Dispatch != "" {
		t.Fatalf("rtk has no contract: %+v", rtk)
	}
	if aws, _ := Lookup("awscli"); !aws.HasFlag("pr-gated") {
		t.Fatalf("awscli lost its pr-gated flag: %+v", aws)
	}
}

func TestParseRefusesMalformed(t *testing.T) {
	for name, text := range map[string]string{
		"empty":          "# only a comment\n",
		"short row":      "a|A_VERSION|npm|p|-|npm|-|-|-|-\n",
		"long row":       "a|A_VERSION|npm|p|-|npm|-|-|-|-|-|x\n",
		"duplicate":      "a|A_VERSION|npm|p|-|npm|-|-|-|-|-\na|A_VERSION|npm|p|-|npm|-|-|-|-|-\n",
		"no dispatch":    "a|A_VERSION|npm|p|a.yml|npm|-|-|-|-|-\n",
		"no contract":    "a|A_VERSION|npm|p|-|npm|auto|-|-|-|-\n",
		"empty source":   "a|A_VERSION||p|-|npm|-|-|-|-|-\n",
		"gated contract": "a|A_VERSION|npm|p|a.yml|npm|auto|-|-|-|pr-gated\n",
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Positive control: the same shape, well formed.
	if _, err := Parse("a|A_VERSION|npm|p|-|npm|-|-|-|-|-\n"); err != nil {
		t.Fatalf("a good row was refused: %v", err)
	}
}

// The shell reader and this one read the same file, so they must agree on what is in it.
func TestShellReaderAgrees(t *testing.T) {
	out, err := exec.Command("bash", "../../../../deploy/local/cli-manifest.sh", "names").Output()
	if err != nil {
		t.Skipf("shell reader not runnable here: %v", err)
	}
	var shell []string
	for _, l := range strings.Fields(string(out)) {
		shell = append(shell, l)
	}
	var goNames []string
	for _, e := range Entries() {
		goNames = append(goNames, e.Name)
	}
	if strings.Join(shell, " ") != strings.Join(goNames, " ") {
		t.Fatalf("shell reads %v, Go reads %v", shell, goNames)
	}
}

// Every pinned ARG is defined exactly once in the Dockerfile.
func TestArgsAreInTheDockerfile(t *testing.T) {
	b, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Skipf("Dockerfile not reachable: %v", err)
	}
	for _, e := range Entries() {
		if n := strings.Count(string(b), "\nARG "+e.Arg+"="); n != 1 {
			t.Errorf("%s: ARG %s defined %d times, want 1", e.Name, e.Arg, n)
		}
	}
}

// cli-pin-bump.sh rewrites only the version ARG of a pr-gated row; a tool that also pins a
// checksum would ship a new version against the old sha256 and fail the image build.
func TestPRGatedRowsPinOnlyTheirVersion(t *testing.T) {
	b, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range Entries() {
		if !e.HasFlag("pr-gated") {
			continue
		}
		n++
		prefix := strings.TrimSuffix(e.Arg, "VERSION")
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "ARG "+prefix) && !strings.HasPrefix(line, "ARG "+e.Arg+"=") {
				t.Errorf("%s is pr-gated, but the Dockerfile also pins %q, which the bump does not rewrite", e.Name, line)
			}
		}
	}
	if n == 0 {
		t.Fatal("no pr-gated row: the check above ran over nothing")
	}
}

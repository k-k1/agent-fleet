package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extractVer pulls the version number out of the real --version output of each tool
// (measured samples).
func TestExtractVer(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"2.1.207 (Claude Code)", "2.1.207"},
		{"1.17.18", "1.17.18"},
		{"codex-cli 0.144.1", "0.144.1"},
		{"rtk 0.43.0", "0.43.0"},
		{"gh version 2.96.0 (2026-07-02)", "2.96.0"},
		{"go version go1.26.4 linux/amd64", "1.26.4"},
		{"v22.17.0", "22.17.0"},
		{"Python 3.11.2", "3.11.2"},
		{"2026.07.20-8cc9c0b", "2026.07.20"}, // cursor: date-based version (the sha suffix is dropped)

		{"(timeout)", "(timeout)"}, // no number: the raw string is returned unchanged
	}
	for _, c := range cases {
		if got := extractVer(c.raw); got != c.want {
			t.Errorf("extractVer(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// probeVersion returns nil for a path that does not exist, and the UI renders a dash.
func TestProbeVersionMissing(t *testing.T) {
	if got := probeVersion(context.Background(), "/no/such/binary", nil); got != nil {
		t.Errorf("probeVersion(missing) = %+v, want nil", got)
	}
}

// toolProbe never launches the same real binary twice. The symptom of "(timeout) in the
// effective column while the ~/.local column has a version" came from launching once per
// column even though, in the lean variant, all three columns point at the same
// ~/.local/bin/<cmd>; relaxing this brings it back. One launch even across a symlink, while
// Path stays the path each column asked for.
func TestToolProbeDedupesByRealPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "tool-real")
	// Appends on every launch, so the number of runs is countable as lines in the file.
	counter := filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> " + counter + "\necho 'tool 1.2.3'\n"
	if err := os.WriteFile(real, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tool-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	tp := newToolProbe(context.Background(), toolSpec{Name: "t", Cmd: "tool-real"}, dir)
	a, b := tp.at(link), tp.at(real)
	if a == nil || a.Version != "1.2.3" || b == nil || b.Version != "1.2.3" {
		t.Fatalf("versions = %+v / %+v, want 1.2.3 both", a, b)
	}
	if a.Path != link || b.Path != real {
		t.Errorf("paths = %q / %q, want %q / %q (each column keeps its own path for the tooltip)", a.Path, b.Path, link, real)
	}
	runs, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(runs), "run"); got != 1 {
		t.Errorf("launches = %d, want 1 (the same real binary is reused)", got)
	}
	// A column with no binary stays nil (rendered as a dash), and that is remembered too
	// rather than retried.
	if got := tp.at(filepath.Join(dir, "nope")); got != nil {
		t.Errorf("at(missing) = %+v, want nil", got)
	}
}

// uvToolVersion reads the version of a `uv tool install`ed Python MCP server from the
// dist-info name WITHOUT exec'ing it: --version starts the server for the cloudwatch MCP,
// and the AWS MCP proxy has no --version at all (see the comment on toolSpec.PyDist). The
// venv root differs between a baked install (/usr/local) and a user install (home), so this
// also covers picking the root by whether the binary lives under home.
func TestUVToolVersion(t *testing.T) {
	home := t.TempDir()
	// Redirect the baked root into a temp dir. Left at the real path
	// (/usr/local/share/uv/tools), the case below that asserts "not present on the baked
	// side" picks up this container's actual baked install (mcp_proxy_for_aws-1.6.4.dist-info)
	// and fails: a test that passes only on a CI runner with nothing baked.
	baked := t.TempDir()
	prevBaked := bakedUVToolRoot
	bakedUVToolRoot = baked
	t.Cleanup(func() { bakedUVToolRoot = prevBaked })
	// Imitate a user-installed uv tool: <home>/.local/share/uv/tools/<tool>/...
	tool := filepath.Join(home, ".local", "share", "uv", "tools", "mcp-proxy-for-aws")
	sp := filepath.Join(tool, "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(filepath.Join(sp, "mcp_proxy_for_aws-1.6.4.dist-info"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, ".local", "bin", "mcp-proxy-for-aws")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := uvToolVersion(exe, "mcp-proxy-for-aws", home)
	if got == nil || got.Version != "1.6.4" {
		t.Fatalf("uvToolVersion = %+v, want 1.6.4", got)
	}
	// No binary means nil (the UI shows a dash, i.e. not installed).
	if got := uvToolVersion(filepath.Join(home, ".local", "bin", "nope"), "mcp-proxy-for-aws", home); got != nil {
		t.Errorf("uvToolVersion(missing) = %+v, want nil", got)
	}
	// The binary exists but its venv is under a different root (so the baked side gets
	// consulted): do not turn that into "not installed" — show the binary with an unknown
	// version. Passing home="" defeats the home check and hits the baked side.
	if got := uvToolVersion(exe, "mcp-proxy-for-aws", ""); got == nil || got.Version != "" {
		t.Errorf("uvToolVersion(root mismatch) = %+v, want an unknown version", got)
	}
	// The other direction: a binary outside home reads its version from the baked root's
	// venv. A different version is planted there, so if the root choice is inverted 1.6.4
	// comes back and the test fails.
	bakedSP := filepath.Join(baked, "mcp-proxy-for-aws", "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(filepath.Join(bakedSP, "mcp_proxy_for_aws-1.5.0.dist-info"), 0o755); err != nil {
		t.Fatal(err)
	}
	bakedExe := filepath.Join(t.TempDir(), "mcp-proxy-for-aws")
	if err := os.WriteFile(bakedExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := uvToolVersion(bakedExe, "mcp-proxy-for-aws", home); got == nil || got.Version != "1.5.0" {
		t.Errorf("uvToolVersion(baked) = %+v, want 1.5.0", got)
	}
}

// collectToolVersions returns a row per tool without panicking even when none of them are
// installed (a CI runner has no claude and the like — the bins are simply nil). Run inside a
// Workspace container, the -v log shows the raw values of the real probes.
func TestCollectToolVersions(t *testing.T) {
	out := collectToolVersions()
	if len(out) != len(toolSpecs) {
		t.Fatalf("got %d rows, want %d", len(out), len(toolSpecs))
	}
	for i, r := range out {
		if r.Name != toolSpecs[i].Name {
			t.Errorf("row %d name = %q, want %q", i, r.Name, toolSpecs[i].Name)
		}
		t.Logf("%-8s pin=%-8s eff=%v baked=%v local=%v overridden=%v",
			r.Name, r.Pin, binStr(r.Effective), binStr(r.Baked), binStr(r.UserLocal), r.Overridden)
	}
}

func binStr(b *toolBin) string {
	if b == nil {
		return "-"
	}
	return b.Version
}

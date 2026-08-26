package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// ConfigExePath must never hand another program's config a path that will not be
// there later. The incident it exists for: a smoke build at /tmp/af-agent wrote its
// own path into claude's statusLine, was deleted minutes later, and the usage capture
// was dead until the next agent restart.
func TestConfigExePathAvoidsVolatileBinary(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "workspace-agent")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_AGENT_INSTALLED_BIN", installed)

	// The test binary itself runs from the build cache under /tmp — the very shape
	// this guards against — so ConfigExePath must hand back the installed path.
	if got := ConfigExePath(); volatilePath(ExePath()) && got != installed {
		t.Errorf("volatile exe %q: ConfigExePath()=%q want %q", ExePath(), got, installed)
	}

	// Nothing installed → our own path is all there is; a broken config beats no config.
	t.Setenv("AF_AGENT_INSTALLED_BIN", filepath.Join(t.TempDir(), "absent"))
	if got := ConfigExePath(); got != ExePath() {
		t.Errorf("no installed binary: ConfigExePath()=%q want %q", got, ExePath())
	}
}

func TestVolatilePath(t *testing.T) {
	t.Setenv("AF_WS_SCRATCH", "/scratch")
	for _, c := range []struct {
		p    string
		want bool
	}{
		{"/tmp/af-agent", true},
		{"/var/tmp/build/agent", true},
		{"/scratch/agent", true},
		{"/usr/local/bin/workspace-agent", false},
		{"/home/dev/.local/bin/workspace-agent", false},
		{"/tmpfoo/agent", false}, // prefix match must respect the separator
		{"", false},
	} {
		if got := volatilePath(c.p); got != c.want {
			t.Errorf("volatilePath(%q)=%v want %v", c.p, got, c.want)
		}
	}
}

func TestExeUnusable(t *testing.T) {
	dir := t.TempDir()
	// A file that exists outside the volatile roots (the test's own TempDir is under
	// /tmp, which is exactly what this rejects) — /bin/sh is on every host we run on.
	if ExeUnusable("/bin/sh") {
		t.Error("/bin/sh exists and is stable, must be usable")
	}
	if !ExeUnusable(filepath.Join(dir, "gone")) {
		t.Error("a missing binary must be reported unusable")
	}
	if !ExeUnusable("/tmp/af-agent") {
		t.Error("a binary in a volatile dir must be reported unusable (it will be gone)")
	}
	if !ExeUnusable("") {
		t.Error("empty path must be reported unusable")
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// isolateCLIs points every place cliPresent looks (PATH, the baked dir, HOME) at empty temp dirs,
// so a real claude on the machine running the tests cannot make a failing case pass.
func isolateCLIs(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	old := bakedBinDir
	bakedBinDir = t.TempDir()
	t.Cleanup(func() { bakedBinDir = old })
	return home
}

func writeBootStatus(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "install.tsv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := bootInstallStatusPath
	bootInstallStatusPath = p
	t.Cleanup(func() { bootInstallStatusPath = old })
}

func TestCLIInstallRefusalNamesTheReason(t *testing.T) {
	isolateCLIs(t)
	for reason, want := range map[string]string{
		"no_space": "home disk is full", "network": "network error", "checksum": "checksum", "other": "workspace log",
	} {
		writeBootStatus(t, "codex\tfailed\t"+reason+"\n")
		ref := cliInstallRefusal("codex")
		if ref == nil || ref.Code != "cli_install_"+reason || !strings.Contains(ref.Message, want) || !strings.Contains(ref.Message, "codex") {
			t.Errorf("%s: refusal = %+v, want code cli_install_%s naming codex and %q", reason, ref, reason, want)
		}
	}
}

func TestCLIInstallRefusalLeavesEverythingElseAlone(t *testing.T) {
	home := isolateCLIs(t)
	writeBootStatus(t, "codex\tfailed\tno_space\nclaude\tskipped\nopencode\tok\nagy\tfailed\tnetwork\n")
	for _, kind := range []string{
		"claude",   // skipped
		"opencode", // ok
		"copilot",  // not in the status at all
		"kiro",     // a kind with no boot-installed CLI
		"shell",    // not a CLI kind
		"nonsense",
	} {
		if ref := cliInstallRefusal(kind); ref != nil {
			t.Errorf("%s: refused: %+v", kind, ref)
		}
	}
	// A different kind's failure does not block this one (codex failed, claude is asked for).
	if ref := cliInstallRefusal("claude"); ref != nil {
		t.Errorf("claude refused over codex's failure: %+v", ref)
	}

	// A stale failure: the CLI is there now, by each of the three routes cliPresent checks.
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ref := cliInstallRefusal("codex"); ref != nil {
		t.Errorf("codex refused though ~/.local/bin/codex exists: %+v", ref)
	}
	if err := os.WriteFile(filepath.Join(bakedBinDir, "cursor-agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBootStatus(t, "cursor\tfailed\tno_space\n")
	if ref := cliInstallRefusal("cursor"); ref != nil {
		t.Errorf("cursor refused though the baked binary exists: %+v", ref)
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "agy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	writeBootStatus(t, "agy\tfailed\tnetwork\n")
	if ref := cliInstallRefusal("agy"); ref != nil {
		t.Errorf("agy refused though it is on PATH: %+v", ref)
	}
}

func TestCLIInstallRefusalUnknownStatusProceeds(t *testing.T) {
	isolateCLIs(t)
	old := bootInstallStatusPath
	bootInstallStatusPath = filepath.Join(t.TempDir(), "missing", "install.tsv")
	t.Cleanup(func() { bootInstallStatusPath = old })
	if ref := cliInstallRefusal("codex"); ref != nil {
		t.Errorf("refused on an unreadable status: %+v", ref)
	}
}

// End to end through the create handler: the refusal is the answer, with its code, before any
// launch machinery runs; an unaffected kind is not refused by this guard.
func TestCreateSessionRefusesAKindWhoseCLIFailedToInstall(t *testing.T) {
	isolateCLIs(t)
	writeBootStatus(t, "codex\tfailed\tno_space\n")
	req := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"kind":"codex","name":"x"}`))
	w := httptest.NewRecorder()
	sessionx.HandleCreateSession(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cli_install_no_space") {
		t.Fatalf("%d %s, want 409 cli_install_no_space", w.Code, w.Body.String())
	}
}

// Presence follows what a launch would run, not the fixed default name.
func TestCLIInstallRefusalFollowsTheLaunchResolution(t *testing.T) {
	home := isolateCLIs(t)
	writeBootStatus(t, "cursor\tfailed\tno_space\ncopilot\tfailed\tnetwork\nclaude\tfailed\tother\n")

	// A working alternative binary chosen through the launch's own override.
	alt := filepath.Join(t.TempDir(), "my-cursor")
	if err := os.WriteFile(alt, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_CURSOR_BIN", alt)
	if ref := cliInstallRefusal("cursor"); ref != nil {
		t.Errorf("cursor refused though AGENT_CURSOR_BIN names a working binary: %+v", ref)
	}
	t.Setenv("AGENT_CURSOR_BIN", filepath.Join(t.TempDir(), "missing"))
	if ref := cliInstallRefusal("cursor"); ref == nil {
		t.Error("cursor not refused with an AGENT_CURSOR_BIN that points nowhere")
	}
	t.Setenv("AGENT_COPILOT_CMD", "echo hi")
	if ref := cliInstallRefusal("copilot"); ref != nil {
		t.Errorf("copilot refused though AGENT_COPILOT_CMD replaces the launch: %+v", ref)
	}
	t.Setenv("AGENT_SESSION_CMD", "bash")
	if ref := cliInstallRefusal("claude"); ref != nil {
		t.Errorf("claude refused though AGENT_SESSION_CMD replaces the launch: %+v", ref)
	}
	t.Setenv("AGENT_SESSION_CMD", "")

	// Leftovers of a partial install are not a CLI: a directory, a file that is not executable.
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(filepath.Join(bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ref := cliInstallRefusal("claude"); ref == nil {
		t.Error("a directory named claude counted as the CLI")
	}
	if err := os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_CURSOR_BIN", "")
	if ref := cliInstallRefusal("cursor"); ref == nil {
		t.Error("a 0600 leftover counted as the CLI")
	}
}

// The English hint matches the cause: only a full home asks for space.
func TestCLIInstallRefusalHintMatchesTheCause(t *testing.T) {
	isolateCLIs(t)
	for reason, wantFree := range map[string]bool{"no_space": true, "network": false, "checksum": false, "other": false} {
		writeBootStatus(t, "codex\tfailed\t"+reason+"\n")
		ref := cliInstallRefusal("codex")
		if ref == nil || strings.Contains(ref.Message, "Free some space") != wantFree {
			t.Errorf("%s: message %q, want free-space hint = %v", reason, ref.Message, wantFree)
		}
	}
}

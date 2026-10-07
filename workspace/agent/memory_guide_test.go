package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// setAgentMemory writes the ui-prefs switch the way the Console saves it (key agentMemory).
func setAgentMemory(t *testing.T, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	p := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet", "ui-prefs.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"agentMemory":`+v+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// memoryGuideTargets: shared files carry the guidance as a marked block beside the member's own
// text and other AF blocks; owned files are AF's alone.
func memoryGuideTargets() (shared map[string]string, owned map[string]string) {
	return map[string]string{
			"claude":   claude.UserInstructionsPath(),
			"codex":    codex.AgentsPath(),
			"opencode": opencode.AgentsPath(),
			"agy":      agy.AgentsPath(),
			"muse":     muse.AgentsPath(),
		}, map[string]string{
			"copilot": copilot.MemoryGuidePath(),
			"kiro":    kiro.MemoryGuidePath(),
		}
}

// ADR 0108 decision 5: every kind that has a local user layer gets the fixed block while the
// switch is on, and loses exactly it when the switch goes off, with the member's own text before
// and after it untouched, on -> off -> on, and a repeated run changes nothing.
func TestMemoryGuideRoundTripsOnEveryKindWithoutTouchingUserText(t *testing.T) {
	instrEnv(t)
	shared, owned := memoryGuideTargets()
	const before, after = "MY OWN RULES BEFORE\n", "MY OWN RULES AFTER\n"
	for _, p := range shared {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(before+"\n"+after), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reconcileAgentInstructions() // switch absent = off
	baseline := map[string]string{}
	for k, p := range shared {
		baseline[k] = read(t, p)
		if mdblock.Has(baseline[k], "memory-guide") {
			t.Fatalf("%s: guide present with the switch off", k)
		}
	}
	for k, p := range owned {
		if read(t, p) != "" {
			t.Fatalf("%s: guide file present with the switch off", k)
		}
	}

	setAgentMemory(t, true)
	reconcileAgentInstructions()
	onFiles := map[string]string{}
	for k, p := range shared {
		got := read(t, p)
		onFiles[k] = got
		if body, ok := mdblock.Get(got, "memory-guide"); !ok || body != userinstr.MemoryGuide {
			t.Fatalf("%s: guide block missing or altered in %s:\n%s", k, p, got)
		}
		if !strings.Contains(got, "MY OWN RULES BEFORE") || !strings.Contains(got, "MY OWN RULES AFTER") {
			t.Fatalf("%s: user text lost:\n%s", k, got)
		}
	}
	for k, p := range owned {
		if got := read(t, p); got != userinstr.MemoryGuide {
			t.Fatalf("%s: guide file = %q", k, got)
		}
	}
	reconcileAgentInstructions() // idempotent: no growth
	for k, p := range shared {
		if got := read(t, p); got != onFiles[k] {
			t.Fatalf("%s: a second run changed the file:\n%s\n---\n%s", k, onFiles[k], got)
		}
		if n := strings.Count(read(t, p), "<!-- agent-fleet:memory-guide -->"); n != 1 {
			t.Fatalf("%s: %d guide blocks", k, n)
		}
	}

	setAgentMemory(t, false)
	reconcileAgentInstructions()
	for k, p := range shared {
		if got := read(t, p); got != baseline[k] {
			t.Fatalf("%s: off did not restore the file:\n%q\n---\n%q", k, baseline[k], got)
		}
	}
	for k, p := range owned {
		if read(t, p) != "" {
			t.Fatalf("%s: guide file left behind after off", k)
		}
	}

	setAgentMemory(t, true)
	reconcileAgentInstructions()
	for k, p := range shared {
		if got := read(t, p); got != onFiles[k] {
			t.Fatalf("%s: second on differs from the first:\n%q\n---\n%q", k, onFiles[k], got)
		}
	}
	if len(instrErrs) != 0 {
		t.Fatalf("errors: %v", instrErrs)
	}
}

// The guide is its own block: the user's notes (and their per-kind switch) do not carry it, and
// turning the switch off leaves the notes alone.
func TestMemoryGuideIsIndependentOfTheUsersNotes(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("Always report in Japanese.\n"); err != nil {
		t.Fatal(err)
	}
	setAgentMemory(t, true)
	reconcileAgentInstructions()
	setAgentMemory(t, false)
	reconcileAgentInstructions()
	for _, p := range []string{claude.UserInstructionsPath(), codex.AgentsPath(), agy.AgentsPath(), muse.AgentsPath()} {
		got := read(t, p)
		if !strings.Contains(got, "Always report in Japanese.") || strings.Contains(got, "memory-guide") {
			t.Fatalf("%s:\n%s", p, got)
		}
	}
	if !strings.Contains(read(t, copilot.UserInstructionsPath()), "Always report in Japanese.") {
		t.Fatal("copilot user file lost")
	}
}

// A file that holds only the guide block must lose it too (an empty result is still a change).
func TestMemoryGuideRemovalEmptiesAFileThatHeldOnlyTheBlock(t *testing.T) {
	instrEnv(t)
	t.Setenv("AF_WORKSPACE_NOTES", "")
	setAgentMemory(t, true)
	if err := codex.ApplyMemoryGuide(userinstr.MemoryGuide); err != nil {
		t.Fatal(err)
	}
	if err := muse.ApplyMemoryGuide(userinstr.MemoryGuide); err != nil {
		t.Fatal(err)
	}
	if err := codex.ApplyMemoryGuide(""); err != nil {
		t.Fatal(err)
	}
	if err := muse.ApplyMemoryGuide(""); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{codex.AgentsPath(), muse.AgentsPath()} {
		if got := read(t, p); strings.Contains(got, "agent-fleet:memory-guide") {
			t.Fatalf("%s kept the block:\n%s", p, got)
		}
	}
}

// lcpp reads the guidance and the project's index from the system prompt, only while the switch
// is on, and the Console preview (no working copy) never carries memories.
func TestLcppSystemPromptCarriesMemoryOnlyWhileOn(t *testing.T) {
	instrEnv(t)
	if harness.MemoryPrompt == nil {
		t.Fatal("harness.MemoryPrompt is not wired")
	}
	cwd := filepath.Join(os.Getenv("HOME"), "repos", "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	setAgentMemory(t, false)
	if got := harness.SystemPrompt(cwd, "lcpp"); strings.Contains(got, "memory_index") {
		t.Fatalf("guidance in the prompt with the switch off:\n%s", got)
	}
	setAgentMemory(t, true)
	got := harness.SystemPrompt(cwd, "lcpp")
	if !strings.Contains(got, userinstr.MemoryGuide) || !strings.Contains(got, "Project:") {
		t.Fatalf("guidance and index missing with the switch on:\n%s", got)
	}
	if got := harness.SystemPrompt("", "lcpp"); strings.Contains(got, "Project:") {
		t.Fatalf("the preview must not carry an index:\n%s", got)
	}
}

// Saving the Agent memory switch through the Console's ui-prefs route reconciles the guidance
// at once: no restart, no second save.
func TestUIPrefsPutOfAgentMemoryAddsAndRemovesTheGuide(t *testing.T) {
	instrEnv(t)
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("PUT %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	put(`{"agentMemory":true}`)
	if !mdblock.Has(read(t, codex.AgentsPath()), "memory-guide") || read(t, kiro.MemoryGuidePath()) == "" {
		t.Fatal("guide not written after switching memory on")
	}
	put(`{"agentMemory":false}`)
	if mdblock.Has(read(t, codex.AgentsPath()), "memory-guide") || read(t, kiro.MemoryGuidePath()) != "" {
		t.Fatal("guide still present after switching memory off")
	}
}

// A shared file whose guide block lost its end marker is left byte for byte as it was, with the
// error reported, on and off, for every kind that composes the block.
func TestMemoryGuideLeavesADamagedFileAlone(t *testing.T) {
	instrEnv(t)
	shared, _ := memoryGuideTargets()
	damaged := "USER BEFORE\n<!-- agent-fleet:memory-guide -->\nold\nUSER AFTER\n"
	for _, p := range shared {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(damaged), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, on := range []bool{true, false} {
		setAgentMemory(t, on)
		reconcileAgentInstructions()
		for k, p := range shared {
			// Other AF blocks (fleet, rtk) may still be appended after the member's text; what
			// must stay byte for byte is the damaged region and everything before it.
			if got := read(t, p); !strings.HasPrefix(got, damaged) || strings.Count(got, "<!-- agent-fleet:memory-guide -->") != 1 {
				t.Fatalf("%s (on=%v): damaged file rewritten:\n%q", k, on, got)
			}
			if instrErrs[k] == "" {
				t.Errorf("%s (on=%v): damage not reported", k, on)
			}
		}
	}
}

// A linked instruction file keeps its link: on writes the target, off with only the block left
// empties the target, and the link is never replaced or removed (claude included, where the file
// was otherwise deleted when it ended up empty).
func TestMemoryGuideWritesThroughLinkedInstructionFiles(t *testing.T) {
	instrEnv(t)
	shared, _ := memoryGuideTargets()
	store := t.TempDir()
	for k, p := range shared {
		target := filepath.Join(store, k+".md")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, on := range []bool{true, false} {
		setAgentMemory(t, on)
		reconcileAgentInstructions()
		for k, p := range shared {
			if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("%s (on=%v): link replaced or gone (%v)", k, on, err)
			}
			if got := mdblock.Has(read(t, filepath.Join(store, k+".md")), "memory-guide"); got != on {
				t.Fatalf("%s: target has guide = %v, want %v", k, got, on)
			}
			if fi, _ := os.Stat(filepath.Join(store, k+".md")); fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s: mode changed to %v", k, fi.Mode().Perm())
			}
		}
	}
}

package main

import (
	"testing"
)

// The codex-side application (ApplyRTK / stripMarkedBlock) is tested in internal/agents/codex.
// Only the read/write tests for the durable prefs that stay in main live here.

func TestAgentRTKPrefsDefaultOn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// absent file → both default on.
	p := readAgentRTKPrefs()
	if !prefOnDefault(p.Codex) || !prefOnDefault(p.Opencode) {
		t.Fatal("absent prefs should default to on")
	}
	// explicit false persists and reads back false.
	no := false
	if err := writeAgentRTKPrefs(agentRTKPrefs{Codex: &no}); err != nil {
		t.Fatal(err)
	}
	p = readAgentRTKPrefs()
	if prefOnDefault(p.Codex) {
		t.Fatal("explicit codex=false should read back false")
	}
	if !prefOnDefault(p.Opencode) {
		t.Fatal("unset opencode should still default on")
	}
}

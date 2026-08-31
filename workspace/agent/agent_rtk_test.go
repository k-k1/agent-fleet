package main

import (
	"testing"
)

// codex 側の適用（ApplyRTK / stripMarkedBlock）のテストは internal/agents/codex
// へ移設（docs/log/23 残① Wave E）。ここには main 側に残る durable prefs の
// 読み書きテストだけを置く。

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

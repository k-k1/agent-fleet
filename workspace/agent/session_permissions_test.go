package main

// The layer that reads the per-kind default for "skip the permission prompts" out of ui-prefs
// (docs/log/76). Resolution itself belongs to the table test in internal/agents, so only two
// things are checked here: that the prefs shape (agentLaunchDefaults[kind].skipPermissions) is
// readable, and that for a kind whose pending approvals cannot be answered from the Console
// the setting has no effect even when it is written.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSkipPermissionsPref(t *testing.T) {
	writeUIPrefs(t, `{"agentLaunchDefaults":{
		"claude":{"model":"opus","skipPermissions":false},
		"cursor":{"model":""},
		"codex":{"model":"","skipPermissions":false}
	}}`)

	if v, ok := skipPermissionsPref(session.KindClaude); !ok || v {
		t.Errorf("claude: got (%v,%v), want (false,true)", v, ok)
	}
	// A row that exists but carries no skipPermissions counts as unset (the default applies).
	if _, ok := skipPermissionsPref(session.KindCursor); ok {
		t.Error("cursor: a row without skipPermissions must read as unset")
	}
	// codex has no approval path and does not set PermissionChoice, so a false written in
	// prefs is ignored: launching with bypass as before is plainly better than hanging on an
	// approval dialog nobody can answer.
	if _, ok := skipPermissionsPref(session.KindCodex); ok {
		t.Error("codex: a setting for a kind without PermissionChoice must not take effect")
	}
}

func TestSkipPermissionsPrefMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := skipPermissionsPref(session.KindClaude); ok {
		t.Error("with no prefs the result must be unset (the default true lives on the agents side)")
	}
}

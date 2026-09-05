package agents

// The three-layer resolution of "skip the permission prompt?" (docs/log/76). Break it and
// either a launch bypasses permissions the user turned off in settings (their choice never
// happened), or a default launch stalls waiting for approval. Both fail silently, so the
// table pins them.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func boolp(v bool) *bool { return &v }

func TestSkipPermissionsResolution(t *testing.T) {
	cases := []struct {
		name string
		meta session.Meta
		pref map[string]bool // kind -> the ui-prefs default (absent = not configured)
		want bool
	}{
		{"no setting and no override still bypasses", session.Meta{Kind: session.KindClaude}, nil, true},
		{"off through the per-kind default", session.Meta{Kind: session.KindClaude}, map[string]bool{session.KindClaude: false}, false},
		{"another kind's default does not bleed in", session.Meta{Kind: session.KindCursor}, map[string]bool{session.KindClaude: false}, true},
		{"an explicit session setting beats the default (off -> on)",
			session.Meta{Kind: session.KindClaude, SkipPermissions: boolp(true)}, map[string]bool{session.KindClaude: false}, true},
		{"an explicit session setting beats the default (on -> off)",
			session.Meta{Kind: session.KindClaude, SkipPermissions: boolp(false)}, map[string]bool{session.KindClaude: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := SkipPermissionsPref
			t.Cleanup(func() { SkipPermissionsPref = orig })
			SkipPermissionsPref = func(kind string) (bool, bool) {
				v, ok := tc.pref[kind]
				return v, ok
			}
			if got := SkipPermissions(tc.meta); got != tc.want {
				t.Fatalf("SkipPermissions = %v, want %v", got, tc.want)
			}
		})
	}
}

// A plan launch drops the bypass for every kind: auto-approving every tool defeats the
// point of starting in plan mode, even when the user chose to skip prompts. Every kind's
// buildProgram/spawn reads only this one bool, so this is the single place plan is folded
// in.
func TestBypassPermissionsFoldsPlanMode(t *testing.T) {
	orig := SkipPermissionsPref
	t.Cleanup(func() { SkipPermissionsPref = orig })
	SkipPermissionsPref = func(string) (bool, bool) { return false, false } // not configured = default true

	if !BypassPermissions(session.Meta{Kind: session.KindClaude}) {
		t.Error("normal launch: want bypass")
	}
	if BypassPermissions(session.Meta{Kind: session.KindClaude, Mode: "plan"}) {
		t.Error("plan launch: want no bypass")
	}
	if BypassPermissions(session.Meta{Kind: session.KindClaude, Mode: "plan", SkipPermissions: boolp(true)}) {
		t.Error("plan launch with skip=true: want no bypass (plan wins)")
	}
}

package muse

import (
	"errors"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestKindAndCaps(t *testing.T) {
	a := New()
	if a.Kind() != session.KindMuse {
		t.Errorf("Kind() = %q", a.Kind())
	}
	c := a.Caps()
	if !c.ManagedOnly {
		t.Error("ManagedOnly must be true: muse has no pane program at all")
	}
	if !c.PermissionChoice {
		t.Error("PermissionChoice must be true, or POST /sessions refuses skip_permissions=false")
	}
	if !c.CanTranscript {
		t.Error("CanTranscript must be true: Transcript reads a store the item stream fills")
	}
	// A cap is a claim about a path that exists, and the fork path is not built.
	if c.CanFork || c.CanForkAt {
		t.Error("a fork cap was claimed for a path that is not implemented yet")
	}
}

func TestBuildLaunchAlwaysRefuses(t *testing.T) {
	_, err := New().BuildLaunch(session.Meta{Kind: session.KindMuse, Dir: t.TempDir()}, agents.LaunchOpts{})
	if !errors.Is(err, ErrNoTerminalRoute) {
		t.Errorf("err = %v, want ErrNoTerminalRoute", err)
	}
}

// A session that has never spoken has an empty history, not a missing one: ok=false would
// make the read layer fall back as if this kind had no transcript source at all.
func TestTranscriptOfAnUntouchedSessionIsEmptyNotAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	td, ok := New().Transcript(session.Meta{Kind: session.KindMuse, Name: "never-spoke", Dir: t.TempDir()})
	if !ok {
		t.Fatal("Transcript reported no source for a session that simply has no history yet")
	}
	if len(td.Turns) != 0 {
		t.Errorf("%d turns for a session that never spoke", len(td.Turns))
	}
	if td.Path == "" {
		t.Error("Path is empty; the Console shows it as the transcript's location")
	}
}

func TestCapabilitiesDeclareOnlyWhatTheDriverImplements(t *testing.T) {
	c := NewDriver().Capabilities()
	if c.ProcessModel != "per-session-child" {
		t.Errorf("ProcessModel = %q", c.ProcessModel)
	}
	if !c.Steer || !c.DynamicModel || !c.DynamicEffort || !c.Questions {
		t.Errorf("a wired capability is not declared: %+v", c)
	}
	// DynamicMode: MSP has no method that sets AF's plan mode. session/setApprovalMode is a
	// different axis, and reading one wire method as two AF axes is how a capability table
	// starts lying.
	if c.DynamicMode {
		t.Error("DynamicMode must be false: MSP has no plan-mode method")
	}
	// Permissions is AF's approval Interaction kind, which does not exist yet.
	if c.Permissions {
		t.Error("Permissions must stay false until AF has an approval Interaction kind")
	}
	if c.Fork {
		t.Error("Fork must stay false until the fork path is built")
	}
}

func TestResumeRefusesAnotherKind(t *testing.T) {
	_, err := NewDriver().Resume(session.Meta{Kind: session.KindClaude, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("the muse driver accepted a claude session")
	}
}

func TestResumeRefusesAGoneDirectory(t *testing.T) {
	_, err := NewDriver().Resume(session.Meta{Kind: session.KindMuse, Dir: "/nonexistent/muse/dir"})
	if err == nil {
		t.Fatal("the driver accepted a session whose working copy is gone")
	}
}

// The binary is proprietary and not in the image, so a deployment that never ran the
// on-demand install must get a clear refusal rather than a failed spawn.
func TestResumeRefusesWhenTheBinaryIsMissing(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	_, err := NewDriver().Resume(session.Meta{Kind: session.KindMuse, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("the driver accepted a session with no binary installed")
	}
	if !strings.Contains(err.Error(), "install-muse") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

// --- the clamps that ride the environment ------------------------------------

// A clamp whose spelling is wrong is SILENTLY ineffective, so the names are pinned here and
// every one of them was read out of the binary's own string table.
func TestChildEnvCarriesTheEnvironmentClamps(t *testing.T) {
	env := childEnv(nil)
	want := []string{
		"MUSE_NO_AUTO_UPDATE=1",
		// Measured on `muse exec`: with this set the "Including your Claude Code and Codex
		// personal rules" banner disappears and the marker planted in ~/.claude/CLAUDE.md
		// stops reaching the durable log; unset or =0 and both come back.
		"MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=1",
		"MUSE_DISABLE_APPROVAL_JUDGE=1",
		"MUSE_EXPERIMENTAL_SKILL_REMINDER=0",
		"MUSE_EXPERIMENTAL_GOAL_REMINDER=0",
		"MUSE_EXPERIMENTAL_VERIFY_REMINDER=0",
		"MUSE_EXPERIMENTAL_TODO_REMINDER=0",
		"MUSE_EXPERIMENTAL_MEMORY_REMINDER=0",
		"MUSE_EXPERIMENTAL_SCOPE_REMINDER=0",
	}
	for _, w := range want {
		if !hasEnv(env, w) {
			t.Errorf("clamp missing from the child environment: %s", w)
		}
	}
}

// An API key always OVERRIDES a stored account login, which silently moves a member from a
// flat-rate subscription onto metered billing. AF must never put one in the child.
func TestChildEnvNeverCarriesAnAPIKey(t *testing.T) {
	env := childEnv([]string{"PATH=/usr/bin"})
	for _, e := range env {
		if strings.HasPrefix(e, "META_API_KEY=") {
			t.Fatalf("META_API_KEY reached the child: %s", e)
		}
	}
}

// The parent's environment is inherited, not replaced: dropping PATH would leave the host
// unable to run the shell tools that are its whole job.
func TestChildEnvInheritsTheParent(t *testing.T) {
	if !hasEnv(childEnv([]string{"PATH=/usr/bin"}), "PATH=/usr/bin") {
		t.Error("the parent environment was dropped")
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// Both posture flags are asserted rather than assumed. Without --disable-sandbox the failure
// is silent: the tool call fails with "bwrap: Failed to make / slave" while turn/completed
// still says completed, so the session looks healthy and accomplishes nothing.
func TestServeArgsCarryBothPostureFlags(t *testing.T) {
	args := serveArgs()
	if args[0] != "serve" {
		t.Errorf("args[0] = %q", args[0])
	}
	for _, want := range []string{"--disable-sandbox", "--trust-workspace"} {
		found := false
		for _, a := range args {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from %v", want, args)
		}
	}
}

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
)

// A queue delivery's branch is named by the same rules a Console launch from the item gets: the
// default feature/{key}, the member's own template, and nothing when the rules need a title the
// reference does not carry.
func TestQueueBranchNameFollowsTheMembersRules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(home, "repos", "app")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", dir, "remote", "add", "origin", "https://github.com/acme/app.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	old := branchBitbucket
	branchBitbucket = branchrule.NewBitbucketCache(func(context.Context, string, string) ([]byte, error) {
		t.Error("a GitHub origin must not ask Bitbucket")
		return nil, branchrule.ErrNoConnection
	})
	defer func() { branchBitbucket = old }()

	ctx := context.Background()
	check := func(what, provider, key, wantName, wantBase string) {
		t.Helper()
		name, base := queueBranchName(ctx, dir, provider, key)
		if name != wantName || base != wantBase {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", what, name, base, wantName, wantBase)
		}
	}
	// No template of the member's: the built-in rule, as decision 12 of ADR 0061 has it.
	check("default, GitHub", "github", "acme/app#12", "feature/12", "main")
	check("default, Jira", "jira", "PROJ-7", "feature/PROJ-7", "main")

	writeHomeUIPrefs(t, home, `{"workItemBranchTemplate":"queue/{num}"}`)
	check("custom template", "github", "acme/app#12", "queue/12", "main")

	// A template that needs the title's slug has none to use: no name, so the throwaway branch.
	writeHomeUIPrefs(t, home, `{"workItemBranchTemplate":"{prefix}{slug}"}`)
	check("template that needs a slug", "github", "acme/app#12", "", "")
}

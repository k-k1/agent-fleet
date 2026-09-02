package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// applyGitIdentity bakes the provider identity into a repo's local config, and a manual
// override is never clobbered by a reapply.
func TestApplyGitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := filepath.Join(reposRoot(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://github.com/acme/app.git").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v: %s", err, out)
	}

	if h := remoteHost(dir); h != "github.com" {
		t.Fatalf("remoteHost = %q; want github.com", h)
	}

	// Provider identity in the store → baked into local config as source=provider.
	s := &secrets.Data{Git: map[string]secrets.GitEntry{}, GitIdentity: map[string]secrets.GitIdentity{
		"github.com": {Name: "Dev", Email: "dev@acme.io"},
	}}
	if err := s.Save(); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	applyGitIdentity(dir)
	if got := gitConfigLocalGet(dir, "user.name"); got != "Dev" {
		t.Fatalf("user.name = %q; want Dev", got)
	}
	if got := gitConfigLocalGet(dir, "user.email"); got != "dev@acme.io" {
		t.Fatalf("user.email = %q; want dev@acme.io", got)
	}
	if got := gitConfigLocalGet(dir, identitySourceKey); got != "provider" {
		t.Fatalf("source = %q; want provider", got)
	}

	// Manual override wins and survives a reapply even after the provider changes.
	gitConfigLocalSet(dir, "user.name", "Me")
	gitConfigLocalSet(dir, identitySourceKey, "manual")
	s.GitIdentity["github.com"] = secrets.GitIdentity{Name: "Other", Email: "other@acme.io"}
	_ = s.Save()
	reapplyProviderIdentity("github.com")
	if got := gitConfigLocalGet(dir, "user.name"); got != "Me" {
		t.Fatalf("manual override clobbered: user.name = %q; want Me", got)
	}
}

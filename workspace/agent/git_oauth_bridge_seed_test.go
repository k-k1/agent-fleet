package main

// git_oauth_bridge_seed_test.go — regression for `seedGitOAuthBridge`.
//
// The implementation lives in package main (cred_helper.go). This one case stays here,
// apart from `internal/gitx/git_oauth_bridge_test.go`, so that it keeps driving the real
// thing: moved into gitx it would call a main function that reads env and writes secrets as
// an injected function value, and the very property this test pins — env is read exactly
// once at startup — would drop out of the check. The other three cases (refresh through the
// bridge) are on the gitx side.

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// withAgentHome is a copy of the identically named helper in gitx.
func withAgentHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "") // plaintext store: this is about the fields, not the seal
}

// seedGitOAuthBridge copies env into the store at startup because the cred helper is a
// separate process. It must be idempotent, and it must CLEAR a stale bridge — a
// deployment that dropped PUBLIC_BASE_URL would otherwise leave workspaces pointing at
// an endpoint that no longer answers.
func TestSeedGitOAuthBridgeIsIdempotentAndClears(t *testing.T) {
	withAgentHome(t)
	t.Setenv("AF_CP_BASE_URL", "https://af.example/")
	t.Setenv("AF_GIT_OAUTH_TOKEN", "afo_tok")
	seedGitOAuthBridge()
	seedGitOAuthBridge() // idempotent

	s, err := secrets.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.GitOAuthBridge == nil || s.GitOAuthBridge.BaseURL != "https://af.example" || s.GitOAuthBridge.Token != "afo_tok" {
		t.Fatalf("bridge: %+v", s.GitOAuthBridge)
	}

	os.Unsetenv("AF_GIT_OAUTH_TOKEN")
	seedGitOAuthBridge()
	s, _ = secrets.Load()
	if s.GitOAuthBridge != nil {
		t.Fatalf("an unset token must clear the stored bridge, got %+v", s.GitOAuthBridge)
	}
}

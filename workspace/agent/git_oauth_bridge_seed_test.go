package main

// git_oauth_bridge_seed_test.go — `seedGitOAuthBridge` の回帰。
//
// 本体（cred_helper.go）は package main に居るので、AG-GIT の移送でも動いていない。
// このテストだけが `internal/gitx/git_oauth_bridge_test.go` から分かれてここに残った
// ——**駆動を変えないため**である。gitx へ持って行くと、env を読んで secrets へ書く
// main の関数を「関数値で注入されたもの」として呼ぶことになり、
// 「起動時に env を 1 度だけ読む」という、このテストが押さえている性質そのものが
// 検査から外れる。残りの 3 本（bridge 経由の refresh）は gitx 側にある。

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// withAgentHome は gitx 側の同名ヘルパの写し（3 行・分割で 2 パッケージに分かれた）。
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

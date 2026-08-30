package copilot

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// copilot の認証は GitHub 連携相乗り（docs/log/36 契約）: Copilot CLI は
// COPILOT_GITHUB_TOKEN > GH_TOKEN > GITHUB_TOKEN と「gh CLI アプリの OAuth
// トークン」を公式サポートし、実測ではこの Workspace の gh 透過認証だけで
// 追加ログインなしに動いた。専用の start/complete フローは持たず、Status()
// は gh のトークン有無（＝GitHub 連携の有無）と CLI バイナリの有無を返す。

// Status is the `copilot` field of GET /connections. githubConnected is the
// caller's (connections.go) view of the GitHub git connection — the same store
// the gh transparent-auth wrapper serves tokens from. supported=false hides the
// kind in the Console (registry の available ゲート — agy と同じ配線):
// バイナリ無し = 旧イメージ。
func Status(githubConnected bool) map[string]any {
	m := map[string]any{"connected": githubConnected}
	if _, err := exec.LookPath("copilot"); err != nil {
		m["supported"] = false
		m["reason"] = "not_installed"
		return m
	}
	m["supported"] = true
	return m
}

// Token returns the gh transparent-auth OAuth token for explicit injection into
// the managed child's env（COPILOT_GITHUB_TOKEN）. The TUI route relies on
// copilot's own gh fallback（実測で動作）— this is for the ACP child, whose env
// we control deterministically. Cached briefly: Resume/reconcile bursts
// shouldn't spawn a gh process per session. "" when gh has no token.
var tokenMu sync.Mutex
var tokenAt time.Time
var tokenVal string

func Token() string {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if time.Since(tokenAt) < time.Minute {
		return tokenVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		// stale-if-error: a transient gh failure shouldn't drop auth for a child
		// spawn that follows; the cache just stays what it was.
		return tokenVal
	}
	tokenVal = strings.TrimSpace(string(out))
	tokenAt = time.Now()
	return tokenVal
}

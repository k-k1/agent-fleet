package copilot

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// copilot rides on the GitHub connection for authentication (docs/log/36 contract): the
// Copilot CLI officially supports COPILOT_GITHUB_TOKEN > GH_TOKEN > GITHUB_TOKEN plus the
// gh CLI app's OAuth token, and measured, this Workspace's gh transparent auth alone was
// enough to run it with no extra sign-in. There is no dedicated start/complete flow;
// Status() reports whether gh has a token (i.e. whether the GitHub connection exists) and
// whether the CLI binary is present.

// Status is the `copilot` field of GET /connections. githubConnected is the
// caller's (connections.go) view of the GitHub git connection — the same store
// the gh transparent-auth wrapper serves tokens from. supported=false hides the
// kind in the Console (the registry's available gate, wired like agy): no binary means an
// old image.
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
// the managed child's env (COPILOT_GITHUB_TOKEN). The TUI route relies on
// copilot's own gh fallback (measured to work) — this is for the ACP child, whose env
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

package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Persistence of the account display info (email/plan). agy's token file carries no
// identity (only access/refresh token and auth_method), so on successful auth the
// "email (plan)" line of the main-screen header is scraped and saved, and GET
// /connections' Status() reads it back. Display-only and best-effort — without it
// AgyCard simply degrades. Not secret, but it is kept alongside the existing stores
// under ~/.config/agent-fleet, which the denylist covers.

type accountInfo struct {
	Email string `json:"email"`
	Plan  string `json:"plan,omitempty"`
}

func accountPath() string {
	return filepath.Join(paths.AgentConfigDir(), "agy-account.json")
}

// captureAccount scrapes the main-screen identity line ("email (plan)" —
// the same line usage.go's planRe matches) from the flow output and persists it. The plan
// suffix can render a few seconds after the main screen, so poll briefly.
func captureAccount(f *agents.Flow, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := planRe.FindStringSubmatch(f.Clean()); m != nil {
			saveAccount(m[1], m[2])
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func saveAccount(email, plan string) {
	if email == "" {
		return
	}
	b, err := json.MarshalIndent(accountInfo{Email: email, Plan: plan}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(paths.AgentConfigDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(accountPath(), append(b, '\n'), 0o600)
}

func loadAccount() (email, plan string) {
	b, err := os.ReadFile(accountPath())
	if err != nil {
		return "", ""
	}
	var a accountInfo
	if json.Unmarshal(b, &a) != nil {
		return "", ""
	}
	return a.Email, a.Plan
}

func removeAccount() { _ = os.Remove(accountPath()) }

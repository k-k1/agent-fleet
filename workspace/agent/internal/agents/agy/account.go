package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// アカウント表示情報（email/plan）の永続化。agy の token ファイルは identity を
// 持たない（access/refresh token と auth_method のみ）ので、認証完了時にメイン
// 画面ヘッダの「email (plan)」行をスクレイプして保存し、GET /connections の
// Status() が読む。表示専用・best-effort — 無くても AgyCard は劣化表示で受ける
// （Track C 連絡事項）。秘匿情報ではないが、置き場所は denylist 配下の
// ~/.config/agent-fleet に既存ストアと揃える。

type accountInfo struct {
	Email string `json:"email"`
	Plan  string `json:"plan,omitempty"`
}

func accountPath() string {
	return filepath.Join(paths.AgentConfigDir(), "agy-account.json")
}

// captureAccount scrapes the main-screen identity line ("email (plan)" —
// usage.go の planRe と同じ行) from the flow output and persists it. The plan
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

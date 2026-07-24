package kiro

// kiro の認証は Builder ID / device-flow 型（docs/43 §2.2）。資格情報は
// `~/.local/share/kiro-cli/data.sqlite3`（600・auth_kv テーブル）。ここでは Status() を
// 出す: `kiro-cli whoami -f json` はクリーンな構造化 JSON を返し（`{accountType,email,
// region,startUrl}`）、未認証は exit 1。対話ログイン（device flow start→poll・stdout
// スクレイプ）と切断ルートは Track C（CP ルート）で足す（cursor の Track A も Status()
// のみで、login フロー配線は Track C だった）。

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// whoamiOut is the shape of `kiro-cli whoami -f json`（実測 2.14.1）。
type whoamiOut struct {
	AccountType string `json:"accountType"` // "BuilderId" | …
	Email       string `json:"email"`
}

// statusResult caches the last successful probe (email + logged-in).
type statusResult struct {
	loggedIn bool
	email    string
}

// Status is the `kiro` field of GET /connections. supported=false hides the kind in
// the Console（registry の available ゲート — cursor/copilot と同じ配線）: バイナリ
// 無し = 未導入（オンデマンド導入前）。connected は実ログイン状態（whoami exit 0）。
// probe は ~30s キャッシュして connections poll ごとの子プロセス起動を避ける。
func Status() map[string]any {
	if _, err := exec.LookPath(bin()); err != nil {
		return map[string]any{"connected": false, "supported": false, "reason": "not_installed"}
	}
	m := map[string]any{"supported": true}
	st := probeStatus()
	m["connected"] = st.loggedIn
	if st.email != "" {
		m["email"] = st.email
	}
	return m
}

var statusMu sync.Mutex
var statusAt time.Time
var statusVal statusResult

const statusTTL = 30 * time.Second

func probeStatus() statusResult {
	statusMu.Lock()
	defer statusMu.Unlock()
	if !statusAt.IsZero() && time.Since(statusAt) < statusTTL {
		return statusVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "whoami", "-f", "json").Output()
	if err != nil {
		// Not logged in (exit 1) or a transient error. Distinguish: an empty prior value
		// means we've never seen a login, so report logged-out; otherwise keep the last
		// good value (stale-if-error) rather than flapping the connection chip.
		if statusAt.IsZero() {
			statusVal = statusResult{}
			statusAt = time.Now()
		}
		return statusVal
	}
	var wo whoamiOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &wo) == nil {
		statusVal = statusResult{loggedIn: true, email: wo.Email}
		statusAt = time.Now()
	}
	return statusVal
}

// LoggedIn reports (cached, ~30s) whether kiro-cli has an authenticated session.
// Guards on the binary being present so a missing CLI reads as unavailable rather
// than paying a failed exec each call.
func LoggedIn() bool {
	if _, err := exec.LookPath(bin()); err != nil {
		return false
	}
	return probeStatus().loggedIn
}

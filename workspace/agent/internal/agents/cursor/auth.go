package cursor

// cursor の認証は専用フロー型（docs/40 契約・claude/agy と同型）。資格情報は
// ~/.config/cursor/auth.json（600・accessToken/refreshToken 平文 JSON — 実測）。
// 対話ログインは `NO_OPEN_BROWSER=1 cursor-agent login`（URL 標準出力）で、
// start/complete 連携は Track C（CP ルート）で足す。ここでは Status() を出す:
// `cursor-agent status --format json` はクリーンな構造化 JSON を返す（実測）。

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// statusOut is the shape of `cursor-agent status --format json`（実測 v2026.07.20）。
type statusOut struct {
	Status          string `json:"status"` // "authenticated" | …
	IsAuthenticated bool   `json:"isAuthenticated"`
	UserInfo        struct {
		Email string `json:"email"`
	} `json:"userInfo"`
}

// Status is the `cursor` field of GET /connections. supported=false hides the kind
// in the Console（registry の available ゲート — copilot/agy と同じ配線）:
// バイナリ無し = 旧イメージ。connected は実ログイン状態（status --format json）。
// probe は ~30s キャッシュして connections poll ごとの子プロセス起動を避ける。
func Status() map[string]any {
	if _, err := exec.LookPath(bin()); err != nil {
		return map[string]any{"connected": false, "supported": false, "reason": "not_installed"}
	}
	m := map[string]any{"supported": true}
	st := probeStatus()
	m["connected"] = st.IsAuthenticated
	if st.UserInfo.Email != "" {
		m["email"] = st.UserInfo.Email
	}
	return m
}

var statusMu sync.Mutex
var statusAt time.Time
var statusVal statusOut

const statusTTL = 30 * time.Second

func probeStatus() statusOut {
	statusMu.Lock()
	defer statusMu.Unlock()
	if !statusAt.IsZero() && time.Since(statusAt) < statusTTL {
		return statusVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), disableAutoUpdateFlag, "status", "--format", "json").Output()
	if err != nil {
		// stale-if-error: 一時失敗で接続状態を落とさない（前回値を維持）。
		return statusVal
	}
	var st statusOut
	if json.Unmarshal([]byte(strings.TrimSpace(string(out))), &st) == nil {
		statusVal = st
		statusAt = time.Now()
	}
	return statusVal
}

package main

// 認証切れ（docs/47 §4-8）の配線: 資格情報が切れているワークスペースでは、claude の
// セッションは「入力待ち」ではなく認証切れとして読まれ、自由文の送信は断られる。
//
// 分類そのもの（2 つの epoch → 生死）は internal/agents/claude の単体テストが押さえる。
// ここで見るのは配線: 待機中に見えるペインでも状態が auth になること、送信ガードが
// その状態を auth_expired として断ること、そして**生きている資格情報では何も変わらない**
// こと（誤検知側は「動いているセッションを全部止める」ので、同じ強さで固定する）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// writeClaudeCreds は隔離した CLAUDE_CONFIG_DIR に資格情報を置く（実フリートのものは
// 読まない）。access / refresh は now からの相対。
func writeClaudeCreds(t *testing.T, access, refresh time.Duration) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	now := time.Now()
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken":           "tok",
		"expiresAt":             now.Add(access).UnixMilli(),
		"refreshTokenExpiresAt": now.Add(refresh).UnixMilli(),
	}})
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if want := refresh <= 0 && access <= 0; claude.AuthExpired() != want {
		t.Fatalf("前提が作れていない: AuthExpired = %v, want %v", claude.AuthExpired(), want)
	}
}

// TestDriveStateAuthExpired: 待機プロンプトのペイン（＝一見 入力待ち）でも、資格情報が
// 切れていれば auth を返し、走りっぱなしに見えていたマーカーは畳まれること。
func TestDriveStateAuthExpired(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	m := paneShowing(t, "authexp1", "internal/tmuxx/testdata/footers/idle_bypass_hint.txt")
	sid := session.UUID(m.Dir, m.Name)
	// 401 でターンが死んだ直後の形: working のまま Stop hook は鳴っていない。
	status.Persist(sid, "working")

	if got := driveState(m, true, true); got != agents.StateAuth {
		t.Fatalf("driveState = %q, want %q（認証切れは 入力待ち ではない）", got, agents.StateAuth)
	}
	if st, ok := status.Read(sid); ok && st.State == "working" {
		t.Error("status marker が working のまま — 自己修復が走っていない")
	}
	if got := driveState(m, true, true); got != agents.StateAuth {
		t.Errorf("2 回目の driveState = %q, want %q（状態が振動している）", got, agents.StateAuth)
	}
}

// TestDriveStateAuthValid: 生きている資格情報では従来どおり（待機ペインは 入力待ち）。
func TestDriveStateAuthValid(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, 8*time.Hour, 25*24*time.Hour)
	m := paneShowing(t, "authexp2", "internal/tmuxx/testdata/footers/idle_bypass_hint.txt")
	if got := driveState(m, true, true); got == agents.StateAuth {
		t.Fatalf("driveState = %q — 生きている資格情報を認証切れと誤判定している", got)
	}
}

// TestPromptBlockerAuthExpired: 送信ガード。認証切れのセッションへ自由文を送ると
// auth_expired で断られること（TUI は文字を受け取るがターンは始まらないので、断らないと
// 送信側からは成功に見え、ミラーには反映待ちのプロンプトだけが残る）。
func TestPromptBlockerAuthExpired(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, -8*24*time.Hour+8*time.Hour, -8*24*time.Hour)
	m := session.Meta{Name: "authexp3", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)

	st := promptBlocker(m.Name)
	if st != agents.StateAuth {
		t.Fatalf("promptBlocker = %q, want %q", st, agents.StateAuth)
	}
	if code := blockedErrCode(st); code != "auth_expired" {
		t.Errorf("blockedErrCode = %q, want auth_expired（Console の err.<code> と CP の分類が見る値）", code)
	}
	if blockedErrMessage(st) == "" {
		t.Error("blockedErrMessage が空 — 断った理由が誰にも伝わらない")
	}
}

// TestPromptBlockerAuthValid: 生きている資格情報では送信を塞がない。
func TestPromptBlockerAuthValid(t *testing.T) {
	isolateAgentState(t)
	writeClaudeCreds(t, 8*time.Hour, 25*24*time.Hour)
	m := session.Meta{Name: "authexp4", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	if st := promptBlocker(m.Name); st == agents.StateAuth {
		t.Fatalf("promptBlocker = %q — 生きている資格情報で送信を断っている", st)
	}
}

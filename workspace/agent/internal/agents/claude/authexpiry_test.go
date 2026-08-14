package claude

// 資格情報の期限判定（docs/47 §4-8）。分類そのもの（2 つの epoch → 生きている/切れた）
// と、ファイルから読む配線の両方を押さえる。
//
// 誤検知の実害が非対称なので、境界は「切れていないと言う側」に寄せてある: 切れて
// いないのに切れたと言えば、動いているセッションの送信を全部断ってしまう。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ms(t time.Time) int64 { return t.UnixMilli() }

func credsAt(access, refresh time.Time) credsFile {
	var c credsFile
	c.ClaudeAiOauth.AccessToken = "sk-ant-oat01-dummy"
	c.ClaudeAiOauth.ExpiresAt = ms(access)
	c.ClaudeAiOauth.RefreshTokenExpiresAt = ms(refresh)
	return c
}

func TestExpiryClassification(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	cases := []struct {
		name         string
		access       time.Time
		refresh      time.Time
		envToken     bool
		wantKnown    bool
		wantDead     bool
		wantSoon     bool
		wantDaysLeft int
	}{
		{
			name: "通常（残り25日）", access: now.Add(8 * time.Hour), refresh: now.Add(25 * day),
			wantKnown: true, wantDaysLeft: 25,
		},
		{
			// CLI が起動時ヒントを出す条件そのもの（残り 1 日以下）。Console は 3 日前から出す。
			name: "残り1日", access: now.Add(2 * time.Hour), refresh: now.Add(20 * time.Hour),
			wantKnown: true, wantSoon: true, wantDaysLeft: 1,
		},
		{
			name: "残り3日ちょうど", access: now.Add(time.Hour), refresh: now.Add(3 * day),
			wantKnown: true, wantSoon: true, wantDaysLeft: 3,
		},
		{
			// refresh は切れたがアクセストークンはまだ生きている: 更新はもうできないが、
			// このトークンが切れるまでターンは走る。まだ dead と名乗ってはいけない（送信を
			// 断ると、まだ動くセッションを止めることになる）。ただし猶予は数時間なので、
			// カードの予告としては「まもなく」— Soon は立てる。
			name: "refresh 切れ・access 生存", access: now.Add(3 * time.Hour), refresh: now.Add(-time.Hour),
			wantKnown: true, wantSoon: true, wantDaysLeft: 0,
		},
		{
			// 実際に起きる形: 最後に発行されたアクセストークン（refresh 期限の直前に
			// 取れたもの）も切れている。
			name: "両方切れ", access: now.Add(-8*day + 8*time.Hour), refresh: now.Add(-8 * day),
			wantKnown: true, wantDead: true,
		},
		{
			// 環境変数のトークンで走っているときは、この資格情報ファイルは使われない。
			name: "env トークン運転", access: now.Add(-8*day + 8*time.Hour), refresh: now.Add(-8 * day),
			envToken: true,
		},
		{
			// claude 自身が判断を降りる形（access が refresh + 3d より先）。
			name: "サブスク OAuth の形ではない", access: now.Add(30 * day), refresh: now.Add(2 * day),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := expiryOf(credsAt(c.access, c.refresh), c.envToken)
			if e.Known != c.wantKnown {
				t.Fatalf("Known = %v, want %v", e.Known, c.wantKnown)
			}
			if got := e.Dead(now); got != c.wantDead {
				t.Errorf("Dead = %v, want %v", got, c.wantDead)
			}
			if got := e.Soon(now); got != c.wantSoon {
				t.Errorf("Soon = %v, want %v", got, c.wantSoon)
			}
			if c.wantKnown {
				if got := e.DaysLeft(now); got != c.wantDaysLeft {
					t.Errorf("DaysLeft = %d, want %d", got, c.wantDaysLeft)
				}
			}
		})
	}
}

// refreshTokenExpiresAt を持たないレコード（claude 自身も同じ条件で判断を降りる）は
// 「切れていない」ではなく「分からない」— 判定に使わせない。
func TestExpiryNoRefreshDeadline(t *testing.T) {
	var c credsFile
	c.ClaudeAiOauth.AccessToken = "x"
	c.ClaudeAiOauth.ExpiresAt = ms(time.Now().Add(-time.Hour))
	if e := expiryOf(c, false); e.Known || e.Dead(time.Now()) {
		t.Fatalf("Known=%v Dead=%v — 材料が無いのに判断している", e.Known, e.Dead(time.Now()))
	}
}

// writeCredsExpiry は隔離した CLAUDE_CONFIG_DIR に資格情報ファイルを置く。
func writeCredsExpiry(t *testing.T, dir string, access, refresh time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"rt","expiresAt":%d,`+
		`"refreshTokenExpiresAt":%d,"subscriptionType":"max"}}`, ms(access), ms(refresh))
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	resetCredCache()
}

// isolateClaudeConfig points ConfigDir() at a temp dir. 実フリートの資格情報を読まない
// ため（テストが実 CLI の設定を触る事故はこのリポジトリで一度起きている）。
func isolateClaudeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	resetCredCache()
	t.Cleanup(resetCredCache)
	return dir
}

func TestCredentialExpiryFromFile(t *testing.T) {
	dir := isolateClaudeConfig(t)

	// ファイルが無い＝未接続。判断材料が無いので Known=false（＝送信を断らない）。
	if e := CredentialExpiry(); e.Known {
		t.Fatalf("資格情報が無いのに Known=true: %+v", e)
	}
	if AuthExpired() {
		t.Fatal("資格情報が無いだけで認証切れを名乗っている")
	}

	now := time.Now()
	writeCredsExpiry(t, dir, now.Add(-10*24*time.Hour+8*time.Hour), now.Add(-10*24*time.Hour))
	if !AuthExpired() {
		t.Fatal("両方切れた資格情報を生きていると判定している")
	}

	// 再認証で書き戻された資格情報がすぐ効くこと（stat キャッシュが張り付かない）。
	writeCredsExpiry(t, dir, now.Add(8*time.Hour), now.Add(30*24*time.Hour))
	if AuthExpired() {
		t.Fatal("再認証後も認証切れのまま — キャッシュが古い判定を保持している")
	}
	if got := oauthToken(); got != "tok" {
		t.Errorf("oauthToken = %q, want %q（同じ読みを共有していること）", got, "tok")
	}
}

package claude

// 資格情報の期限切れを**落ちる前に**見つける（docs/47 §4-8）。
//
// §4-7 の検出は事後のもの: ターンが 401 で死んで転写に合成レコードが残って初めて
// 「認証切れだった」と分かる。落ちる前にも、落ちた後にも、状態としては何も出ない。
//
// CLI 自身の警告も当てにできない。2.1.231 のバイナリを読むと、起動時ヒント
// （id="oauth-expiry-warning" — スクショの「Your login expires in 1 day · run /login
// to renew」）はこう決まっている:
//
//	if (apiProvider !== "firstParty" || !oauthLogin) return null
//	if (typeof refreshTokenExpiresAt !== "number") return null
//	if (expiresAt > refreshTokenExpiresAt + 3d) return null
//	const left = refreshTokenExpiresAt - now
//	if (left > 3d || left <= 0) return null          // ← 期限切れ後は null
//	return { daysLeft: ceil(left / 1d) }             // 描くのは daysLeft <= 1 のときだけ
//
// つまり **切れた後の TUI には何の痕跡も残らない**。残るのは「入力待ちに見えるのに
// 送ったプロンプトが反映待ちのまま動かないセッション」だけで、Console からは原因が
// 分からない（実測・利用者報告 2026-08-14）。
//
// 材料は claude 自身が見ているのと同じ 2 つの epoch(ms)。トークン本体は読まない
// （値をログにも DTO にも載せない — 出すのは時刻と真偽だけ）:
//
//	claudeAiOauth.expiresAt             アクセストークンの期限（実測 約8時間）
//	claudeAiOauth.refreshTokenExpiresAt リフレッシュトークンの期限（実測 約30日）
//
// アクセストークンは refresh で伸びるが、refresh が切れたらもう伸ばせない。よって
// 「確実に死んでいる」と言えるのは**両方**が過ぎたときだけで、refresh だけ切れて
// いる間はまだ最後のアクセストークンで動く。Console のバッジと送信ガードはこの
// 厳しい側（両方切れ）を使い、設定カードの予告は refresh の期限を使う。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// warnWindow は「そろそろ切れる」と見なす残り時間。claude が期限を語る気になる窓
// （上記 expiresAt > refresh + 3d の 3d）に合わせてある。CLI が実際に 1 行出すのは
// 残り 1 日以下・しかも 15 秒で消える起動ヒントなので、Console 側はもう少し早くから
// 静かに出す — 消えない場所に出せるのが Console の取り柄で、気づく余裕がその差。
const warnWindow = 3 * 24 * time.Hour

// Expiry is what the credentials file says about how long this login has left.
// Known=false は「判断材料が無い」で、**期限切れではない**: 未接続・APIキー運転・
// ファイル形式が変わった、のいずれか。材料が無いときに切れていると名乗るのは、
// 動いているセッションを止める誤検知になる。
type Expiry struct {
	Known   bool
	Access  time.Time // claudeAiOauth.expiresAt
	Refresh time.Time // claudeAiOauth.refreshTokenExpiresAt（＝サインインし直す期限）
}

// Dead reports that no valid token remains: refresh も access も過ぎている。この
// ときだけセッションを「認証切れ」と名乗らせ、自由文の送信を断る。
func (e Expiry) Dead(now time.Time) bool {
	if !e.Known {
		return false
	}
	end := e.Refresh
	if e.Access.After(end) {
		end = e.Access
	}
	return !now.Before(end)
}

// Soon reports that the login is inside warnWindow of its renewal deadline (but not
// dead yet) — 設定カードの「あと N 日で期限切れ」用。
func (e Expiry) Soon(now time.Time) bool {
	if !e.Known || e.Dead(now) {
		return false
	}
	return e.Refresh.Sub(now) <= warnWindow
}

// DaysLeft is the whole days left until the renewal deadline, rounded up the way
// claude rounds its own banner (ceil). 0 = 今日中に切れる / もう切れている。
func (e Expiry) DaysLeft(now time.Time) int {
	if !e.Known {
		return 0
	}
	left := e.Refresh.Sub(now)
	if left <= 0 {
		return 0
	}
	return int((left + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
}

// credsFile is the shape we read out of claude's .credentials.json. AccessToken is
// here only because oauthToken() (usage.go) needs the same file — nothing else in
// this package may touch it, and it must never leave the process.
type credsFile struct {
	ClaudeAiOauth struct {
		AccessToken           string `json:"accessToken"`
		ExpiresAt             int64  `json:"expiresAt"`             // ms epoch
		RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"` // ms epoch
	} `json:"claudeAiOauth"`
}

// expiryOf is the pure decision over one parsed credentials file — the part the tests
// pin. env は「環境変数のトークンで走っている」かどうか（下の CredentialExpiry が
// 実環境から渡す）。
//
// 判断しない（Known=false）ケース:
//   - 環境変数のトークン運転（CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY）— この
//     ファイルは使われないので、古いまま残っていても意味を持たない。
//   - refreshTokenExpiresAt が無い（0）— claude 自身も同じ条件で判断を降りている。
//   - access が refresh + 3d より先 — 通常のサブスク OAuth の形ではない（CLI の
//     null 条件そのまま）。憶測で切れ扱いにしない。
func expiryOf(c credsFile, envToken bool) Expiry {
	o := c.ClaudeAiOauth
	if envToken || o.RefreshTokenExpiresAt <= 0 {
		return Expiry{}
	}
	e := Expiry{
		Known:   true,
		Access:  time.UnixMilli(o.ExpiresAt),
		Refresh: time.UnixMilli(o.RefreshTokenExpiresAt),
	}
	if o.ExpiresAt > 0 && e.Access.After(e.Refresh.Add(warnWindow)) {
		return Expiry{}
	}
	return e
}

func credsPath() string { return filepath.Join(ConfigDir(), ".credentials.json") }

// envToken reports whether a token in the environment overrides the credentials file.
// claude reads CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY ahead of its own OAuth
// record, and sessions inherit the agent's environment, so a stale file must not be
// allowed to declare those sessions dead.
func envToken() bool {
	return os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || os.Getenv("ANTHROPIC_API_KEY") != ""
}

// 資格情報は「セッション一覧のポーリング × セッション数」で読まれるので、内容は
// stat（サイズ + mtime）が変わるまで使い回す。TTL ではなく stat にしているのは、
// 再認証した直後に古い判定が数十秒残ると「直したのに認証切れのまま」に見えるから。
var (
	credMu    sync.Mutex
	credKey   string // size|modtime
	credCache credsFile
	credOK    bool
)

// readCreds returns the parsed credentials file, cached on its stat. ok=false when the
// file is absent or unparsable.
func readCreds() (credsFile, bool) {
	p := credsPath()
	key := ""
	if st, err := os.Stat(p); err == nil {
		// パスも鍵に含める: CLAUDE_CONFIG_DIR は差し替わりうる（テストの隔離、将来の
		// 切替）ので、mtime+size だけだと別ファイルが偶然一致したときに前の判定が残る。
		key = p + "|" + st.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(st.Size(), 10)
	}
	credMu.Lock()
	defer credMu.Unlock()
	if key != "" && key == credKey {
		return credCache, credOK
	}
	credKey, credCache, credOK = key, credsFile{}, false
	b, err := os.ReadFile(p)
	if err != nil {
		return credCache, credOK
	}
	var c credsFile
	if json.Unmarshal(b, &c) != nil {
		return credCache, credOK
	}
	credCache, credOK = c, true
	return credCache, credOK
}

// CredentialExpiry reports what the local OAuth record says about this login's
// remaining life. Known=false when there is nothing to judge (see expiryOf).
func CredentialExpiry() Expiry {
	c, ok := readCreds()
	if !ok {
		return Expiry{}
	}
	return expiryOf(c, envToken())
}

// AuthExpired is the one-call form used by the live-state code and the send guard:
// this workspace's claude login can no longer run a turn.
//
// ワークスペース単位の事実（資格情報はコンテナに 1 つ）なので、claude のセッション
// 全部が同時にこれを名乗る。セッション毎の状態ではないが、利用者が見るのはセッション
// の行なので、そこに出さないと気づけない。
func AuthExpired() bool { return CredentialExpiry().Dead(time.Now()) }

// resetCredCache drops the stat cache. 再認証／切断の直後は同じ stat のまま中身だけ
// 差し替わる可能性がある（同じ秒に同じサイズで書き戻る）ので、その 2 経路だけは
// 明示的に捨てる。
func resetCredCache() {
	credMu.Lock()
	credKey, credCache, credOK = "", credsFile{}, false
	credMu.Unlock()
}

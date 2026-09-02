package chatx

// 移送に伴い、main のテストヘルパのうち**この家系だけが使っていて、かつ純粋なもの**を
// こちら側に作り直したもの。作り直しは「テストの駆動を変える」行為なので、
// README §4 の規律どおり**前後の両方に同じ変異を当てて等価を確かめる**（PR 本文に結果）。
//
// 作り直さなかったもの（main に残したテストが使う）: consoleCatalog / useTempUsageDir /
// stubChatProvider —— それぞれ Console のカタログを読む・main の折り込み状態を戻す・
// main の型を取る、で**純粋ではない**ため。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// writeUIPrefs は ui-prefs.json を隔離した HOME に書く（main の ui_prefs_test.go と同じ形）。
func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Dir(uiprefs.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiprefs.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// japaneseRanges / firstJapaneseRune / hasJapanese は main の prompt_lang_test.go の
// **範囲表ごとの写し**（4 レンジ全部）。`unicode.Hiragana/Katakana/Han` で書き直すと
// **CJK 記号・句読点 (0x3000-0x303F) と全角英数記号 (0xFF01-0xFF60) が判定から落ちて
// 検査が弱くなる**。作り直しは「同じことをしている」ように見えて範囲が変わるので、
// 表はそのまま持ってくる（最初に書き直して 2 レンジ落とし、変異試験の前に気付いた）。
var japaneseRanges = []*unicode.RangeTable{
	{R16: []unicode.Range16{
		{Lo: 0x3000, Hi: 0x303F, Stride: 1}, // 、。「」『』（）〜 など CJK 記号・句読点
		{Lo: 0x3040, Hi: 0x30FF, Stride: 1}, // ひらがな・カタカナ・ー・・
		{Lo: 0x4E00, Hi: 0x9FFF, Stride: 1}, // CJK 統合漢字
		{Lo: 0xFF01, Hi: 0xFF60, Stride: 1}, // 全角英数・記号
	}},
}

func firstJapaneseRune(s string) rune {
	for _, r := range s {
		if unicode.IsOneOf(japaneseRanges, r) {
			return r
		}
	}
	return 0
}

func hasJapanese(s string) bool { return firstJapaneseRune(s) != 0 }

// stubChatProvider は 1 種別のプロバイダを差し替える（main の bridge_operator_test.go と同じ形）。
func stubChatProvider(t *testing.T, kind string, p ChatProvider) {
	t.Helper()
	old, had := ChatProviders[kind]
	ChatProviders[kind] = p
	t.Cleanup(func() {
		if had {
			ChatProviders[kind] = old
		} else {
			delete(ChatProviders, kind)
		}
	})
}

// consoleCatalog は Console の i18n カタログを **ドメイン別ファイルの束**として読む
// （main の console_catalog_test.go の写し）。
//
// 🔥 **相対パスの深さだけが違う。** 原本は workspace/agent から `../../console/...`、
// こちらは workspace/agent/internal/chatx から `../../../../console/...`。
// 深さを直し忘れると `os.ReadDir` が外れて **t.Skipf で黙って飛ぶ＝検査が消える**
// （移送で検査が無言化する典型）。TestConsoleCatalogIsReachable がそれを踏ませない。
// consoleLocalesDir は**この 2 本が見るのと同じ 1 つのパス式**。
// 深さを直し忘れたときに「カタログが無い」と「見る場所が違う」を切り分けられるよう、
// 経路を 1 本にしてある。
func consoleLocalesDir(locale string) string {
	return filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", locale)
}

func consoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := consoleLocalesDir(locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		t.Fatalf("%s に .ts が 1 つも無い（カタログの置き場所が変わった？）", dir)
	}
	return b.String()
}

func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}

// TestConsoleCatalogIsReachable は **consoleCatalog が Skip に落ちていない**ことを見る。
// 相対パスの深さを間違えると、カタログのキー検査 2 本が「カタログが無い」で飛んで
// 静かに消える —— それを赤で捕まえるための 1 本。
//
// 🔥 **`consoleCatalog` を経由してはいけない。** 経由すると、深さが違うときに
// `t.Skipf` が**この検査ごと飛ばす**ので、守ろうとした穴がそのまま開く
// （最初にそう書いてレビューで指摘された。SKIP は 5 本→8 本に増えるのに `go test` は緑）。
// ここは `os.ReadDir` の結果を自分で見て `t.Fatalf` する。
func TestConsoleCatalogIsReachable(t *testing.T) {
	dir := consoleLocalesDir("ja")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("カタログの置き場が読めない: %s: %v（相対パスの深さを疑う。"+
			"ここが読めないと NoticeKeys / ReportKeys の 2 本が黙って skip される）", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ts") {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("%s に .ts が 1 つも無い（カタログの置き場所が変わった？）", dir)
	}
}

// stubAbortResumeHolds は「このセッションは自動再開の最中か」の継ぎ目だけを差し替える。
//
// 🔥 移送前は main の `abortResumeStates` に状態を直接書いて**本物の判定**を通していた。
// chatx から main の var には届かないので、**判定への入力を注入する**形に変えている
// （判定そのものの検査は main の abort_resume_test.go が持つ）。
// 駆動を変えているので、README §4 のとおり**移送前後の両方に同じ変異を当てて等価を確認**した
// （結果は PR 本文。当てた場所も併記）。
func stubAbortResumeHolds(t *testing.T, name string, hold bool) {
	t.Helper()
	old := deps.AbortResumeHolds
	deps.AbortResumeHolds = func(n string, a claude.Abort, now time.Time) bool {
		if n == name {
			return hold
		}
		return old(n, a, now)
	}
	t.Cleanup(func() { deps.AbortResumeHolds = old })
}

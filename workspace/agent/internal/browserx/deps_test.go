package browserx

import (
	"errors"
	"os"
	"testing"
)

// TestMain は browserx 単体のテストバイナリで deps.go の関数変数を結線する。
// 本番の結線は package main の alias_browser.go が行うが、このバイナリに package main は
// 居ないので、nil のまま chromium 解決へ入ると panic する（deps.go の既定は「黙らせない」）。
//
// chromium 系は「ピンは無い」で結線する。これは代用ではなく**このバイナリでの事実**である:
//   - chromiumPinnedBinary "" = ピン済みビルド未インストール。findChromiumBinary は
//     AF_CHROMIUM_BIN / CHROMIUM_PATH → PATH → ピン → playwright キャッシュの順に見るので、
//     live テストは移送前と同じく env と PATH で chromium を見つける（順序に触っていない）。
//   - chromiumDefaultPin "" = ensureChromiumForPane が (false, nil) を返す＝「入れるものが無い」。
//     テストバイナリが chromium を勝手にダウンロードしないための正しい答えでもある。
//   - installChromium はそれゆえ到達しない。到達したら結線の想定が崩れているので、
//     黙って成功させずエラーにする。
//
// agentSendToSession はここでは結線しない。必要な 2 本が
// browser_handoff_ledger_test.go の stubAgentInputServer で明示的に差し替えており、
// **実配線を通す台帳テストは package main 側に置いてある**（あちらの冒頭コメント参照）。
// ここで既定を与えると、結線を忘れたテストが黙って通ってしまう。
func TestMain(m *testing.M) {
	chromiumDefaultPin = func() string { return "" }
	chromiumPinnedBinary = func() string { return "" }
	installChromium = func(string) error {
		return errors.New("browserx のテストは chromium をインストールしない（ここへ来たら結線の想定違い）")
	}
	os.Exit(m.Run())
}

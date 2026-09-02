package browserx

// browserx が package main から借りるホスト機能。import ではなく関数変数で受けている:
// 提供側は browserx が依存してはならないファイル（mcp_stdio.go の loopback REST 配達
// スタック、install_tools.go の chromium ピン解決）に在り、package main を import し返す
// ことはそもそもできない。
//
// **名前は移送前と同じにしてある**ので、移送されたファイル側の呼び出しは 1 文字も
// 変わっていない（移送を移送のままに保つ）。結線は package main の alias_browser.go の
// init で 1 度だけ行う。
//
// nil のまま呼べば panic する。無害な既定値を置くとテストが「結線し忘れ」を緑で
// 通してしまうので、結線漏れは黙らせずに落とす。browserx 単体のテストバイナリは
// deps_test.go の TestMain で明示的に結線している。
var (
	// agentSendToSession は loopback の Agent REST でセッションへプロンプトを配達する
	// （停止中なら再開してから再送）。ハンドオフ台帳の配達段が使う。
	agentSendToSession func(name string, body []byte) (out string, resumed bool, err error)

	// chromiumDefaultPin はこのアーキが固定している chromium ビルドを返す。
	chromiumDefaultPin func() string

	// chromiumPinnedBinary はピン済み chromium の実行ファイルを返す（未インストールなら ""）。
	chromiumPinnedBinary func() string

	// installChromium は指定ピンの chromium を取得して配置する。
	installChromium func(pin string) error
)

// SetDeps はホスト機能を結線する。package main の init から 1 度だけ呼ばれる。
func SetDeps(
	sendToSession func(name string, body []byte) (string, bool, error),
	defaultPin func() string,
	pinnedBinary func() string,
	install func(pin string) error,
) {
	agentSendToSession = sendToSession
	chromiumDefaultPin = defaultPin
	chromiumPinnedBinary = pinnedBinary
	installChromium = install
}

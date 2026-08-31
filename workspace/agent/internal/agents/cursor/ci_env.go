package cursor

// CI 変数を cursor CLI に渡さない（docs/log/40 Track B）。
//
// cursor CLI は `CI` を見つけると対話 UI を出さない: バナーだけ描いて composer を
// 描画せず、打鍵も無視する（実測 2026-08-27・cursor 2026.08.25）。しかも CLI 自身の
// 起動ログは `first_paint_ms` を出して正常完了を報告するため、外形は健康なまま UI
// だけが無い＝「固まっている」ようにしか見えない。実際 CI 上の TUI 契約テストが
// これで落ち続けた（tui_mirror_contract_test.go）。
//
// 判定は値ではなく **存在** で行われる: `CI=`（空文字）でも同じく死に、生き返るのは
// unset か `CI=false` だけ（実測）。したがって空文字での上書きは対策にならない。
//
// Workspace のコンテナ自体は CI を設定しないが、利用者は Console の設定（環境変数）で
// 足せる。足した瞬間に cursor のセッションだけが「バナーだけの死んだペイン」になり、
// 原因に辿り着くのは上記のとおり難しい。そこで AF は cursor を起動する全経路で外す。
// 他の kind には広げない — copilot は CI 検出を自己更新の抑止に使っており（docs/log/36）、
// 一律に外すとそちらの前提を壊す。

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// ciEnvVar は cursor が対話 UI の可否に使う変数名。
const ciEnvVar = "CI"

// EnvWithoutCI は環境変数リストから CI を取り除いた新しいリストを返す（入力は変更
// しない）。`CI` そのものだけを落とし、`CI_FOO` や `MY_CI` のような別名は残す。
// exec.Cmd を組み立てる全経路（TUI 以外＝ACP ドライバ・ログイン PTY・status/models
// などのプローブ・アシスタントチャットの headless）で使う。パッケージ外にも
// 公開しているのは、チャットの headless 経路だけ main パッケージから CLI を叩くため。
func EnvWithoutCI(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name == ciEnvVar {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// unsetCIPrefix は同じ処理の tmux ペイン用。ペインへ渡すのは exec.Cmd ではなく
// シェル文字列で、tmux の `-e` は「設定」しかできず unset ができないため、
// coreutils の `env -u` で外す（`CI=` と空にするのでは効かない — 上記）。
const unsetCIPrefix = "env -u " + ciEnvVar + " "

// probeCmd は CLI を一発叩くプローブ（status / about / models / logout）用の
// exec.Cmd を、CI を外した環境で組み立てる。これらは非対話なので実測では CI 有りでも
// 動くが、経路ごとに例外を作ると「どこで外していてどこで外していないか」を覚えて
// おく必要が出る。規則は 1 つ——AF は cursor に CI を渡さない。
func probeCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin(), args...)
	cmd.Env = EnvWithoutCI(os.Environ())
	return cmd
}

package copilot

// ユーザー指示（docs/60）の copilot 側 artifact。
//
// copilot は user スコープの指示を 3 経路で読む（実測 2026-08-13・1.0.79 — 行動
// カナリアで確認。内容の開示は拒否するので「この語が指示にあるか」は答えない）:
//
//	$COPILOT_HOME/copilot-instructions.md              … 利用者のファイル。AF は所有しない
//	$COPILOT_HOME/instructions/**/*.instructions.md    … ★ここへ AF 専用の1本を置く
//	COPILOT_CUSTOM_INSTRUCTIONS_DIRS=<dir>             … env。効くが起動経路ごとの配線が要る
//
// ディレクトリ内の専用ファイルを選ぶ理由: env 版は tmux 起動 / managed ACP ドライバ /
// 利用者が手で叩く `copilot` の 3 経路すべてに export を配る必要があり、1 つ漏れると
// 「そのセッションだけ効かない」という見えない穴になる。ファイルなら**どの起動経路でも
// 同じように読まれる**。$COPILOT_HOME 配下に AF 専用ファイルを持つのは rtk
// （hooks/rtk.json）で既に踏んでいる前例と同じ形。
//
// フリート方針はまだ配っていない（docs/60 §60.13 P2）。ここは user 層だけを扱う。

import (
	"os"
	"path/filepath"
)

// UserInstructionsPath is the AF-owned file inside copilot's user instructions dir.
// The name is af's alone, so the whole file can be written and removed as a unit —
// no markers, nothing of the user's to preserve.
func UserInstructionsPath() string {
	return filepath.Join(Home(), "instructions", "agent-fleet-user.instructions.md")
}

// ApplyUserInstructions writes (or removes, when body is empty) that file.
func ApplyUserInstructions(body string) error {
	path := UserInstructionsPath()
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

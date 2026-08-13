package kiro

// ユーザー指示（docs/60）の kiro 側 artifact。
//
// kiro の永続コンテキストは **steering**（markdown のディレクトリ）で、ワークスペース側
// `.kiro/steering/`（リポジトリの中＝プロジェクト層）とは別に、**global な
// `~/.kiro/steering/*.md`** がある。後者がユーザー層に当たる。
//
// 実測（2026-08-13・kiro 2.16.0、行動カナリア）: `~/.kiro/steering/` に置いた md の
// 指示どおりに `kiro-cli chat --no-interactive` が応答した（プロジェクト側に .kiro が
// 無いディレクトリで確認）。front-matter は不要で、素の markdown が読まれる。
//
// AF 専用の名前のファイルを 1 本だけ持つ（copilot と同じ形）。ディレクトリ内の他の
// steering は利用者やチームのものなので、列挙も削除もしない。

import (
	"os"
	"path/filepath"
)

// UserInstructionsPath is the AF-owned file inside kiro's global steering directory.
func UserInstructionsPath() string {
	return filepath.Join(Home(), "steering", "agent-fleet-user.md")
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

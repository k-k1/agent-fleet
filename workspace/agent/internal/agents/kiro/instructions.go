package kiro

// ユーザー指示（docs/log/60）の kiro 側 artifact。
//
// kiro の永続コンテキストは **steering**（markdown のディレクトリ）で、ワークスペース側
// `.kiro/steering/`（リポジトリの中＝プロジェクト層）とは別に、**global な
// `~/.kiro/steering/*.md`** がある。後者がユーザー層に当たる。
//
// 実測（2026-08-13・kiro 2.16.0、行動カナリア）: `~/.kiro/steering/` に置いた md の
// 指示どおりに `kiro-cli chat --no-interactive` が応答した（プロジェクト側に .kiro が
// 無いディレクトリで確認）。front-matter は不要で、素の markdown が読まれる。
//
// AF 専用の名前のファイルを持つ（copilot と同じ形）— ユーザー指示とフリート方針で
// **1 本ずつ**。ディレクトリ内の他の steering は利用者やチームのものなので、列挙も
// 削除もしない。名前は "guide" < "user" の順に並ぶようにしてあるが、読み込み順は
// 保証されていないので優先順位は本文側にも書いてある（docs/log/60 §60.5-4）。

import (
	"os"
	"path/filepath"
)

// UserInstructionsPath is the AF-owned file inside kiro's global steering directory.
func UserInstructionsPath() string {
	return filepath.Join(Home(), "steering", "agent-fleet-user.md")
}

// FleetNotesPath is the AF-owned steering file carrying the baked workspace guide
// (docs/log/60 §60.13 P2 — kiro read no fleet policy at all until now).
func FleetNotesPath() string {
	return filepath.Join(Home(), "steering", "agent-fleet-guide.md")
}

// ApplyFleetNotes writes the workspace guide. An empty guide is a no-op.
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return writeOwnedFile(FleetNotesPath(), fleet)
}

// ApplyUserInstructions writes (or removes, when body is empty) the user file.
func ApplyUserInstructions(body string) error {
	return writeOwnedFile(UserInstructionsPath(), body)
}

// writeOwnedFile writes a file agent-fleet owns outright, removing it when the body
// is empty so nothing stale is left behind. Writes only when the content changes.
func writeOwnedFile(path, body string) error {
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

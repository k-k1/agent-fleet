package session

import "strings"

// ShellQuote は s をシングルクォートで安全に囲む（tmux に渡す起動コマンドの
// 組み立て用）。package main の shellQuote を docs/log/23 残① Wave D で移設した —
// CLI 縦割りパッケージ（internal/agents/opencode 等）と main の双方が使う。
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

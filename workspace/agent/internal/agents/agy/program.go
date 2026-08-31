package agy

// agy の起動コマンド組み立てと、状態ファイル（~/.gemini/antigravity-cli 配下）の
// 読み口。resume は claude 型の自己 sid 固定ができない（--session-id 相当なし）
// ため、codex 同様スロット毎に会話 UUID を保持して `--conversation` で再開する。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr は package main の同名ヘルパの複製（極小のため共有せず重複を許容）。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stateDir is agy's own state root: token, settings, caches, conversation DBs.
// agy hardcodes ~/.gemini (旧 gemini-cli 系の流儀) — the whole ~/.gemini tree is
// denylisted from the file browser (fs.go) because the OAuth token persists in
// plaintext under it when no keyring is present (docs/decisions/0008 再PoC).
// (tokenPath / SignedIn live in usage.go — Track C 側の定義を正とする。)
func stateDir() string { return filepath.Join(paths.GeminiHome(), "antigravity-cli") }

func settingsPath() string { return filepath.Join(stateDir(), "settings.json") }

// LastConversationFor returns agy's conversation UUID for dir from its
// cache/last_conversations.json (cwd→UUID) — the immediate source; the SQLite
// summaries DB is lazily written and can't be relied on (docs/log/32 Track D-3).
// "" when the file or entry is absent.
func LastConversationFor(dir string) string {
	return LastConversationIn(stateDir(), dir)
}

// LastConversationIn is LastConversationFor against an explicit antigravity-cli
// state root — for the assistant chat, whose agy runs under an isolated HOME
// (chatAgyHome) rather than the user's ~/.gemini.
func LastConversationIn(stateRoot, dir string) string {
	b, err := os.ReadFile(filepath.Join(stateRoot, "cache", "last_conversations.json"))
	if err != nil {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m[dir]
}

// buildProgram returns the tmux pane program for an agy session. agy owns its
// auth (the token file written via the Connections flow), so nothing is
// injected. Resume is `--conversation <UUID>` with the slot's captured id —
// NOT `--continue`, whose cwd→last mapping any other agy run in the dir
// overwrites (docs/log/32 Track D-3). --model stays at agy's default in M1 unless
// the create request pinned one.
// bypass=false は「権限確認をスキップしない」（docs/log/76 の利用者選択、または plan 起動）。
// 承認待ちは pending.go が "permission" として拾い、Console の許可カードで答えられる。
func buildProgram(model, mode, resumeID string, bypass bool) string {
	if override := os.Getenv("AGENT_AGY_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_AGY_FLAGS", "--dangerously-skip-permissions")
	if !bypass {
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--dangerously-skip-permissions", ""))
	}
	if mode == "plan" {
		// agy has a native execution-mode flag（bypass は上で外れている — 全ツールを
		// 自動承認しては plan で始める意味が無い）。
		flags = strings.TrimSpace(flags + " --mode plan")
	}
	if model != "" {
		flags += " --model " + session.ShellQuote(model)
	}
	if resumeID != "" {
		flags += " --conversation " + session.ShellQuote(resumeID)
	}
	return strings.TrimSpace("agy " + flags)
}

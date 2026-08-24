package copilot

// copilot の起動コマンド組み立てと、状態ディレクトリ（$COPILOT_HOME、既定
// ~/.copilot）のパス解決。resume は claude 型の自己 sid 固定（--session-id）が
// 使えるため、agy/codex のような会話 UUID 捕獲は不要 — 常に同じフラグで新規/
// 再開の両方をまかなえる（実測: 既存 id は resume、未知の valid v4 は新規作成）。

import (
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

// Home is copilot's state root ($COPILOT_HOME, default ~/.copilot): config.json
// (trustedFolders), mcp-config.json, logs/, session-store.db and
// session-state/<sid>/. The tree is denylisted from the file browser (fs.go):
// キーチェーンの無いコンテナでは auth トークンが平文で保存され得る（公式 docs）。
func Home() string { return paths.CopilotHome() }

func configPath() string { return filepath.Join(Home(), "config.json") }

// sessionStateDir is the per-session state dir holding events.jsonl (read 正本).
func sessionStateDir(sid string) string { return filepath.Join(Home(), "session-state", sid) }

// EventsPath is the session's events.jsonl — the transcript/state source shared
// by every mode (TUI / -p / ACP managed) — docs/36 実測記録.
func EventsPath(sid string) string { return filepath.Join(sessionStateDir(sid), "events.jsonl") }

// defaultFlags is the fleet-standard permission/privacy posture:
//   - --allow-all: fleet 既定の bypass 相当（claude の skip-permissions と同格）。
//   - --no-remote --no-remote-export: セッションの GitHub 同期とリモート操縦は
//     既定オフ（会話のフリート外流出と二重操縦を防ぐ — docs/36 契約）。
const defaultFlags = "--allow-all --no-remote --no-remote-export"

// buildProgram returns the tmux pane program for a copilot session. Auth is
// ambient（gh 透過認証のトークンを copilot 自身が拾う — 実測）なのでトークンは
// 注入しない。--session-id は新規作成と resume の両方を同じ形でまかなう。
// bypass=false は「権限確認をスキップしない」（docs/76 の利用者選択、または plan 起動）。
// 外すのは --allow-all だけ。--no-remote / --no-remote-export（会話のフリート外流出と
// 二重操縦の防止）は権限確認とは別軸なので必ず残す。
func buildProgram(model, effort, mode, sid string, bypass bool) string {
	if override := os.Getenv("AGENT_COPILOT_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_COPILOT_FLAGS", defaultFlags)
	if !bypass {
		// TUI 側の許可メニューはユーザーが端末/ミラーで操作する。
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--allow-all", ""))
	}
	if mode == "plan" {
		flags = strings.TrimSpace(flags + " --mode plan")
	}
	concreteModel := model != "" && model != "auto"
	if concreteModel {
		flags += " --model " + session.ShellQuote(model)
	}
	// Auto (copilot's default, and the ONLY model on the Free plan) rejects --effort:
	// "Model \"auto\" does not support reasoning effort configuration". Pass effort only
	// alongside an explicit non-auto model, else the session errors on launch.
	if effort != "" && concreteModel {
		flags += " --effort " + session.ShellQuote(effort)
	}
	if sid != "" {
		flags += " --session-id " + session.ShellQuote(sid)
	}
	return strings.TrimSpace("copilot " + flags)
}

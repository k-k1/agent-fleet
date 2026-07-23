package cursor

// cursor の起動コマンド組み立てと、状態・転写パスの解決（docs/40 Track A）。
//
// セッション同一性は AF 側で採番した v4 UUID を `--resume <uuid>` に渡す方式。
// 実測（v2026.07.20）: 未知の valid v4 UUID を --resume に渡すとその ID で新規
// チャットを作成し、既存 ID なら resume する（copilot の --session-id と同型）——
// docs/40 は `create-chat` 事前採番を想定していたが、自己採番 UUID なら起動時の
// 追加 exec が要らず速い。転写は Claude Code 互換 JSONL（`<chatId>.jsonl`）で、
// cwd スラグ配下に置かれる（実測パスは transcriptPath 参照）。

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr は極小ヘルパ（copilot/program.go と同様、共有せず重複を許容）。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Home is cursor's state root (~/.cursor): chats/<ws-hash>/<chatId>/store.db
// （非公開 SQLite・読まない）、projects/<slug>/agent-transcripts/（読む JSONL）、
// hooks.json・cli-config.json など。資格情報は別ツリー（~/.config/cursor/auth.json）
// にあり、両方とも fs.go の denylist 対象（平文トークン保護 — docs/40 契約）。
func Home() string {
	return filepath.Join(paths.HomeDir(), ".cursor")
}

// projectsDir is ~/.cursor/projects — the per-cwd transcript tree root.
func projectsDir() string { return filepath.Join(Home(), "projects") }

// cwdSlug は cwd を cursor の projects スラグへ写像する（実測: 先頭/末尾の "/" を
// 除いて残りの "/" を "-" に。例 /tmp/curprobe → tmp-curprobe）。スラグ規則が版で
// ドリフトしても transcriptPath は glob フォールバックで chatId から一意に引ける
// ので、これは第一候補にすぎない。
func cwdSlug(dir string) string {
	return strings.ReplaceAll(strings.Trim(filepath.Clean(dir), "/"), "/", "-")
}

// transcriptPath resolves the Claude Code-compatible JSONL transcript for chatId
// launched in dir. chatId is globally unique, so a glob across every cwd slug finds
// it even if the slug rule drifts; the computed slug is only the fast first guess.
func transcriptPath(dir, chatID string) string {
	if chatID == "" {
		return ""
	}
	guess := filepath.Join(projectsDir(), cwdSlug(dir), "agent-transcripts", chatID, chatID+".jsonl")
	if _, err := os.Stat(guess); err == nil {
		return guess
	}
	// スラグ規則ドリフト時: chatId でグロブして拾う（一意）。
	if hits, _ := filepath.Glob(filepath.Join(projectsDir(), "*", "agent-transcripts", chatID, chatID+".jsonl")); len(hits) > 0 {
		return hits[0]
	}
	return guess // 未生成（起動直後）— 呼び出し側は Stat 失敗を空扱いにする。
}

// defaultFlags is the fleet-standard posture:
//   - --force: fleet 既定の bypass 相当（"unless explicitly denied" — deny リストは
//     引き続き有効。実測 help）。claude の skip-permissions と同格。
//   - --trust: 未信頼ワークスペースの trust プロンプトをスキップ（実測 help。
//     copilot の config.json 事前追記に相当する起動時スキップ）。
const defaultFlags = "--force --trust"

// bin returns the cursor CLI binary（symlink `cursor-agent`。`agent` は短すぎて
// PATH 衝突しやすいので使わない）。AGENT_CURSOR_BIN で差し替え可。
func bin() string { return envOr("AGENT_CURSOR_BIN", "cursor-agent") }

// buildProgram returns the tmux pane program for a cursor TUI session. Auth is
// ambient（~/.config/cursor/auth.json を CLI 自身が拾う — 実測）なのでトークンは
// 注入しない。--resume は新規作成と resume の両方を同じ形でまかなう。
func buildProgram(model, mode, chatID string) string {
	if override := os.Getenv("AGENT_CURSOR_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CURSOR_FLAGS", defaultFlags)
	if mode == "plan" {
		// Plan mode replaces the bypass default: auto-approving every tool would
		// defeat a plan start（copilot/agy と同じ判断）。--trust は残す。
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--force", ""))
		flags = strings.TrimSpace(flags + " --plan")
	}
	// cursor のモデル ID は effort 込み（例 claude-opus-4-8-thinking-high）——
	// 別 --effort フラグは無い（実測 help）ので effort は渡さない。"auto" は既定＝
	// フラグ無し。
	if model != "" && model != "auto" {
		flags += " --model " + session.ShellQuote(model)
	}
	if chatID != "" {
		flags += " --resume " + session.ShellQuote(chatID)
	}
	return strings.TrimSpace(bin() + " " + flags)
}

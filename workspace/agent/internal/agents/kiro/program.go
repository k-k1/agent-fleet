package kiro

// kiro の起動コマンド組み立てと、セッションストア（v2 JSONL）のパス解決・sid 発見
// （docs/43 Track A）。
//
// セッション ID は CLI 採番（cursor と違い AF では先取りできない — kiro.go 参照）。
// 起動は `kiro-cli chat --agent-engine v2 --trust-all-tools [--model …] [--effort …]
// [--resume-id …]`。--agent-engine v2 を明示ピンするのは、既定が現状 v2 でも将来
// v3 へ既定が振れるドリフト保険（docs/43 §5-2 決定）。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr は極小ヘルパ（cursor/program.go と同様、共有せず重複を許容）。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// bin returns the kiro CLI binary. AGENT_KIRO_BIN で差し替え可（テスト／別パス）。
func bin() string { return envOr("AGENT_KIRO_BIN", "kiro-cli") }

// Bin exposes the resolved kiro CLI binary for callers outside this package.
func Bin() string { return bin() }

// Installed reports whether the kiro CLI is present on PATH (baked or already
// on-demand installed). Used by the connection card's install flow (docs/43 Track C)
// to decide whether the ~855MB bundle still needs to land in ~/.local.
func Installed() bool {
	_, err := exec.LookPath(bin())
	return err == nil
}

// Home is kiro's config/session root (~/.kiro): settings/cli.json、settings/mcp.json、
// agents/、そして sessions/cli/（v2 セッションストア）。資格情報は別ツリー
// （~/.local/share/kiro-cli/data.sqlite3）で、両方とも fs.go の denylist 対象。
func Home() string { return filepath.Join(paths.HomeDir(), ".kiro") }

// sessionsDir is ~/.kiro/sessions/cli — the flat v2 session store (per-sid
// <sid>.json meta + <sid>.jsonl transcript + <sid>.lock + <sid>.history).
func sessionsDir() string { return filepath.Join(Home(), "sessions", "cli") }

// sessionJSONPath / transcriptPath resolve a session's meta and transcript files.
func sessionJSONPath(sid string) string { return filepath.Join(sessionsDir(), sid+".json") }
func transcriptPath(sid string) string  { return filepath.Join(sessionsDir(), sid+".jsonl") }

// sessionMeta is the subset of <sid>.json we read to attribute a session to a cwd.
type sessionMeta struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

// discoverSid finds the newest v2 session launched in dir. kiro writes <sid>.json at
// session start (before the first turn — 実測), so a fresh launch's session is the
// newest for its cwd; once resolveSid caches it, the choice sticks. Same-cwd
// collisions (two AF slots in ONE dir) are the known edge — worktrees give distinct
// dirs, which is the fleet's parallel-isolation mechanism, so this stays correct in
// practice. Recency = the .json file's mtime (rewritten each turn).
func discoverSid(dir string) string {
	want := filepath.Clean(dir)
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return ""
	}
	best, bestMod := "", int64(-1)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(sessionsDir(), e.Name())
		var sm sessionMeta
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &sm) != nil {
			continue
		}
		if filepath.Clean(sm.Cwd) != want {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if m := fi.ModTime().UnixNano(); m > bestMod {
			best, bestMod = strings.TrimSuffix(e.Name(), ".json"), m
		}
	}
	return best
}

// defaultFlags is the fleet-standard posture for a TUI launch:
//   - chat: the interactive subcommand.
//   - --agent-engine v2: pin the v2 engine (v2 JSONL store は本実装の read 正本。
//     既定 v2 でも将来 v3 に振れないよう明示。v3 は別ストア/別状態契約になり得る)。
//   - --trust-all-tools: fleet 既定の bypass 相当（claude skip-permissions と同格）。
//     初回の危険モード確認ダイアログは chat.disableTrustAllConfirmation で抑止する
//     （ensureSettings が固定）。
const defaultFlags = "chat --agent-engine v2 --trust-all-tools"

// buildProgram returns the tmux pane program for a kiro TUI session. 認証は環境依存
// （~/.local/share/kiro-cli を CLI 自身が拾う — 実測）なのでトークンは注入しない。
func buildProgram(model, effort, mode, resumeID string) string {
	if override := os.Getenv("AGENT_KIRO_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_KIRO_FLAGS", defaultFlags)
	if mode == "plan" {
		// Plan mode drops the bypass so tools require approval — auto-approving every
		// tool would defeat a plan start（cursor/copilot/agy と同じ判断）。承認待ちは
		// state.go が "question" として拾える（明示テキスト "requires approval"）。
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--trust-all-tools", ""))
	}
	// "auto"（既定・1M ctx）はフラグ無し。named モデルは Free でも指定可（実測）。
	if model != "" && model != "auto" {
		flags += " --model " + session.ShellQuote(model)
	}
	if effort != "" {
		flags += " --effort " + session.ShellQuote(effort)
	}
	if resumeID != "" {
		flags += " --resume-id " + session.ShellQuote(resumeID)
	}
	core := strings.TrimSpace(bin() + " " + flags)
	// On-demand first-use install (docs/43 Track B / §4-2). kiro's ~855MB bundle is
	// NOT baked or boot-installed for everyone; it lands in the user's ~/.local the
	// first time they actually launch kiro. tmux runs the pane program via /bin/sh,
	// so guard the launch: install (progress visible in the pane) only when the CLI
	// is missing, then run it. A baked / already-installed kiro makes this a no-op
	// `command -v` check. Only for the default binary — an AGENT_KIRO_BIN override
	// (tests, alt paths) skips the bootstrap so it never triggers a real install.
	if bin() == "kiro-cli" {
		return "command -v kiro-cli >/dev/null 2>&1 || workspace-agent install-kiro; " + core
	}
	return core
}

// ensureSettings pins the two fleet-required kiro settings ONCE per process, best
// effort:
//   - app.disableAutoupdates=true — 版はイメージ再ビルド/オンデマンド導入で管理し、
//     背景自己更新を止める（entrypoint も再固定するが lean/フル両対応でここでも保険）。
//   - chat.disableTrustAllConfirmation=true — --trust-all-tools 初回の危険モード確認
//     ダイアログを抑止（未設定だと launch pane がダイアログで固着する — 実測）。
//
// 導入（Track B）が両設定を固めるのが本筋だが、素の home でも launch が固着しないよう
// read 層側でも冪等に固める。バイナリ非存在時は no-op。
var settingsOnce sync.Once

func ensureSettings() {
	settingsOnce.Do(func() {
		if _, err := exec.LookPath(bin()); err != nil {
			return
		}
		for _, kv := range [][2]string{
			{"app.disableAutoupdates", "true"},
			{"chat.disableTrustAllConfirmation", "true"},
		} {
			_ = exec.Command(bin(), "settings", kv[0], kv[1]).Run()
		}
	})
}

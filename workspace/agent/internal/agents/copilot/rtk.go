package copilot

import (
	"os"
	"path/filepath"
)

// rtk (token-saving CLI proxy) — copilot 側の適用 artifact。codex/agy と違い
// copilot は「決定的な preToolUse フック」を持てる（claude/opencode と同格）:
// copilot CLI はユーザースコープ $COPILOT_HOME/hooks/*.json の preToolUse フックを
// 読み、`rtk hook copilot` がツール呼び出しの command を `rtk <command>` へ書き換える
// （実測: modifiedArgs 出力が適用され `git status` → `rtk git status` になる）。
//
// なぜプラグイン(--plugin-dir)ではなくユーザースコープ hooks ファイルなのか:
// copilot CLI にはプラグイン定義 hooks.json の preToolUse が発火しない既知バグ
// (github/copilot-cli#2540)があり、確実に発火するのは policy / repo(.github/hooks) /
// user($COPILOT_HOME/hooks) / settings.json 由来のフック。よって user スコープの
// 単独ファイルに置く。trust.go が同じ $COPILOT_HOME/config.json を触るのと同居可能
// （別ファイルなので衝突しない）。
//
// native 形式（イベント名 camelCase "preToolUse"）を使う: rtk は modifiedArgs だけを
// 返し permissionDecision を付けないので --allow-all の姿勢を崩さず透過的に書き換わる
// （PascalCase "PreToolUse" だと Claude 互換出力になり permissionDecision:"ask" が付く）。
// matcher "bash" で shell ツール(toolName=="bash")のみに絞る。
//
// durable な設定と起動時 reconcile は package main（agent_rtk.go）に残る。

// hooksPath is the user-scope hook file. A dedicated filename (not config.json)
// keeps rtk's toggle independent of trust.go's trustedFolders writes.
func hooksPath() string { return filepath.Join(Home(), "hooks", "rtk.json") }

// rtkHooks is the preToolUse hook wiring copilot's shell tool through rtk.
const rtkHooks = `{
  "version": 1,
  "hooks": {
    "preToolUse": [
      { "type": "command", "matcher": "bash", "bash": "rtk hook copilot" }
    ]
  }
}
`

// ApplyRTK writes (on) or removes (off) the user-scope rtk hook file. Idempotent:
// writes only when the content differs, and a missing file when off is a no-op.
func ApplyRTK(on bool) {
	path := hooksPath()
	if !on {
		_ = os.Remove(path) // absent ⇒ os.Remove errors, ignored — desired end state reached
		return
	}
	if b, err := os.ReadFile(path); err == nil && string(b) == rtkHooks {
		return // already applied
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".af-tmp"
	if os.WriteFile(tmp, []byte(rtkHooks), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

package agy

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — agy 側の適用 artifact。codex と同じく command-
// rewrite hook が無いので、実体はグローバル AGENTS.md 末尾のマーカー付き案内
// ブロックの有無（instruction-based / best-effort）。置き場所は ~/.gemini/AGENTS.md
// — 実測（docs/32 Track A）で agy が対話・headless 両モードで読むグローバル
// コンテキストはこのパスのみ（~/.gemini/antigravity-cli/AGENTS.md は読まれず、
// プロジェクト root の AGENTS.md は対話モードのみ）。durable な設定と起動時
// reconcile は package main（agent_rtk.go）に残る。

// マーカーの綴りは mdblock が持つ（codex/agy/ユーザー指示で共通）。
var rtkMarkerStart, rtkMarkerEnd = mdblock.Markers("rtk")

// rtkBlock is the instruction appended to ~/.gemini/AGENTS.md when rtk is on.
const rtkBlock = "## rtk (token saver) — prefer it for shell commands\n" +
	"`rtk` is a CLI proxy that compacts command output to save context tokens. Prefix\n" +
	"read / inspect / build commands with it — same command, smaller output:\n" +
	"`rtk git status`, `rtk ls`, `rtk grep ...`, `rtk cargo test`, `rtk npm run build`.\n" +
	"Skip it only when you need the raw, unfiltered stream; `rtk proxy <cmd>` runs a\n" +
	"command without filtering.\n"

func agentsPath() string { return filepath.Join(paths.GeminiHome(), "AGENTS.md") }

// ApplyRTK appends (on) or removes (off) the marked rtk block in ~/.gemini/AGENTS.md.
// Idempotent: any prior block is stripped first. Writes only when changed.
func ApplyRTK(on bool) {
	body := ""
	if on {
		body = rtkBlock
	}
	_ = editAgents(func(s string) string { return mdblock.Set(s, "rtk", body) })
}

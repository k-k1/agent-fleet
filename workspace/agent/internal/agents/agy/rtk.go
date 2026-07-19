package agy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — agy 側の適用 artifact。codex と同じく command-
// rewrite hook が無いので、実体はグローバル AGENTS.md 末尾のマーカー付き案内
// ブロックの有無（instruction-based / best-effort）。置き場所は ~/.gemini/AGENTS.md
// — 実測（docs/32 Track A）で agy が対話・headless 両モードで読むグローバル
// コンテキストはこのパスのみ（~/.gemini/antigravity-cli/AGENTS.md は読まれず、
// プロジェクト root の AGENTS.md は対話モードのみ）。durable な設定と起動時
// reconcile は package main（agent_rtk.go）に残る。

const rtkMarkerStart = "<!-- agent-fleet:rtk -->"
const rtkMarkerEnd = "<!-- /agent-fleet:rtk -->"

// rtkBlock is the instruction appended to ~/.gemini/AGENTS.md when rtk is on.
const rtkBlock = "## rtk (token saver) — prefer it for shell commands\n" +
	"`rtk` is a CLI proxy that compacts command output to save context tokens. Prefix\n" +
	"read / inspect / build commands with it — same command, smaller output:\n" +
	"`rtk git status`, `rtk ls`, `rtk grep ...`, `rtk cargo test`, `rtk npm run build`.\n" +
	"Skip it only when you need the raw, unfiltered stream; `rtk proxy <cmd>` runs a\n" +
	"command without filtering.\n"

func agentsPath() string { return filepath.Join(paths.HomeDir(), ".gemini", "AGENTS.md") }

// stripMarkedBlock removes the region from start..end (inclusive) and rejoins. A
// missing end marker (malformed file) drops everything from start onward.
// （codex/rtk.go の同名ヘルパの複製 — 極小のため共有せず重複を許容。）
func stripMarkedBlock(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return s
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.TrimRight(s[:i], "\n") + "\n"
	}
	tail := rest[k+len(end):]
	head := strings.TrimRight(s[:i], "\n")
	tail = strings.TrimLeft(tail, "\n")
	if head == "" {
		return tail
	}
	if tail == "" {
		return head + "\n"
	}
	return head + "\n\n" + tail
}

// ApplyRTK appends (on) or removes (off) the marked rtk block in ~/.gemini/AGENTS.md.
// Idempotent: any prior block is stripped first. Writes only when changed.
func ApplyRTK(on bool) {
	path := agentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	}
	out := stripMarkedBlock(orig, rtkMarkerStart, rtkMarkerEnd)
	if on {
		block := rtkMarkerStart + "\n" + rtkBlock + rtkMarkerEnd + "\n"
		if out == "" {
			out = block
		} else {
			out = strings.TrimRight(out, "\n") + "\n\n" + block
		}
	}
	if out == orig || out == "" {
		return // no change, or nothing to write (no base file & rtk off)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".af-tmp"
	if os.WriteFile(tmp, []byte(out), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

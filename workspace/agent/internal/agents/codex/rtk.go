package codex

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — codex 側の適用 artifact（docs/23 残① Wave E で
// package main の agent_rtk.go から移設）。codex には command-rewrite hook が無い
// ので、on/off の実体は ~/.codex/AGENTS.md 末尾のマーカー付き rtk 案内ブロックの
// 有無（instruction-based / best-effort）。durable な設定と起動時 reconcile は
// package main（agent_rtk.go）に残る。

const rtkMarkerStart = "<!-- agent-fleet:rtk -->"
const rtkMarkerEnd = "<!-- /agent-fleet:rtk -->"

// rtkBlock is the instruction appended to codex's AGENTS.md when rtk is on.
// Kept terse; codex reads AGENTS.md at session start.
const rtkBlock = "## rtk (token saver) — prefer it for shell commands\n" +
	"`rtk` is a CLI proxy that compacts command output to save context tokens. Prefix\n" +
	"read / inspect / build commands with it — same command, smaller output:\n" +
	"`rtk git status`, `rtk ls`, `rtk grep ...`, `rtk cargo test`, `rtk npm run build`.\n" +
	"Skip it only when you need the raw, unfiltered stream; `rtk proxy <cmd>` runs a\n" +
	"command without filtering.\n"

// home mirrors codex's own resolution: $CODEX_HOME, else ~/.codex (where the
// entrypoint seeds AGENTS.md).
func home() string { return paths.CodexHome() }

func agentsPath() string { return filepath.Join(home(), "AGENTS.md") }

// stripMarkedBlock removes the region from start..end (inclusive) and rejoins. A
// missing end marker (malformed file) drops everything from start onward.
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

// ApplyRTK appends (on) or removes (off) the marked rtk block in AGENTS.md.
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

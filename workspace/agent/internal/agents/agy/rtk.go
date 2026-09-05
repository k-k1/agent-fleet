package agy

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — the artifact that applies it on the agy side. Like codex,
// agy has no command-rewrite hook, so on/off is really the presence of a marked guidance
// block at the end of the global AGENTS.md (instruction-based / best-effort). It goes in
// ~/.gemini/AGENTS.md: measured (docs/log/32 Track A), that is the only global context agy
// reads in both interactive and headless mode (~/.gemini/antigravity-cli/AGENTS.md is not
// read, and a project-root AGENTS.md only in interactive mode). The durable setting and the
// reconcile at startup stay in package main (agent_rtk.go).

// mdblock owns the marker spelling (shared by codex, agy and the user instructions).
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

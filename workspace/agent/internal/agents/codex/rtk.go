package codex

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// rtk (token-saving CLI proxy) — the codex-side apply artifact. codex has no
// command-rewrite hook, so on/off is nothing but the presence of the marked rtk
// instruction block at the end of ~/.codex/AGENTS.md (instruction-based,
// best-effort). The durable setting and the reconcile at startup stay in package
// main (agent_rtk.go).

// mdblock owns the marker spelling (shared by codex, agy and the user instructions).
var rtkMarkerStart, rtkMarkerEnd = mdblock.Markers("rtk")

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

// AgentsPath is codex's global instruction file ($CODEX_HOME/AGENTS.md) — the one
// file that carries the fleet guide, the user's own instructions and the rtk block
// (docs/log/60 §60.7: codex has no way to point at an extra instructions file, so
// composing this file is the only delivery).
func AgentsPath() string { return filepath.Join(home(), "AGENTS.md") }

// ApplyRTK appends (on) or removes (off) the marked rtk block in AGENTS.md.
// Idempotent: any prior block is stripped first. Writes only when changed.
func ApplyRTK(on bool) {
	body := ""
	if on {
		body = rtkBlock
	}
	_ = editAgents(func(s string) string { return mdblock.Set(s, "rtk", body) })
}

// editAgents is the single read-modify-write for AGENTS.md — the fleet guide, the
// user's instructions and the rtk block all go through it, so the three writers
// cannot race each other into a half-written file (docs/log/60 §60.7, "one file, one
// writer"). Everything outside agent-fleet's markers is preserved.
func editAgents(edit func(string) string) error {
	path := AgentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	out := edit(orig)
	if out == orig || out == "" {
		return nil // no change, or nothing to write (no base file & nothing to add)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

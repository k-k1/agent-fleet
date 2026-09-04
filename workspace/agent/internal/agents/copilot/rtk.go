package copilot

import (
	"os"
	"path/filepath"
)

// rtk (the token-saving CLI proxy) — the artifact applied on the copilot side. Unlike
// codex/agy, copilot can have a deterministic preToolUse hook, on a par with
// claude/opencode: the copilot CLI reads the preToolUse hooks in the user-scope
// $COPILOT_HOME/hooks/*.json, and `rtk hook copilot` rewrites a tool call's command into
// `rtk <command>` (measured: the modifiedArgs output is applied and `git status` becomes
// `rtk git status`).
//
// Why a user-scope hooks file and not a plugin (--plugin-dir): the copilot CLI has a known
// bug where preToolUse in a plugin's hooks.json never fires (github/copilot-cli#2540). The
// hooks that do fire reliably come from policy, the repo (.github/hooks), the user
// ($COPILOT_HOME/hooks) and settings.json — hence a standalone file in user scope. It
// coexists with trust.go touching the same $COPILOT_HOME/config.json, since it is a
// separate file.
//
// The native form (camelCase event name "preToolUse") is used: rtk returns only
// modifiedArgs and no permissionDecision, so the rewrite is transparent and the --allow-all
// posture is left intact. PascalCase "PreToolUse" would produce Claude-compatible output
// and attach permissionDecision:"ask". matcher "bash" narrows it to the shell tool
// (toolName=="bash") alone.
//
// The durable setting and the reconcile at startup stay in package main (agent_rtk.go).

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

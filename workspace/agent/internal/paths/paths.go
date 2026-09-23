// Package paths holds the path conventions under the home directory (docs/log/23 P1-W5).
// Both agent-fleet roots — ~/.config/agent-fleet and ~/.local/state/agent-fleet — are inside
// the file browser's denylist. This is the lowest-layer helper, referenced by package main
// and internal/session/status alike.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// AgentConfigDir is the root for what a Workspace must not lose: the credential store
// (internal/secrets), anything that can carry a credential value, user-supplied
// configuration and user-authored content.
//
// On the ecs-ec2 runtime it is NOT in the home volume. ~/.config is one of AF_WS_KEEP_DIRS,
// so the entrypoint replaces it with a symlink into AF_WS_KEEP — an EFS mount shared by
// every Workspace in the deployment (ADR 0045 decision 3-6: home is a single-AZ EBS volume,
// and losing it must not cost anyone their logins). Every read here is therefore an NFS
// round trip, which is why only the durable half lives here (ADR 0087 decision 4).
func AgentConfigDir() string {
	return filepath.Join(HomeDir(), ".config", "agent-fleet")
}

// AgentStateDir is the root for what the agent derives and can rebuild: per-session and
// per-turn state, the session ledger, the scratch working directories of chat runs. It is
// in the home volume (local disk on every runtime), and the fstore stores resolve through
// it — a session-list poll reads hundreds of these files every four seconds, and on EFS
// that measured 836 file syscalls per poll per open Console tab (ADR 0087 source A).
//
// The line between the two roots, in the order to apply it:
//
//  1. a credential, or a file that can carry one → AgentConfigDir (secrets.*, the legacy
//     plaintext credential files, mcp-tenant.json: a tenant-distributed server definition
//     arrives with header and env VALUES in it);
//  2. user-supplied configuration or user-authored content → AgentConfigDir (rtk.json,
//     ui-prefs.json, toolchains.json, user-notes*, assistants/, knowledge/, locks.json,
//     chats/ and the rest);
//  3. everything else → here. Anything keyed by session name or sid belongs on this side:
//     the session ledger itself moved, so state that outlives it is meaningless anyway.
//
// What moves here is lost with the EBS volume. That is the trade the ADR makes: an empty
// session list is recoverable in a way a lost credential store is not (the transcripts
// themselves sit under CLAUDE_CONFIG_DIR and are not affected either way).
//
// internal/statemig carries the one-way migration off the old location and the list of
// entries it moves; add to that list when adding a store here.
func AgentStateDir() string {
	return filepath.Join(HomeDir(), ".local", "state", "agent-fleet")
}

// ClaudeConfigDir resolves where the claude CLI reads/writes its state
// (settings.json, .claude.json, projects/*.jsonl). P3-5 stage 2 relocates that tree out
// of home via CLAUDE_CONFIG_DIR; unset means the classic ~/.claude.
//
// It lives here rather than in internal/agents/claude so a lower layer (the MCP
// registry's session materialize, docs/log/48 §8) can write the CLI's native config
// without importing an agent package — one resolver, no drifting copy.
func ClaudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".claude")
}

// CodexHome mirrors codex's own resolution: $CODEX_HOME, else ~/.codex (where the
// entrypoint seeds AGENTS.md and codex keeps config.toml). Same rationale as
// ClaudeConfigDir.
func CodexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".codex")
}

// OpencodeConfigDir is opencode's global config root (opencode.jsonc, plugin/,
// AGENTS.md). opencode itself resolves it through XDG_CONFIG_HOME, but the workspace
// never sets that variable and every other af writer into this tree (the rtk plugin,
// the entrypoint's permission block and AGENTS.md seed) spells it from HOME — so this
// stays HOME-based, and af's writes all land in the same directory.
func OpencodeConfigDir() string {
	return filepath.Join(HomeDir(), ".config", "opencode")
}

// CopilotHome is copilot's state root ($COPILOT_HOME, else ~/.copilot): config.json,
// mcp-config.json, session-state/.
func CopilotHome() string {
	if d := os.Getenv("COPILOT_HOME"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".copilot")
}

// FleetNotesPath is the workspace guide baked into the image
// (`workspace/workspace-notes.md` — the fleet layer of docs/log/60). The agent composes
// it into each CLI's global instruction file at startup; the env override exists so
// tests can point at a fixture instead of the image copy.
func FleetNotesPath() string {
	if p := os.Getenv("AF_WORKSPACE_NOTES"); p != "" {
		return p
	}
	return "/usr/local/share/agent-fleet/workspace-notes.md"
}

// FleetNotesDir holds the topic files behind the workspace guide (`workspace/notes/*.md`):
// the procedures the guide's index points at, read by an agent when it reaches that
// situation. The same files are registered as skills where a CLI has a user skills root
// (fleetskills), so the CLI's own index surfaces them. The env override mirrors
// AF_WORKSPACE_NOTES for tests.
func FleetNotesDir() string {
	if p := os.Getenv("AF_WORKSPACE_NOTES_DIR"); p != "" {
		return p
	}
	return "/usr/local/share/agent-fleet/notes"
}

// CursorHome is cursor's state root (~/.cursor): mcp.json, cli-config.json, projects/.
func CursorHome() string { return filepath.Join(HomeDir(), ".cursor") }

// KiroHome is kiro's config/session root (~/.kiro): settings/, agents/, sessions/cli/.
func KiroHome() string { return filepath.Join(HomeDir(), ".kiro") }

// GeminiHome is the tree agy inherits from its gemini-cli lineage (~/.gemini, hardcoded
// off $HOME): antigravity-cli/ for state and config/ for settings and mcp_config.json.
func GeminiHome() string { return filepath.Join(HomeDir(), ".gemini") }

// AgentDataDir is the per-user home volume for larger, persistent agent-fleet data
// (survives container recreate — ~/.local persists). Distinct from AgentConfigDir
// (small JSON state under ~/.config); used for the cleanup archive of removed
// sessions so tidy-up is recoverable.
func AgentDataDir() string {
	return filepath.Join(HomeDir(), ".local", "share", "agent-fleet")
}

// ExePath is the absolute path to this binary, used to build hook/MCP commands
// that resolve in an agent's hook context regardless of PATH. One resolver, so main and
// codex cannot drift apart (docs/log/23 remaining item 1 Wave F).
func ExePath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return InstalledExePath()
	}
	return exe
}

// InstalledExePath is where the workspace image puts the agent — the one path that
// outlives any single build of it. The env override exists for tests and for a host
// install that isn't in the image location.
func InstalledExePath() string {
	if p := os.Getenv("AF_AGENT_INSTALLED_BIN"); p != "" {
		return p
	}
	return "/usr/local/bin/workspace-agent"
}

// ConfigExePath is the agent path to write into ANOTHER program's persistent config
// (claude's statusLine and hooks, a CLI's MCP server command). Usually that is this
// binary — but a build running from a volatile directory (a dev build in /tmp, an
// e2e/smoke copy, anything under the scratch disk) must not pin ITS path there: the
// config outlives the binary, and once the file is gone the CLI just fails to run the
// command, silently. Measured: a smoke build in /tmp wrote `/tmp/af-agent statusline`
// into the shared settings.json, was deleted two minutes later, and the usage capture
// stopped dead — the chip then displayed a fabricated 0% for six hours. So when this
// binary is ephemeral, persist the installed one instead (which resolves its own
// state from the session's env exactly the same way).
func ConfigExePath() string {
	exe := ExePath()
	if !volatilePath(exe) {
		return exe
	}
	if fi, err := os.Stat(InstalledExePath()); err == nil && !fi.IsDir() {
		return InstalledExePath()
	}
	return exe // nothing installed (host/native dev): our own path is all there is
}

// ExeUnusable reports whether an agent path already recorded in a config can no
// longer be relied on — it is gone, or it lives where it will be wiped out from
// under the config. Callers repoint such a command at ConfigExePath.
func ExeUnusable(p string) bool {
	if p == "" {
		return true
	}
	if volatilePath(p) {
		return true
	}
	_, err := os.Stat(p)
	return err != nil
}

// volatilePath reports whether p sits under a directory whose contents do not
// survive: the temp dirs, and the workspace scratch disk (wiped on every stop).
func volatilePath(p string) bool {
	roots := []string{os.TempDir(), "/tmp", "/var/tmp"}
	if s := os.Getenv("AF_WS_SCRATCH"); s != "" {
		roots = append(roots, s)
	}
	for _, r := range roots {
		if r == "" || r == "/" {
			continue
		}
		if strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

// ValidIDSegment guards path traversal for files named after one of our own ids:
// conversation ids, assistant ids, and anything else built from randUUID() (36 characters of
// hex and '-'). Callers must run the id through this immediately before turning it into a
// file name.
//
// It lives here so there is one implementation: the check was once split between
// chat_store.go's validConvID and internal/assistants' validID, and loosening only one of
// them would have gone unnoticed.
func ValidIDSegment(id string) bool {
	if len(id) != 36 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-') {
			return false
		}
	}
	return true
}

// ImagegenStudiosDir holds the image studios, one `<id>.json` each (ADR 0100 decision 2), plus
// each studio's append-only `<id>.log.jsonl`. Under AgentConfigDir because a studio is
// user-authored content a Workspace must not lose, and inside the Files pane's denylist on
// purpose: the draft is edited through the studio, never as a file. Two processes read it —
// the Agent that owns it and the session's MCP child, which decides what to advertise from it
// without a round trip — so the path lives here rather than in either package. The id is a
// ValidIDSegment.
func ImagegenStudiosDir() string { return filepath.Join(AgentConfigDir(), "imagegen", "studios") }

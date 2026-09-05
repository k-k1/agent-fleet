package copilot

// Assembling copilot's launch command, and resolving the state directory ($COPILOT_HOME,
// default ~/.copilot). resume can pin the sid itself the way claude does (--session-id), so
// there is no need to capture a conversation UUID as agy/codex do — one set of flags covers
// both new and resumed sessions (measured: an existing id resumes, an unknown valid v4 is
// created).

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr duplicates the helper of the same name in package main; it is small enough that the
// duplication is preferred to sharing it.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Home is copilot's state root ($COPILOT_HOME, default ~/.copilot): config.json
// (trustedFolders), mcp-config.json, logs/, session-store.db and
// session-state/<sid>/. The tree is denylisted from the file browser (fs.go):
// in a container with no keychain the auth token can end up stored in plaintext
// (upstream docs).
func Home() string { return paths.CopilotHome() }

func configPath() string { return filepath.Join(Home(), "config.json") }

// sessionStateDir is the per-session state dir holding events.jsonl (the read source of truth).
func sessionStateDir(sid string) string { return filepath.Join(Home(), "session-state", sid) }

// EventsPath is the session's events.jsonl — the transcript/state source shared
// by every mode (TUI / -p / ACP managed) — docs/log/36 measurement record.
func EventsPath(sid string) string { return filepath.Join(sessionStateDir(sid), "events.jsonl") }

// defaultFlags is the fleet-standard permission/privacy posture:
//   - --allow-all: the fleet-default bypass equivalent (the peer of claude's
//     skip-permissions).
//   - --no-remote --no-remote-export: a session's GitHub sync and remote steering are off by
//     default (keeps the conversation from leaving the fleet and prevents double steering —
//     the docs/log/36 contract).
const defaultFlags = "--allow-all --no-remote --no-remote-export"

// buildProgram returns the tmux pane program for a copilot session. Auth is ambient (copilot
// picks up gh's transparent-auth token itself — measured), so no token is injected.
// --session-id covers creating a new session and resuming one in the same shape.
// bypass=false means "do not skip the permission prompts" (the user's choice from
// docs/log/76, or a plan launch), and only --allow-all is dropped. --no-remote /
// --no-remote-export (keeping the conversation inside the fleet and preventing double
// steering) sit on a different axis from permission prompts, so they always stay.
func buildProgram(model, effort, mode, sid string, bypass bool) string {
	if override := os.Getenv("AGENT_COPILOT_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_COPILOT_FLAGS", defaultFlags)
	if !bypass {
		// The TUI's own permission menu is what the user drives from the terminal or the mirror.
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--allow-all", ""))
	}
	if mode == "plan" {
		flags = strings.TrimSpace(flags + " --mode plan")
	}
	concreteModel := model != "" && model != "auto"
	if concreteModel {
		flags += " --model " + session.ShellQuote(model)
	}
	// Auto (copilot's default, and the ONLY model on the Free plan) rejects --effort:
	// "Model \"auto\" does not support reasoning effort configuration". Pass effort only
	// alongside an explicit non-auto model, else the session errors on launch.
	if effort != "" && concreteModel {
		flags += " --effort " + session.ShellQuote(effort)
	}
	if sid != "" {
		flags += " --session-id " + session.ShellQuote(sid)
	}
	return strings.TrimSpace("copilot " + flags)
}

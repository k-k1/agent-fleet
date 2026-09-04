package cursor

// Launch-command assembly for cursor, plus resolution of its state and transcript paths
// (docs/log/40 Track A).
//
// Session identity is a v4 UUID minted on the AF side and passed as `--resume <uuid>`.
// Measured (v2026.07.20): an unknown but valid v4 UUID makes cursor create a new chat under
// that ID, and an existing ID resumes — the same shape as copilot's --session-id. Minting
// it ourselves avoids the extra launch-time exec that docs/log/40's `create-chat`
// pre-allocation would have needed. Transcripts are Claude Code-compatible JSONL
// (`<chatId>.jsonl`) under the cwd slug (see transcriptPath for the measured path).

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr is a tiny helper, duplicated rather than shared (copilot/program.go keeps its own).
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Home is cursor's state root (~/.cursor): chats/<ws-hash>/<chatId>/store.db (a private
// SQLite we never read), projects/<slug>/agent-transcripts/ (the JSONL we do read),
// hooks.json, cli-config.json. Credentials live in a separate tree
// (~/.config/cursor/auth.json); both trees are on fs.go's denylist so plaintext tokens
// stay unreadable (docs/log/40 contract).
func Home() string { return paths.CursorHome() }

// projectsDir is ~/.cursor/projects — the per-cwd transcript tree root.
func projectsDir() string { return filepath.Join(Home(), "projects") }

// cwdSlug maps a cwd to cursor's projects slug. Measured: drop the leading and trailing
// "/" and turn the remaining ones into "-" (/tmp/curprobe -> tmp-curprobe). Only a first
// guess — if the slug rule drifts between versions, transcriptPath still resolves the file
// by globbing on the unique chatId.
func cwdSlug(dir string) string {
	return strings.ReplaceAll(strings.Trim(filepath.Clean(dir), "/"), "/", "-")
}

// transcriptPath resolves the Claude Code-compatible JSONL transcript for chatId
// launched in dir. chatId is globally unique, so a glob across every cwd slug finds
// it even if the slug rule drifts; the computed slug is only the fast first guess.
func transcriptPath(dir, chatID string) string {
	if chatID == "" {
		return ""
	}
	guess := filepath.Join(projectsDir(), cwdSlug(dir), "agent-transcripts", chatID, chatID+".jsonl")
	if _, err := os.Stat(guess); err == nil {
		return guess
	}
	// Slug rule drifted: glob on the chatId instead, which is unique.
	if hits, _ := filepath.Glob(filepath.Join(projectsDir(), "*", "agent-transcripts", chatID, chatID+".jsonl")); len(hits) > 0 {
		return hits[0]
	}
	return guess // Not written yet (just launched); callers treat a failed Stat as empty.
}

// disableAutoUpdateFlag suppresses the CLI's background self-update, keeping versions
// managed by image rebuild. It is a root option, so it must precede the ACP subcommand:
// measured, `cursor-agent --disable-auto-update acp` is accepted while
// `acp --disable-auto-update` is rejected (help hides the flag but the CLI takes it;
// default false). The bundle skips its background update — a setTimeout(...).unref() two
// seconds after launch — when `disableAutoUpdate || channel==="static"` (docs/log/40
// Track B). The entrypoint re-pins cli-config.json `channel:"static"` as well, so either
// half still holds when user settings break the other.
const disableAutoUpdateFlag = "--disable-auto-update"

// defaultFlags is the fleet-standard posture:
//   - --disable-auto-update: suppress the background self-update (versions come from
//     image rebuilds).
//   - --force: the fleet-default bypass equivalent ("unless explicitly denied" — the deny
//     list still applies; measured from help). On a par with claude's skip-permissions.
//   - --trust: skip the trust prompt for an untrusted workspace (measured from help); the
//     launch-time counterpart of copilot's pre-write into config.json.
const defaultFlags = disableAutoUpdateFlag + " --force --trust"

// bin returns the cursor CLI binary: the `cursor-agent` symlink, not `agent`, which is
// short enough to collide on PATH. AGENT_CURSOR_BIN overrides it.
func bin() string { return envOr("AGENT_CURSOR_BIN", "cursor-agent") }

// Bin exposes the resolved cursor CLI binary for the assistant-chat headless
// backend (chat_providers.go cursorChat), which shells out `cursor-agent -p`
// from the main package and must honor the same AGENT_CURSOR_BIN override.
func Bin() string { return bin() }

// buildProgram returns the tmux pane program for a cursor TUI session. Auth is ambient —
// the CLI picks up ~/.config/cursor/auth.json itself (measured) — so no token is injected.
// --resume covers creating a new chat and resuming one in the same shape.
// bypass=false means permission prompts are not skipped (the user's choice per docs/log/76,
// or a plan start). Only --force is dropped; --trust (workspace trust) must always stay:
// the trust prompt is not a permission prompt, and without it both ACP and TUI launches
// hang (measured).
func buildProgram(model, mode, chatID string, bypass bool) string {
	if override := os.Getenv("AGENT_CURSOR_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_CURSOR_FLAGS", defaultFlags)
	if !bypass {
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--force", ""))
	}
	if mode == "plan" {
		// Plan assumes no bypass: auto-approving every tool would defeat a plan start
		// (the same call as copilot/agy).
		flags = strings.TrimSpace(flags + " --plan")
	}
	// cursor model IDs already carry the effort (claude-opus-4-8-thinking-high) and there
	// is no separate --effort flag (measured from help), so effort is never passed. "auto"
	// is the default and means no flag at all.
	if model != "" && model != "auto" {
		flags += " --model " + session.ShellQuote(model)
	}
	if chatID != "" {
		flags += " --resume " + session.ShellQuote(chatID)
	}
	// With CI still set the interactive UI disappears entirely (ci_env.go). Not applied to
	// an AGENT_CURSOR_CMD override: that escape hatch runs the given command verbatim and
	// never reaches this assembly (early return above).
	return unsetCIPrefix + strings.TrimSpace(bin()+" "+flags)
}

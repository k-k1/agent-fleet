package kiro

// Building kiro's launch command, plus resolving the session store's paths (v2 JSONL) and
// discovering a sid (docs/log/43 Track A).
//
// The session id is allocated by the CLI: unlike cursor, AF cannot impose one (see
// kiro.go). The launch is `kiro-cli chat --agent-engine v2 --trust-all-tools [--model …]
// [--effort …] [--resume-id …]`. --agent-engine v2 is pinned explicitly as insurance
// against drift: the default is v2 today, but it could swing to v3 (docs/log/43 §5-2).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// envOr is a tiny helper, duplicated rather than shared (as in cursor/program.go).
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// bin returns the kiro CLI binary. AGENT_KIRO_BIN overrides it (tests, alternate paths).
func bin() string { return envOr("AGENT_KIRO_BIN", "kiro-cli") }

// Bin exposes the resolved kiro CLI binary for callers outside this package.
func Bin() string { return bin() }

// Installed reports whether the kiro CLI is present on PATH (baked or already
// on-demand installed). Used by the connection card's install flow (docs/log/43 Track C)
// to decide whether the ~855MB bundle still needs to land in ~/.local.
func Installed() bool {
	_, err := exec.LookPath(bin())
	return err == nil
}

// Home is kiro's config/session root (~/.kiro): settings/cli.json, settings/mcp.json,
// agents/, and sessions/cli/ (the v2 session store). Credentials live in a separate tree
// (~/.local/share/kiro-cli/data.sqlite3); both are on fs.go's denylist.
func Home() string { return paths.KiroHome() }

// sessionsDir is ~/.kiro/sessions/cli — the flat v2 session store (per-sid
// <sid>.json meta + <sid>.jsonl transcript + <sid>.lock + <sid>.history).
func sessionsDir() string { return filepath.Join(Home(), "sessions", "cli") }

// sessionJSONPath / transcriptPath resolve a session's meta and transcript files.
func sessionJSONPath(sid string) string { return filepath.Join(sessionsDir(), sid+".json") }
func transcriptPath(sid string) string  { return filepath.Join(sessionsDir(), sid+".jsonl") }

// sessionMeta is the subset of <sid>.json we read to attribute a session to a cwd
// and fence it by creation time.
type sessionMeta struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	CreatedAt string `json:"created_at"` // RFC3339 — when kiro minted the session
}

// discoverSid finds the newest v2 session launched in dir AT OR AFTER notBefore. kiro
// writes <sid>.json at session start (before the first turn — measured), so a fresh
// launch's session is the newest for its cwd; once resolveSid caches it, the choice
// sticks.
//
// The notBefore fence is essential (A-1): recreate cuts a NEW slug into the SAME dir,
// so the predecessor's <sid>.json always lingers there. Without the fence, a fresh
// launch's mirror poll would adopt that predecessor during the window before kiro
// writes its own <sid>.json (minutes, if this is the on-demand first install), cache
// it permanently, break the start-empty contract, and hand `--resume-id <old sid>` to
// the next resume — continuing an unrelated conversation. Fenced to the slot's
// creation time (kiro's session created_at ≥ the slot's CreatedAt, same host clock),
// the predecessor is excluded and discovery simply returns "" until the real session
// exists. Same-cwd collisions between two LIVE slots remain the known edge — worktrees
// give distinct dirs, the fleet's parallel-isolation mechanism.
//
// Recency = the .json file's mtime (rewritten each turn). notBefore zero = no fence
// (unparseable slot CreatedAt — degrade to the pre-A-1 behavior rather than never
// resolving).
func discoverSid(dir string, notBefore time.Time) string {
	want := filepath.Clean(dir)
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return ""
	}
	best, bestMod := "", int64(-1)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(sessionsDir(), e.Name())
		var sm sessionMeta
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &sm) != nil {
			continue
		}
		if filepath.Clean(sm.Cwd) != want {
			continue
		}
		if !notBefore.IsZero() {
			ct, err := time.Parse(time.RFC3339, sm.CreatedAt)
			if err != nil || ct.Before(notBefore) {
				continue // predecessor (or undatable) session — never adopt it
			}
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if m := fi.ModTime().UnixNano(); m > bestMod {
			best, bestMod = strings.TrimSuffix(e.Name(), ".json"), m
		}
	}
	return best
}

// defaultFlags is the fleet-standard posture for a TUI launch:
//   - chat: the interactive subcommand.
//   - --agent-engine v2: pin the v2 engine. The v2 JSONL store is what this
//     implementation reads, and v3 could be a different store with a different state
//     contract, so the default must not be allowed to swing there on its own.
//   - --trust-all-tools: the fleet's default bypass posture, the counterpart of claude's
//     skip-permissions. The first-run dangerous-mode confirmation dialog is suppressed by
//     chat.disableTrustAllConfirmation, which ensureSettings pins.
const defaultFlags = "chat --agent-engine v2 --trust-all-tools"

// buildProgram returns the tmux pane program for a kiro TUI session. No token is
// injected: authentication is environmental, since the CLI picks up
// ~/.local/share/kiro-cli itself (measured). bypass=false means "do not skip the
// permission prompts" (the user's choice per docs/log/76, or a plan launch); state.go
// then picks a pending approval up as "question" from the explicit text "requires
// approval". Only --trust-all-tools is removed — chat.disableTrustAllConfirmation
// (suppressing the first-run dangerous-mode dialog, pinned by ensureSettings) is a
// different axis and stays.
func buildProgram(model, effort, mode, resumeID string, bypass bool) string {
	if override := os.Getenv("AGENT_KIRO_CMD"); override != "" {
		return override
	}
	flags := envOr("AGENT_KIRO_FLAGS", defaultFlags)
	if !bypass {
		flags = strings.TrimSpace(strings.ReplaceAll(flags, "--trust-all-tools", ""))
	}
	// "auto" is the default (1M ctx) and takes no flag. A named model is selectable even
	// on Free (measured).
	if model != "" && model != "auto" {
		flags += " --model " + session.ShellQuote(model)
	}
	if effort != "" {
		flags += " --effort " + session.ShellQuote(effort)
	}
	if resumeID != "" {
		flags += " --resume-id " + session.ShellQuote(resumeID)
	}
	core := strings.TrimSpace(bin() + " " + flags)
	// On-demand first-use install (docs/log/43 Track B / §4-2). kiro's ~855MB bundle is
	// NOT baked or boot-installed for everyone; it lands in the user's ~/.local the
	// first time they actually launch kiro. tmux runs the pane program via /bin/sh,
	// so guard the launch: install (progress visible in the pane), then run it.
	//
	// The guard is unconditional — NOT `command -v kiro-cli ||` as it once was. That
	// form only ever covered "missing", so the copy in the home volume stayed on the
	// version of its first install forever: it survives image rebuilds, has no
	// boot-install to refresh it, and its self-updater is pinned off. A versions.json
	// pin bump therefore never reached the user. `install-kiro --if-needed` decides
	// for itself: silent (a marker stat) when the pinned version is already there,
	// re-install with visible progress when the installed version drifts from the pin.
	// Failure falls through to launching whatever is installed (`;`, not `&&`).
	// Only for the default binary — an AGENT_KIRO_BIN override (tests, alt paths)
	// skips the bootstrap so it never triggers a real install.
	if bin() == "kiro-cli" {
		return "workspace-agent install-kiro --if-needed; " + core
	}
	return core
}

// ensureSettings pins the two fleet-required kiro settings ONCE per process, best
// effort:
//   - app.disableAutoupdates=true — the version is managed by image rebuild and on-demand
//     install, so background self-update is off. The entrypoint pins it too; this is the
//     backstop that covers both the lean and the full image.
//   - chat.disableTrustAllConfirmation=true — suppresses the first-run dangerous-mode
//     dialog for --trust-all-tools (measured: without it the launch pane sticks on the
//     dialog).
//
// Install (Track B) pinning both settings is the primary path; doing it idempotently here
// in the read layer keeps a launch from sticking on a bare home as well. No-op when the
// binary is absent.
var settingsOnce sync.Once

func ensureSettings() {
	settingsOnce.Do(func() {
		if _, err := exec.LookPath(bin()); err != nil {
			return
		}
		for _, kv := range [][2]string{
			{"app.disableAutoupdates", "true"},
			{"chat.disableTrustAllConfirmation", "true"},
		} {
			_ = exec.Command(bin(), "settings", kv[0], kv[1]).Run()
		}
	})
}

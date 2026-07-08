package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// The Agent interface and its input/output types live in internal/agents
// (docs/23 残① Wave C); this file keeps the registry, the shared live-state
// helpers, and the sid stores until the per-CLI impls move out in later waves.

// agentRegistry is the kind → agents.Agent registry. agentOf falls back to claude
// for an unknown or empty kind, matching the historical default (a session with no
// recognized kind launches claude).
var agentRegistry = map[string]agents.Agent{
	session.KindClaude:   claudeAgent{},
	session.KindOpencode: opencodeAgent{},
	session.KindCodex:    codexAgent{},
	session.KindShell:    shellAgent{},
	session.KindSSM:      ssmAgent{},
}

func agentOf(kind string) agents.Agent {
	if a, ok := agentRegistry[kind]; ok {
		return a
	}
	return agentRegistry[session.KindClaude]
}

// normalizeKind maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func normalizeKind(kind string) string {
	if _, ok := agentRegistry[kind]; ok {
		return kind
	}
	return session.KindClaude
}

// --- shared live-state helpers -------------------------------------------------

// driveState is the live state for the drive endpoints (status/output/messages):
// "stopped" when not alive, else idle-or-recorded. heal self-corrects a stale
// non-idle cache when the claude pane is back at its ready prompt (killed+resumed,
// rejected permission, abandoned question) — /output opts out (heal=false) to match
// its historical behavior.
func driveState(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	// opencode: derive state from its own store (the status plugin is unreliable) so the
	// chat chip doesn't stick on 進行中 after a turn the plugin never reported idle for.
	if m.Kind == session.KindOpencode {
		if st := opencodeLiveState(m); st != "" {
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	state := status.LiveState(sid)
	if heal && state != "idle" && tmuxx.AtIdlePrompt(m.Name) {
		state = "idle"
		status.Remove(sid)
	}
	return state
}

// statusOnlyLive is the WireLive body shared by opencode/codex: state from the
// status store (no idle-heal, no background-busy), and resumable unless the working
// dir is gone.
func statusOnlyLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	if alive {
		li.State = status.LiveState(session.UUID(m.Dir, m.Name))
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

// --- sid store -----------------------------------------------------------------

// sidStore maps our deterministic slot sid to an agent's own session id, so a slot
// resumes its OWN conversation (internal/fstore の Store に薄い読み口を被せたもの:
// read は ok を潰して "" を返す — 呼び出し側は空文字を「無し」として扱う)。
type sidStore struct{ files fstore.Store[string] }

func (s sidStore) read(sid string) string {
	v, _ := s.files.Read(sid)
	return v
}

func (s sidStore) write(sid, val string) { _ = s.files.Write(sid, val) }
func (s sidStore) remove(sid string)     { s.files.Remove(sid) }

var (
	// opencode: written externally by the bundled plugin (on session.created, keyed
	// by AF_SESSION_SID); the agent only reads/removes it.
	opencodeSids = sidStore{fstore.TrimmedStrings(paths.AgentConfigDir, "opencode-sid")}
	// codex: written by the session-status hook from codex's own session_id (codex
	// has no --session-id flag to pin), read for `codex resume <id>`.
	codexSids = sidStore{fstore.TrimmedStrings(paths.AgentConfigDir, "codex-sid")}
)

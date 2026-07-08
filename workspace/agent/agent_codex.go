package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// codexAgent — codex 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）

// --- codex ---------------------------------------------------------------------

type codexAgent struct{}

func (codexAgent) Kind() string { return session.KindCodex }

// CanTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via Transcript() (readCodexTranscript), windowed by the generic
// /messages handler. No fork/label (codex has no --session-id pin nor --name).
func (codexAgent) Caps() agents.Caps { return agents.Caps{CanTranscript: true} }

func (codexAgent) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readCodexTranscript(m)
}

func (codexAgent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureCodexFolderTrusted(m.Dir)
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildCodexProgram.
	cxSid := session.UUID(m.Dir, m.Name)
	return agents.LaunchPlan{Program: buildCodexProgram(m.Model, cxSid, codexSids.Read(cxSid)), Cwd: m.Dir}, nil
}

func (codexAgent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid.
	return statusOnlyLive(m, alive)
}

func (codexAgent) ClearResume(sid string) { codexSids.Remove(sid) }

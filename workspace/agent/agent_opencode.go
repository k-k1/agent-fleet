package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// opencodeAgent — opencode 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）

// --- opencode ------------------------------------------------------------------

type opencodeAgent struct{}

func (opencodeAgent) Kind() string { return session.KindOpencode }

// CanTranscript lights up the Console chat mirror for opencode; its turns come from the
// SQLite store via Transcript() (readOpencodeTranscript), windowed by the generic
// /messages handler. No fork/label/inline-questions (those are claude-specific).
func (opencodeAgent) Caps() agents.Caps { return agents.Caps{CanTranscript: true} }

func (opencodeAgent) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readOpencodeTranscript(m)
}

func (opencodeAgent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// opencode resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// AF_SESSION_SID lets the bundled opencode plugin report this session's
	// working/idle state back keyed by OUR deterministic sid (same store claude
	// uses), so wireSession can surface it. Provider API keys are injected as env
	// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file. The
	// env is prefixed onto the command itself (not tmux -e, which sets only the
	// session environment and does NOT reach the pane's process).
	ocSid := session.UUID(m.Dir, m.Name)
	envs := append([]string{"AF_SESSION_SID=" + ocSid}, opencodeEnv()...)
	// Resume the slot's current opencode conversation, resolved from the store itself
	// (plugin-independent — see opencodeActiveSession), UNLESS its last turn was
	// interrupted (incomplete). opencode continues an incomplete turn on resume, re-running
	// the pending work (e.g. an Explore subagent the user stopped); starting fresh avoids
	// that. The interrupted conversation stays in the store, just not auto-resumed.
	resume := ""
	if db, ok := opencodeOpenRO(); ok {
		resume = opencodeActiveSession(db, m)
		db.Close()
	}
	if resume == "" {
		resume = opencodeSids.read(ocSid) // fallback when the store can't be read
	}
	if resume != "" && !opencodeSessionResumable(resume) {
		resume = ""
	}
	return agents.LaunchPlan{Program: buildOpencodeProgram(m.Model, envs, resume), Cwd: m.Dir}, nil
}

func (opencodeAgent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State is derived from opencode's own store (opencodeLiveState) — robust against the
	// status plugin not firing — falling back to the plugin status file when the db can't
	// be read. Resumable unless the working dir is gone.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		if st := opencodeLiveState(m); st != "" {
			li.State = st
		} else {
			li.State = status.LiveState(session.UUID(m.Dir, m.Name))
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (opencodeAgent) ClearResume(sid string) { opencodeSids.remove(sid) }

package main

// opencodeAgent — opencode 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）

// --- opencode ------------------------------------------------------------------

type opencodeAgent struct{}

func (opencodeAgent) kind() string { return kindOpencode }

// canTranscript lights up the Console chat mirror for opencode; its turns come from the
// SQLite store via transcript() (readOpencodeTranscript), windowed by the generic
// /messages handler. No fork/label/inline-questions (those are claude-specific).
func (opencodeAgent) caps() agentCaps { return agentCaps{canTranscript: true} }

func (opencodeAgent) transcript(m sessionMeta) (transcriptData, bool) {
	return readOpencodeTranscript(m)
}

func (opencodeAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// opencode resumes (or starts) in its real project dir; refuse if it's gone.
	if !dirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// AF_SESSION_SID lets the bundled opencode plugin report this session's
	// working/idle state back keyed by OUR deterministic sid (same store claude
	// uses), so wireSession can surface it. Provider API keys are injected as env
	// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file. The
	// env is prefixed onto the command itself (not tmux -e, which sets only the
	// session environment and does NOT reach the pane's process).
	ocSid := sessionUUID(m.Dir, m.Name)
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
	return launchPlan{program: buildOpencodeProgram(m.Model, envs, resume), cwd: m.Dir}, nil
}

func (opencodeAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	// State is derived from opencode's own store (opencodeLiveState) — robust against the
	// status plugin not firing — falling back to the plugin status file when the db can't
	// be read. resumable unless the working dir is gone.
	li := liveInfo{resumable: true}
	if alive {
		if st := opencodeLiveState(m); st != "" {
			li.state = st
		} else {
			li.state = liveStateFromStatus(sessionUUID(m.Dir, m.Name))
		}
	} else if !dirExists(m.Dir) {
		li.resumable = false
	}
	return li
}

func (opencodeAgent) clearResume(sid string) { opencodeSids.remove(sid) }

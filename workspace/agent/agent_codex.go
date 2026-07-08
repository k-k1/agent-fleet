package main

// codexAgent — codex 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）

// --- codex ---------------------------------------------------------------------

type codexAgent struct{}

func (codexAgent) kind() string { return kindCodex }

// canTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via transcript() (readCodexTranscript), windowed by the generic
// /messages handler. No fork/label (codex has no --session-id pin nor --name).
func (codexAgent) caps() agentCaps { return agentCaps{canTranscript: true} }

func (codexAgent) transcript(m sessionMeta) (transcriptData, bool) {
	return readCodexTranscript(m)
}

func (codexAgent) buildLaunch(m sessionMeta, _ launchOpts) (launchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !dirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureCodexFolderTrusted(m.Dir)
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildCodexProgram.
	cxSid := sessionUUID(m.Dir, m.Name)
	return launchPlan{program: buildCodexProgram(m.Model, cxSid, codexSids.read(cxSid)), cwd: m.Dir}, nil
}

func (codexAgent) wireLive(m sessionMeta, alive bool) liveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid.
	return statusOnlyLive(m, alive)
}

func (codexAgent) clearResume(sid string) { codexSids.remove(sid) }

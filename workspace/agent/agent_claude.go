package main

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// claudeAgent — claude 種別の Agent 実装（docs/23 P1残: agent.go の CLI 縦割りファイル分割）

// --- claude --------------------------------------------------------------------

type claudeAgent struct{ noGenericTranscript }

func (claudeAgent) kind() string { return session.KindClaude }

func (claudeAgent) caps() agentCaps {
	return agentCaps{canFork: true, canTranscript: true, usesLabel: true}
}

func (claudeAgent) buildLaunch(m session.Meta, _ launchOpts) (launchPlan, error) {
	// A claude session must launch in its real working dir: if the dir is gone (its
	// repo was deleted) we refuse rather than resume the conversation in an unrelated
	// cwd. wireSession reports this as non-resumable.
	if !session.DirExists(m.Dir) {
		return launchPlan{}, dirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so claude doesn't stall on the folder-trust dialog
	// (not skippable via --dangerously-skip-permissions).
	ensureFolderTrusted(m.Dir)
	sid := session.UUID(m.Dir, m.Name)
	// A jsonl can exist yet hold no real conversation — e.g. only a Remote Control
	// "bridge-session" line when RC connected but nothing was said. claude --resume
	// then dies with "No conversation found". Drop such a stub so buildSessionProgram
	// starts fresh (--session-id) instead of resuming.
	if !jsonlResumable(sid) {
		for _, p := range jsonlPaths(sid) {
			_ = os.Remove(p)
		}
	}
	// No env token is injected: the interactive TUI authenticates from claude's own
	// .credentials.json, written by `claude auth login` via the Connections flow
	// (claude_auth.go). CLAUDE_CODE_OAUTH_TOKEN is headless-only.
	return launchPlan{program: buildSessionProgram(sid, m.Model, m.Label, m.ForkFrom), cwd: m.Dir}, nil
}

func (claudeAgent) wireLive(m session.Meta, alive bool) liveInfo {
	li := liveInfo{resumable: true}
	sid := session.UUID(m.Dir, m.Name)
	li.remoteURL = remoteSessionURL(sid)
	li.context = latestSessionContext(sid)
	if alive {
		// Default a live claude with no recorded event yet to idle (it sits at the
		// prompt waiting for input). Hook events refine it.
		li.state = status.LiveState(sid)
		// Self-heal a stale cache: a non-idle state that no longer matches the terminal
		// (killed+resumed, rejected permission, abandoned question) — if the pane is
		// back at the ready prompt, it's idle.
		if li.state != "idle" && tmuxx.AtIdlePrompt(m.Name) {
			li.state = "idle"
			status.Remove(sid)
		}
		// Idle by hook, but a run_in_background task may still be running under the
		// pane — surface that so 入力待ち isn't mistaken for "done".
		if li.state == "idle" {
			li.backgroundBusy = sessionBackgroundBusy(m.Name)
		}
	} else if !session.DirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.resumable = false
	}
	return li
}

func (claudeAgent) clearResume(string) {}

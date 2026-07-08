package main

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// claudeAgent — claude 種別の Agent 実装（docs/23 P1残: agent.go の CLI 縦割りファイル分割）

// --- claude --------------------------------------------------------------------

type claudeAgent struct{ agents.NoGenericTranscript }

func (claudeAgent) Kind() string { return session.KindClaude }

func (claudeAgent) Caps() agents.Caps {
	return agents.Caps{CanFork: true, CanTranscript: true, UsesLabel: true}
}

func (claudeAgent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// A claude session must launch in its real working dir: if the dir is gone (its
	// repo was deleted) we refuse rather than resume the conversation in an unrelated
	// cwd. wireSession reports this as non-resumable.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
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
	return agents.LaunchPlan{Program: buildSessionProgram(sid, m.Model, m.Label, m.ForkFrom), Cwd: m.Dir}, nil
}

func (claudeAgent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	sid := session.UUID(m.Dir, m.Name)
	li.RemoteURL = remoteSessionURL(sid)
	li.Context = latestSessionContext(sid)
	if alive {
		// Default a live claude with no recorded event yet to idle (it sits at the
		// prompt waiting for input). Hook events refine it.
		li.State = status.LiveState(sid)
		// Self-heal a stale cache: a non-idle state that no longer matches the terminal
		// (killed+resumed, rejected permission, abandoned question) — if the pane is
		// back at the ready prompt, it's idle.
		if li.State != "idle" && tmuxx.AtIdlePrompt(m.Name) {
			li.State = "idle"
			status.Remove(sid)
		}
		// Idle by hook, but a run_in_background task may still be running under the
		// pane — surface that so 入力待ち isn't mistaken for "done".
		if li.State == "idle" {
			li.BackgroundBusy = sessionBackgroundBusy(m.Name)
		}
	} else if !session.DirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.Resumable = false
	}
	return li
}

func (claudeAgent) ClearResume(string) {}

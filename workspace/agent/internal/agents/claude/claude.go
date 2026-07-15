// Package claude は claude CLI 種別の縦割りパッケージ（docs/23 残① Wave F: 最大の
// 縦割り）。Agent 実装・起動コマンド組み立て・jsonl transcript 解析・auth/settings/
// usage の Connections/Console ハンドラ・status hook 配線・コンテキスト充填率・
// バックグラウンド実行検知を package main から移設した。挙動・ワイヤ・ディスクは
// main 時代とバイト同一を維持すること。session-status サブコマンドの入口
// （hook stdin の解読と pending payload 適用）は main に残る。
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// New returns the claude Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl — claude 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）
type agentImpl struct{ agents.NoGenericTranscript }

func (agentImpl) Kind() string { return session.KindClaude }

func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanFork: true, CanTranscript: true, UsesLabel: true}
}

// ForkSource resolves this session's conversation id (its deterministic sid) as the
// fork source, refusing when the jsonl holds no real conversation yet — `claude
// --resume` would die with "No conversation found".
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := session.UUID(m.Dir, m.Name)
	if !JSONLResumable(sid) {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return sid, nil
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
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
	// then dies with "No conversation found". Drop such a stub so buildProgram
	// starts fresh (--session-id) instead of resuming.
	if !JSONLResumable(sid) {
		for _, p := range jsonlPaths(sid) {
			_ = os.Remove(p)
		}
	}
	// No env token is injected: the interactive TUI authenticates from claude's own
	// .credentials.json, written by `claude auth login` via the Connections flow
	// (auth.go). CLAUDE_CODE_OAUTH_TOKEN is headless-only.
	return agents.LaunchPlan{Program: buildProgram(sid, m.Model, m.Effort, m.Mode, m.Label, m.ForkFrom), Cwd: m.Dir}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	sid := session.UUID(m.Dir, m.Name)
	li.RemoteURL = RemoteSessionURL(sid)
	li.Context = latestContext(sid)
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
		// Idle by hook, but background work may still be running — surface it so 入力待ち
		// isn't mistaken for "done". BackgroundBusy sees run_in_background worker
		// processes under the pane; SubagentBusy sees in-process background subagents /
		// Workflow agents (which spawn no such process) via their transcript freshness.
		if li.State == "idle" {
			li.BackgroundBusy = BackgroundBusy(m.Name) || SubagentBusy(sid)
		}
	} else if !session.DirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(string) {}

// RemoteSessionURL derives the claude.ai Remote Control page for sid from its
// jsonl "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". We read only the
// head of the log (the bridge line is written at session start) to stay cheap on
// the polled list. Returns "" when there is no bridge (RC off / not yet connected).
func RemoteSessionURL(sid string) string {
	for _, p := range jsonlPaths(sid) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		buf := make([]byte, 64*1024)
		n, _ := f.Read(buf)
		f.Close()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !strings.Contains(line, `"type":"bridge-session"`) {
				continue
			}
			var b struct {
				BridgeSessionID string `json:"bridgeSessionId"`
			}
			if json.Unmarshal([]byte(line), &b) == nil && b.BridgeSessionID != "" {
				return "https://claude.ai/code/session_" + strings.TrimPrefix(b.BridgeSessionID, "cse_")
			}
		}
	}
	return ""
}

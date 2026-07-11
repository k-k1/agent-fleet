// Package codex は codex CLI 種別の縦割りパッケージ（docs/23 残① Wave E）。
// Agent 実装・起動コマンド組み立て・rollout JSONL transcript 読み出し・auth/usage
// の Connections ハンドラ・rtk ブロック適用を package main から移設した。
// 挙動・ワイヤ・ディスクは main 時代とバイト同一を維持すること。
package codex

import (
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// sids maps our deterministic slot sid to codex's own session id: written by the
// session-status hook (RememberSid — codex has no --session-id flag to pin), read
// for `codex resume <id>`.
var sids = agents.NewSidStore("codex-sid")

// RememberSid records the slot sid → codex session id mapping. Called from the
// session-status hook entrypoint in package main when codex's hook JSON carries
// its own session_id.
func RememberSid(slotSid, codexID string) { sids.Write(slotSid, codexID) }

// New returns the codex Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl — codex 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）
type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCodex }

// CanTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via Transcript() (readTranscript), windowed by the generic /messages
// handler. CanFork: the conversation forks via `codex fork <id>` (ForkSource /
// BuildLaunch). No label (codex has no --name).
func (agentImpl) Caps() agents.Caps { return agents.Caps{CanTranscript: true, CanFork: true} }

// ForkSource resolves this session's codex conversation id as the fork source —
// the hook-captured per-slot id, provided its rollout actually exists on disk.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	id := sids.Read(session.UUID(m.Dir, m.Name))
	if id == "" || rolloutPath(id) == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return id, nil
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscript(m)
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureFolderTrusted(m.Dir)
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildProgram.
	cxSid := session.UUID(m.Dir, m.Name)
	// First launch of a forked slot: no own captured session yet — fork the source
	// conversation (`codex fork <id>`). The injected hooks record the fork's own id
	// on the first prompt, so later launches resume it and ForkFrom is ignored.
	forkFrom := ""
	if sids.Read(cxSid) == "" {
		forkFrom = m.ForkFrom
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, cxSid, sids.Read(cxSid), forkFrom), Cwd: m.Dir}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid (the status
	// store; no idle-heal, no background-busy). Resumable unless the working dir is gone.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		li.State = status.LiveState(session.UUID(m.Dir, m.Name))
		// The hooks report only working/idle — a request_user_input dialog keeps the
		// turn "working" forever. Probe the rollout tail so the sessions list shows
		// 質問あり (and notifies) like claude; only while working, to keep the probe
		// off the common idle path.
		if li.State == "working" && HasPendingQuestion(m) {
			li.State = "question"
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

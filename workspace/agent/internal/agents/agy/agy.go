// Package agy は Antigravity CLI（agy）種別の縦割りパッケージ（docs/32 Track A）。
// codex パッケージの構成に倣い、Agent 実装・起動コマンド組み立て・auth の
// Connections ハンドラ・rtk ブロック適用を種別内に閉じる。実行方式は
// Terminal (CLI)/tmux 一択 — v1.1.4 に構造化出力が無く Managed は組めない
// （docs/decisions/0008）。ホスト要件（RDRAND・バイナリ有無）は internal/hostcaps
// が判定し、Status()/BuildLaunch がそれを配線する（docs/32 Track B 契約）。
package agy

import (
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/hostcaps"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to agy's conversation UUID, read back as
// `--conversation <UUID>` on resume. agy has no hooks to report its own id, so
// the UUID is adopted from its cwd→last-conversation map once it changes from
// the pre-launch snapshot — see captureConversation.
var sids = agents.NewSidStore("agy-sid")

// prelaunch snapshots the cwd's last-conversation UUID at fresh-launch time, so
// captureConversation can tell a conversation this slot created from a stale
// entry left by an earlier session in the same dir. `--continue` (= the raw
// cwd map) is NOT used for resume: any other agy run in the dir overwrites the
// mapping and slots would swap conversations (docs/32 Track D-3).
var prelaunch = agents.NewSidStore("agy-prelaunch")

// New returns the agy Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{ agents.NoGenericTranscript }

func (agentImpl) Kind() string { return session.KindAgy }

// M1 has no fork (agy exposes no fork affordance), no transcript mirror (the
// summaries SQLite is lazily written — docs/32 Track D-3), and no display label
// (agy has no --name).
func (agentImpl) Caps() agents.Caps { return agents.Caps{} }

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// Same host gate as the Console's kind selector: on a host where agy can't run
	// (binary absent / no RDRAND → SIGABRT at launch) refuse to build the pane
	// program instead of letting the session die on start (docs/32 Track B).
	if supported, reason := hostcaps.AgyStatus(); !supported {
		return agents.LaunchPlan{}, fmt.Errorf("agy はこのホストで利用できません（%s）", reason)
	}
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so agy doesn't stall on its "Do you trust the
	// contents of this project?" prompt (not skippable by flags).
	ensureWorkspaceTrusted(m.Dir)
	slotSid := session.UUID(m.Dir, m.Name)
	resumeID := sids.Read(slotSid)
	if resumeID == "" {
		// Fresh conversation: snapshot the dir's current map entry so the poll-time
		// capture only adopts a UUID this launch actually created.
		prelaunch.Write(slotSid, lastConversationFor(m.Dir))
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Mode, resumeID), Cwd: m.Dir}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// agy has no status hooks and its TUI chrome matches none of the claude-shaped
	// tmuxx idle/busy heuristics, so no live state is surfaced (like shell/ssm).
	li := agents.LiveInfo{Resumable: true}
	// Capture on BOTH sides of alive: v1.1.4 flushes the cwd→conversation map
	// only on a graceful exit (統合E2E実測), so for a session the user exited
	// themselves the UUID first becomes visible on a poll AFTER death. The
	// alive-side call stays for forward-compat should agy start flushing early.
	captureConversation(m)
	if !alive && !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

// captureConversation adopts the slot's conversation UUID from agy's cwd→last-
// conversation map once it differs from the pre-launch snapshot (i.e. this
// session has created its conversation). agy only flushes the map on graceful
// exit (v1.1.4), so the id typically lands via GracefulStop (halt) or the
// first sessions-list poll after the user's own /exit.
func captureConversation(m session.Meta) {
	slotSid := session.UUID(m.Dir, m.Name)
	if sids.Read(slotSid) != "" {
		return
	}
	if cur := lastConversationFor(m.Dir); cur != "" && cur != prelaunch.Read(slotSid) {
		sids.Write(slotSid, cur)
		prelaunch.Remove(slotSid)
	}
}

func (agentImpl) ClearResume(sid string) {
	sids.Remove(sid)
	prelaunch.Remove(sid)
}

// Package copilot は GitHub Copilot CLI（`copilot`, npm @github/copilot）種別の
// 縦割りパッケージ（docs/36 Track A）。read 層（Agent 実装・events.jsonl の
// transcript/状態読み）と managed driver（--acp: Agent Client Protocol JSON-RPC
// over stdio、per-session child — driver.go/acp.go）を種別内に閉じる。
//
// セッション同一性は AF 側で外部採番した UUID（`--session-id <uuid v4>`、TUI と
// ACP の session/load で共通）— agy の「resume UUID が取れない」問題（docs/32
// 46271bb）は構造的に発生しない。read 正本は $COPILOT_HOME/session-state/<sid>/
// events.jsonl（TUI・-p・ACP 全経路で同一形式・ライブ追記 — docs/36 実測記録）。
// 認証は GitHub 連携相乗り（gh 透過認証の OAuth トークン。Copilot CLI は
// COPILOT_GITHUB_TOKEN > GH_TOKEN > GITHUB_TOKEN と gh CLI アプリのトークンを
// 公式サポート）で、専用の Connections フローは持たない。
package copilot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to the copilot session UUID. Written at
// FRESH launch time (BuildLaunch / driver Resume) with an AF-generated v4 UUID —
// copilot accepts `--session-id <uuid>` to CREATE a session under that id and
// `session/load` resumes it, so there is no capture race to solve.
var sids = agents.NewSidStore("copilot-sid")

// New returns the copilot Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCopilot }

// The chat mirror IS supported: transcript.go reads session-state/<sid>/events.jsonl,
// which copilot appends live in every mode. CanFork/CanForkAt: copilot exposes no fork
// affordance of its own, so both are done by copying the session-state directory and
// truncating events.jsonl (forkat.go) — events.jsonl is the restore source (実測,
// docs/55 §55.5). No display label.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true}
}

// ForkSource resolves this session's copilot session id as the fork source, refusing when
// nothing has been recorded yet (a branch of an empty conversation is a new session).
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := SessionID(m)
	if sid == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := readEventLines(sid)
	if err != nil || !hasUserMessage(lines) {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return sid, nil
}

// ResolveForkAt validates the anchor against this session's own events.jsonl. Like
// claude, the value travels unchanged (the cut is expressed as "stop before this event"),
// and validating here rather than only at launch means a bad anchor is reported as such
// instead of as a session that starts and dies.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	sid := SessionID(m)
	if sid == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := readEventLines(sid)
	if err != nil {
		return "", errors.New("copilot の会話ログを読めません")
	}
	anchor := at.Anchor
	if at.Include {
		next, err := nextPromptID(lines, anchor)
		if err != nil {
			return "", err
		}
		if next == "" {
			return "", nil // 最後のやり取り = 全部残す（会話まるごとの経路へ）
		}
		anchor = next
	}
	// Dry run of the real surgery so this answer and the launch can never disagree.
	if _, err := forkEventLines(lines, anchor, sid, sid); err != nil {
		return "", err
	}
	return anchor, nil
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so the TUI doesn't stall on its "Confirm folder
	// trust" dialog (実測: config.json trustedFolders への事前追記でスキップ)。
	// 起動毎に再固定する（agy 5a19080 の教訓 — 一回きりの固定は後で剥がれる）。
	EnsureFolderTrusted(m.Dir)
	// 押し付けた id を copilot が使わなくなっていたら、起動前に拾い直す（sid.go）。
	// ここで直さないと `--session-id <使われていない id>` を渡し続け、ユーザーの会話は
	// どこからも参照されないまま取り残される。
	sid := resolveSid(m)
	if sid == "" {
		var err error
		if sid, err = newSessionID(); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("セッション ID を採番できません: %w", err)
		}
		sids.Write(session.UUID(m.Dir, m.Name), sid)
	}
	// First launch of a fork (docs/55): build our own session-state directory before
	// copilot starts, so the launch below is an ordinary `--session-id <sid>` resume.
	// A failure must NOT fall through to a fresh session — the user asked for a branch
	// carrying history, and starting empty looks like the branch silently lost it.
	if m.ForkFrom != "" && !sessionStateExists(sid) {
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("分岐を作成できませんでした: %w", err)
		}
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, m.Mode, sid), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// copilot has no status hooks; working/idle/question is derived from the
	// events.jsonl tail (state.go) — TUI 文字列非依存（false-idle 教訓に合致）。
	li := agents.LiveInfo{Resumable: true}
	if alive {
		// 生存ポーリングがドリフトの検知点（copilot に hook は無い）。resolveSid は
		// 台帳を直すので、以降の SessionID 読みが新しい会話を指す（sid.go）。
		resolveSid(m)
		if st := LiveState(m); st != "" {
			li.State = st
		}
	}
	if !alive && !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// SessionID returns the slot's copilot session UUID ("" when none allocated yet).
func SessionID(m session.Meta) string { return sids.Read(session.UUID(m.Dir, m.Name)) }

// newSessionID generates an RFC4122 v4 UUID. copilot VALIDATES the version/variant
// bits of --session-id (実測: 非 v4 は "The value is not a valid UUID" で起動失敗)。
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

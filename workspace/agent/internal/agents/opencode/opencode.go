// Package opencode は opencode CLI 種別の縦割りパッケージ（docs/23 残① Wave D:
// 最初の CLI 縦割り）。Agent 実装・起動コマンド組み立て・SQLite transcript 読み
// 出し・プロバイダ鍵の Connections ハンドラ・rtk プラグイン適用を package main
// から移設した。挙動・ワイヤ・ディスクは main 時代とバイト同一を維持すること。
package opencode

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// sids maps our deterministic slot sid to opencode's own session id ("ses_…"):
// written externally by the bundled plugin (on session.created, keyed by
// AF_SESSION_SID); the agent only reads/removes it.
var sids = agents.NewSidStore("opencode-sid")

// New returns the opencode Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl — opencode 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）
type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindOpencode }

// CanTranscript lights up the Console chat mirror for opencode; its turns come from the
// SQLite store via Transcript() (readTranscript), windowed by the generic /messages
// handler. CanFork: the conversation forks via `opencode --session <src> --fork`
// (ForkSource / BuildLaunch), aligning the fork affordance with claude. CanForkAt: the
// fork can also be pinned to a past turn — the serve API's `POST /session/{id}/fork`
// takes a messageID (docs/55). That route only exists on the managed driver, so the
// handler refuses a point fork for a CLI-route session rather than silently widening it.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true}
}

// ForkSource resolves this session's current opencode conversation as the fork
// source. An interrupted conversation is refused: opencode re-runs the incomplete
// turn on resume/fork, which is never what a fork should do.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	db, ok := openRO()
	if !ok {
		return "", errors.New("opencode の会話ストアを読めません")
	}
	ses := activeSession(db, m)
	db.Close()
	if ses == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	if !sessionResumable(ses) {
		return "", errors.New("中断中の会話は分岐できません（一度セッションを再開してから分岐してください）")
	}
	return ses, nil
}

// ResolveForkAt validates a mirror anchor against this session's OWN conversation.
// opencode's fork endpoint takes the messageID as-is and stops copying at the first
// message that sorts >= it (docs/55 §55.2), so the anchor needs no translation — the work
// here is refusing the anchors that would branch something other than what was pointed at.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	anchor := at.Anchor
	if anchor == "" {
		return "", errors.New("分岐点が指定されていません")
	}
	// 分岐点を渡せる口は serve の API 側にしかない（`--session <src> --fork` には無い）。
	if m.DriverKind() != session.DriverManaged {
		return "", fmt.Errorf("%w: opencode の発言時点からの分岐は managed のセッションでのみ利用できます",
			agents.ErrForkAtRoute)
	}
	db, ok := openRO()
	if !ok {
		return "", errors.New("opencode の会話ストアを読めません")
	}
	defer db.Close()
	ses := activeSession(db, m)
	if ses == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	var owner string
	if err := db.QueryRow(`SELECT session_id FROM message WHERE id = ?`, anchor).Scan(&owner); err != nil {
		return "", errors.New("指定された分岐点がこの会話に見つかりません")
	}
	// A sidechain (subagent) turn lives in a CHILD session, so its id is not part of the
	// parent's ordering at all: forking the parent "at" it would cut at an unrelated
	// place. The mirror only offers the affordance on the user's own turns, but the
	// anchor arrives from the client and has to be checked here too.
	if owner != ses {
		return "", errors.New("サブエージェントの発言からは分岐できません")
	}
	if !at.Include {
		return anchor, nil
	}
	// 「この発言の続きから」= 次のユーザー発言の手前まで。間に挟まる assistant
	// メッセージ（回答・ツール往復）はすべて引き継がれる。次が無い＝最後のやり取りなら
	// 会話まるごと（""）が正解 — 最後まで残すとはそういうこと。
	return nextUserMessageID(db, ses, anchor)
}

// nextUserMessageID returns the id of the first user message that sorts after anchor in
// ses. "" (with no error) when the anchor is the last exchange.
func nextUserMessageID(db *sql.DB, ses, anchor string) (string, error) {
	rows, err := db.Query(
		`SELECT id, data FROM message WHERE session_id = ? AND id > ? ORDER BY time_created, id`, ses, anchor)
	if err != nil {
		return "", errors.New("opencode の会話ストアを読めません")
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var data []byte
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var md struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(data, &md) == nil && md.Role == "user" {
			return id, nil
		}
	}
	return "", nil
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscript(m)
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// opencode resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// AF_SESSION_SID lets the bundled opencode plugin report this session's
	// working/idle state back keyed by OUR deterministic sid (same store claude
	// uses), so wireSession can surface it. Provider API keys are injected as env
	// (ANTHROPIC_API_KEY, …) so opencode authenticates without a plaintext file. The
	// env rides LaunchPlan.Env → `tmux new-session -e` (reaches the pane process;
	// verified on tmux 3.3a) so the keys never appear in the command string /
	// /proc/*/cmdline / pane_start_command.
	ocSid := session.UUID(m.Dir, m.Name)
	envs := append([]string{"AF_SESSION_SID=" + ocSid}, env()...)
	// Resume the slot's OWN opencode conversation (activeSession: the plugin-captured
	// per-slot id, else a store-derived conversation this slot itself opened — never an
	// older one from the same dir), UNLESS its last turn was interrupted (incomplete).
	// opencode continues an incomplete turn on resume, re-running the pending work
	// (e.g. an Explore subagent the user stopped); starting fresh avoids that. The
	// interrupted conversation stays in the store, just not auto-resumed.
	resume := ""
	if db, ok := openRO(); ok {
		resume = activeSession(db, m)
		db.Close()
	} else {
		resume = sids.Read(ocSid) // store unreadable — the captured mapping is all we have
	}
	if resume != "" && !sessionResumable(resume) {
		resume = ""
	}
	// First launch of a forked slot: no own conversation yet — copy the source and
	// diverge (`--session <src> --fork`). opencode materializes the fork as a NEW
	// session at boot; the plugin records its id for this slot, so later launches
	// resume the fork normally (ForkFrom is then ignored, like claude's).
	fork := false
	if resume == "" && m.ForkFrom != "" {
		// `--session <src> --fork` has no "fork up to here" argument — only the serve API
		// does (docs/55 §55.5). Refuse rather than launch a CLI fork that would quietly
		// copy the WHOLE conversation when the user asked for a point.
		if m.ForkAt != "" {
			return agents.LaunchPlan{}, errors.New(
				"発言時点からの分岐は managed のセッションでのみ利用できます")
		}
		resume, fork = m.ForkFrom, true
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Mode, resume, fork), Cwd: m.CWD(), Env: envs}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State is derived from opencode's own store (LiveState) — robust against the
	// status plugin not firing — falling back to the plugin status file when the db can't
	// be read. Resumable unless the working dir is gone.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		if st := LiveState(m); st != "" {
			li.State = st
		} else {
			li.State = status.LiveState(session.UUID(m.Dir, m.Name))
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

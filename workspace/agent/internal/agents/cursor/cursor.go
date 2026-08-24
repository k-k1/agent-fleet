// Package cursor は Cursor CLI（`cursor-agent` / Anysphere）種別の縦割りパッケージ
// （docs/40 Track A）。read 層（Agent 実装・Claude Code 互換 JSONL の transcript/
// 状態読み）を種別内に閉じる。managed driver（`cursor-agent agent acp`、per-session
// child・ACP JSON-RPC over stdio）は Track A2 で driver.go/serve.go を足す。
//
// セッション同一性は AF 側で採番した v4 UUID を `--resume <uuid>` で渡す方式
// （実測: 未知の valid v4 は新規作成、既存は resume）——copilot の --session-id と
// 同型で、agy の「resume UUID が取れない」問題（docs/32 46271bb）は構造的に発生
// しない。read 正本は Claude Code 互換 JSONL 転写（program.go transcriptPath）——
// 非公開 SQLite（~/.cursor/chats/**/store.db）には依存しない（opencode ストア契約
// 変更で false-idle を踏んだ教訓 — docs/40 決定 3）。認証は専用フロー型で、
// 資格情報は ~/.config/cursor/auth.json（auth.go）。
package cursor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to the cursor chat UUID. Written at FRESH
// launch time with an AF-generated v4 UUID —— cursor accepts `--resume <uuid>` to
// CREATE a chat under that id (実測) and resumes it later, so there is no capture
// race to solve.
var sids = agents.NewSidStore("cursor-sid")

// New returns the cursor Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCursor }

// No fork (cursor's `/fork` is TUI-only) and no display label. The chat mirror IS
// supported: transcript.go reads the Claude Code-compatible JSONL, which cursor
// appends live in the TUI/-p routes.
func (agentImpl) Caps() agents.Caps { return agents.Caps{CanTranscript: true} }

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// 押し付けた id を cursor が使わなくなっていたら、起動前に拾い直す（sid.go）。
	// ここで直さないと `--resume <使われていない id>` を渡し続け、ユーザーの会話は
	// どこからも参照されないまま取り残される。
	chatID := resolveSid(m)
	if chatID == "" {
		var err error
		if chatID, err = newChatID(); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("チャット ID を採番できません: %w", err)
		}
		sids.Write(session.UUID(m.Dir, m.Name), chatID)
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Mode, chatID), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// cursor の TUI 状態は JSONL 転写末尾から分類する（state.go）——TUI 文字列
	// 非依存で false-idle 教訓に合致。managed（ACP）ルートは転写を書かないので
	// driver の runTurn 境界が状態源（Track A2）。
	li := agents.LiveInfo{Resumable: true}
	if alive {
		// 生存ポーリングがドリフトの検知点（cursor に hook は無い）。resolveSid は
		// 台帳を直すので、以降の ChatID 読みが新しい会話を指す（sid.go）。
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

// PendingModal は畳まれる直前の人待ちを持ち越しへ渡す（docs/75 P5）。
//
// cursor の人待ちは ACP の `session/request_permission`（plan 起動 / bypass を外した
// とき）だけで、TUI ルートの許可メニューは JSONL に痕跡を残さない（state.go の
// 冒頭コメント）ので観測できない — 取れないものは取れないと答える。
//
// Kind は **permission**。Interaction 自体は "question" を名乗るが、それは Console に
// 選択カードを描かせるための形であって、答えの宛先は ACP の JSON-RPC id である。
// 子プロセスごと消えた後にその可否を選ばせても届かない（docs/75 §75.6.4）ので、
// 持ち越すのは「何を訊かれていたか」という事実だけにする。
//
// ★ handle はメモリ上にしか無い。**畳む前に**呼ばれなければ何も残らない（昇格の契機は
// halt と gracefulShutdown。コンテナごと SIGKILL された場合だけは拾えない）。
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	if m.DriverKind() != session.DriverManaged {
		return agents.PendingModal{}, false
	}
	h := handleFor(m.Name)
	if h == nil {
		return agents.PendingModal{}, false
	}
	detail := h.pendingPermission()
	if detail == "" {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "permission", Detail: detail}, true
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// ChatID returns the slot's cursor chat UUID ("" when none allocated yet).
func ChatID(m session.Meta) string { return sids.Read(session.UUID(m.Dir, m.Name)) }

// newChatID generates an RFC4122 v4 UUID. cursor's --resume accepts a self-minted
// valid v4 to create a fresh chat（実測）; the version/variant bits keep it a
// well-formed UUID so the CLI doesn't reject it.
func newChatID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

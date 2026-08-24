// Package kiro は Kiro CLI（`kiro-cli`・旧 Amazon Q Developer CLI）種別の縦割り
// パッケージ（docs/43 Track A）。read 層（Agent 実装・v2 JSONL セッションストアの
// transcript／TUI 文字列契約の状態読み）を種別内に閉じる。managed driver
// （`kiro-cli acp`、per-session child・ACP JSON-RPC over stdio）は Track A2 で
// driver.go/acp.go/mirror.go に実装済み（cursor/copilot 同型・session/load の
// クロスプロセス resume＋文脈保持を実測）。
//
// セッション同一性（cursor との最大の差異）: kiro は**セッション ID を CLI 側で
// 採番**する。`--resume-id <uuid>` に自己採番の未知 UUID を渡しても採用されず、CLI
// は独自の新 ID を切る（実測 2.14.1）。よって cursor の「AF が UUID を先に切る」方式は
// 使えない。代わりに、起動後に生成される `~/.kiro/sessions/cli/<sid>.json`（cwd 記録
// 付き）を cwd＋更新時刻で発見し、一度掴んだら sidstore にキャッシュする（codex の
// rollout 発見と同型）。resume はキャッシュ済み sid を `--resume-id` に渡す。
//
// read 正本は v2 JSONL（`~/.kiro/sessions/cli/<sid>.jsonl`・append-only）——新 TUI と
// ACP が共用し、toolUse の入力と toolResult の出力まで載る（transcript.go）。非保証の
// classic SQLite（`~/.local/share/kiro-cli/data.sqlite3`・headless 専用）には依存しない
// （opencode ストア契約変更で false-idle を踏んだ教訓）。認証は Builder ID/device flow
// 型で、資格情報は `~/.local/share/kiro-cli/`（auth.go / fs.go denylist）。
package kiro

import (
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to kiro's own (CLI-minted) session id.
// Written LAZILY the first time a read/wire path discovers the minted id on disk
// (kiro mints it at launch — we can't pre-allocate as cursor does), and read at
// resume time to pass `--resume-id`.
var sids = agents.NewSidStore("kiro-sid")

// New returns the kiro Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindKiro }

// No fork (kiro has no non-interactive fork) and no display label. The chat mirror
// IS supported: transcript.go reads the v2 JSONL that the TUI appends live.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, PermissionChoice: true}
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	ensureSettings() // idempotent: pin autoupdate off + skip the --trust-all danger dialog
	// Resume only a session we've already learned for THIS slot. A fresh slot must NOT
	// grab an unrelated newest session in the same cwd, so we read the cache directly
	// (discovery is post-launch only — see resolveSid).
	resumeID := sids.Read(session.UUID(m.Dir, m.Name))
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, m.Mode, resumeID, agents.BypassPermissions(m)), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// kiro の TUI 状態は明示テキスト契約（state.go）で読む——「Kiro is working」/
	// 「ask a question or describe a task」/「requires approval」。2.14.1 には Stop
	// hook が無い（トリガは AgentSpawn/PrePrompt/PreToolUse/PostToolUse のみ・実測）
	// ので、この poll が TUI ルートの状態源。managed（ACP）は driver の runTurn 境界が
	// 状態源（Track A2）。
	li := agents.LiveInfo{Resumable: true}
	if alive {
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
// kiro の人待ちは承認だけで、在処は経路で違う: TUI はペインの承認パネル（文字列契約・
// state.go）、managed は ACP の `session/request_permission`（driver の handle）。
// どちらも**プロセスと一緒に消える**ので、halt / gracefulShutdown より遅い契機では
// 何も取れない（コンテナごと SIGKILL された場合は素直に失われる — docs/75 §75.7）。
//
// Kind は **permission**: 可否の宛先（TUI のパネル / ACP の JSON-RPC id）が消えている
// 以上、選ばせても届かない。持ち越すのは事実だけ（docs/75 §75.6.4）。
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	detail := ""
	if m.DriverKind() == session.DriverManaged {
		h := handleFor(m.Name)
		if h == nil {
			return agents.PendingModal{}, false
		}
		detail = h.pendingPermission()
	} else {
		detail = approvalDetail(m)
	}
	if detail == "" {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "permission", Detail: detail}, true
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// resolveSid returns the slot's kiro session id, discovering it on first use. kiro
// mints its own id at launch, so the resume-cache is populated here (a read/wire
// path) rather than at BuildLaunch. Once cached it sticks — a stable render key and
// the `--resume-id` source for the next launch.
func resolveSid(m session.Meta) string {
	slot := session.UUID(m.Dir, m.Name)
	if cached := sids.Read(slot); cached != "" {
		if _, err := os.Stat(sessionJSONPath(cached)); err == nil {
			return cached // still on disk
		}
		// The cached session's files were deleted (kiro --delete-session / manual): fall
		// through and rediscover so the mirror doesn't stick on a vanished conversation.
	}
	// Fence discovery to this slot's creation time so a predecessor session lingering
	// in the same dir (recreate cuts a new slug into the same dir) is never adopted
	// during the fresh-launch window — A-1. An unparseable CreatedAt degrades to no
	// fence rather than never resolving.
	if sid := discoverSid(m.Dir, slotCreatedAt(m)); sid != "" {
		sids.Write(slot, sid)
		return sid
	}
	return ""
}

// slotCreatedAt parses the slot's creation time (Meta.CreatedAt, set at create and
// stable across resumes). Zero time when absent/unparseable = no discovery fence.
func slotCreatedAt(m session.Meta) time.Time {
	if m.CreatedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

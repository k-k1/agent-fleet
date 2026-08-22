package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 「押し付け型」スロットの取りこぼし回収。
//
// CLI 側に自前の会話 id を採番させて捕捉する種別（codex は hook、opencode は plugin、
// agy/kiro はディスク探索）と違い、claude・copilot・cursor は **我々が採番した id を
// CLI に渡して、以後それが正しいと信じ続ける**（`--session-id` / `--resume <uuid>`）。
// この形は CLI がその id を使わなくなった瞬間に静かに壊れる:
//
//	claude 2.1.239 実測 — フルスクリーン TUI への切替などで claude は自分自身を起動し
//	直すが、その再起動 argv は設定系フラグだけから組み直され、--session-id は構造上
//	そこに入らない。id を失った claude はランダムな新 id でまっさらな会話を始める。
//	我々は決定論 sid の転写を探し続け、ミラーは「まだ会話はありません」のまま固まった
//	（386 スロット中 6 件）。claude は hook が session_id を名乗るので、そちらは
//	AF_SESSION_NAME を手掛かりに引き戻せた（internal/agents/claude/sid.go）。
//
// copilot と cursor には status hook が無い（それぞれ events.jsonl / 転写末尾から状態を
// 読む）ので、名乗りを聞く口が無い。残る手掛かりはディスクだけ — それがここ。
//
// **押し付けた id が CLI 側にまったく存在しないときだけ**動く、というのがこの回収の
// 肝。健全なスロットの会話を横取りする経路を作らないための線引きで、実際に観測された
// 壊れ方（押し付けた id で CLI が一度も書かなかった）にちょうど対応する。

// CLISession is one conversation the CLI itself keeps on disk, as seen by a kind's
// enumerator. Created may be zero when the CLI records no creation time (cursor —
// 転写ディレクトリの mtime を代理に使う; 実測でファイル追記では動かず作成時刻に留まる)。
type CLISession struct {
	ID      string
	Created time.Time
}

// ResolveImposedSID returns the conversation id the slot should use, adopting a
// replacement when the id we imposed was never taken up by the CLI.
//
// sessions enumerates the CLI's own conversations for dir. Returns "" for a slot that
// has never launched (no id allocated yet) — a fresh slot must never adopt a stranger's
// conversation, so discovery is deliberately not attempted there.
//
// 採用は「この dir の・スロット作成時刻以降の・他スロットに取られていない」候補が
// **ちょうど 1 つ**のときだけ。曖昧なら動かさない: 誤採用は他人の会話をミラーに映す
// ことで、固まったままより悪い。同一 dir に生きたスロットが 2 つある場合が既知の縁で、
// worktree が別 dir を与えるのがフリートの並行分離機構（kiro の discoverSid と同じ判断）。
func ResolveImposedSID(store SidStore, m session.Meta, sessions func(dir string) []CLISession) string {
	slot := session.UUID(m.Dir, m.Name)
	cached := store.Read(slot)
	if cached == "" {
		return "" // まだ一度も起動していないスロット。探索しない。
	}
	all := sessions(m.Dir)
	for _, s := range all {
		if s.ID == cached {
			return cached // CLI は我々が渡した id で書いている＝正常。ここが大多数。
		}
	}
	// ここから先はドリフト時のみ。ListMetas を舐めるコストはこの稀な経路にしか乗らない。
	notBefore := metaCreated(m)
	claimed := claimedByOtherSlots(store, slot)
	var found string
	for _, s := range all {
		if claimed[s.ID] {
			continue
		}
		if !notBefore.IsZero() && !s.Created.IsZero() && s.Created.Before(notBefore) {
			continue // このスロットより前からある会話 — 前任スロットのものかもしれない
		}
		if found != "" {
			return cached // 候補が複数。当て推量はしない。
		}
		found = s.ID
	}
	if found == "" {
		return cached
	}
	store.Write(slot, found)
	return found
}

// claimedByOtherSlots collects the conversation ids other slots have already been
// given, so a replacement is never stolen from a healthy session. copilot/cursor
// record theirs at BuildLaunch (before the CLI starts), so every AF slot's id is
// claimed from launch — an unclaimed conversation is one no slot imposed.
func claimedByOtherSlots(store SidStore, slot string) map[string]bool {
	out := map[string]bool{}
	for _, other := range session.ListMetas() {
		s := session.UUID(other.Dir, other.Name)
		if s == slot {
			continue
		}
		if id := store.Read(s); id != "" {
			out[id] = true
		}
	}
	return out
}

// metaCreated parses the slot's creation time. Zero (absent/unparsable) = no fence,
// degrading to the permissive behavior rather than never resolving — kiro の
// slotCreatedAt と同じ判断。
func metaCreated(m session.Meta) time.Time {
	if m.CreatedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

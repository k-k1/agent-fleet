package agents

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateSlots points the sid store and MetaDir at temp dirs（実フリートのセッションを
// 読まないため）。
func isolateSlots(t *testing.T) SidStore {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	return NewSidStore("test-sid")
}

const (
	imposedID = "11111111-1111-4111-8111-111111111111" // 我々が押し付けた id
	driftedID = "22222222-2222-4222-8222-222222222222" // CLI が実際に使い始めた id
	otherID   = "33333333-3333-4333-8333-333333333333"
)

func slotMeta(t *testing.T, name, dir, created string) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: dir, Kind: session.KindCopilot, CreatedAt: created}
	session.WriteMeta(m)
	return m
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 正常系: 押し付けた id で CLI が書いている限り、何も動かさない。ここが大多数の経路で、
// 健全なスロットの会話を横取りする余地を作らないことがこの回収の前提。
func TestResolveImposedKeepsHonoredID(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: imposedID, Created: ts(t, "2026-08-22T10:00:05+09:00")},
			{ID: driftedID, Created: ts(t, "2026-08-22T10:01:00+09:00")}, // 別スロットの新しい会話
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the honored id %q", got, imposedID)
	}
	if got := store.Read(slot); got != imposedID {
		t.Fatalf("ledger = %q, 健全なスロットの台帳が書き換わっている", got)
	}
}

// ドリフト: 押し付けた id が CLI 側に存在しない＝一度も使われなかった。この dir の
// 未取得の会話がちょうど 1 つなら拾い直す（claude で実際に起きた壊れ方に対応）。
func TestResolveImposedAdoptsDrift(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != driftedID {
		t.Fatalf("= %q, want the drifted id %q", got, driftedID)
	}
	if got := store.Read(slot); got != driftedID {
		t.Fatalf("ledger = %q, want it repointed to %q", got, driftedID)
	}
}

// 未起動スロット（台帳が空）では探索しない。新しいスロットが同じ dir に居る他人の会話を
// 掴むと、始まってすらいないミラーに知らない会話が出る。
func TestResolveImposedNeverAdoptsForFreshSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != "" {
		t.Fatalf("= %q, want \"\" — 未起動スロットは探索してはいけない", got)
	}
}

// 候補が複数なら動かさない。誤採用（他人の会話をミラーに映す）は固まったままより悪い。
func TestResolveImposedRefusesAmbiguity(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")},
			{ID: otherID, Created: ts(t, "2026-08-22T10:00:40+09:00")},
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the cached id kept when ambiguous", got)
	}
}

// 他スロットが既に持っている会話は候補から外す。copilot/cursor は BuildLaunch で
// 台帳に書くので、AF が起動した会話は全て「取得済み」になる。
func TestResolveImposedSkipsClaimedByOtherSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	other := slotMeta(t, "s2", "/tmp/repo", "2026-08-22T09:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)
	store.Write(session.UUID(other.Dir, other.Name), driftedID) // s2 のもの

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")}}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, 他スロットの会話を奪っている", got)
	}
}

// スロット作成より前からある会話は拾わない。recreate は同じ dir に新しい slug を切る
// ので、前任スロットの会話が必ずそこに残っている（kiro discoverSid と同じ縁）。
func TestResolveImposedFencesBySlotCreation(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	store.Write(session.UUID(m.Dir, m.Name), imposedID)

	list := func(string) []CLISession {
		return []CLISession{{ID: driftedID, Created: ts(t, "2026-08-22T09:59:00+09:00")}} // 前任
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, 前任スロットの会話を採用している", got)
	}
}

// 押し付けた id が CLI 側に**在る**なら、それがスロット作成時刻より古くても手放さない。
// 具体例: fork で材料化したセッションは元会話の created_at を引き継ぐ（copilot の
// MaterializeForkAt は sid を差し替えるだけ）ので、スロットより古い健全な会話になる。
// 「在るなら触らない」を先に判定しないと、同じ dir の新しい会話へ乗り換えてしまい、
// 分岐した会話が丸ごと見えなくなる。
func TestResolveImposedKeepsHonoredIDOlderThanSlot(t *testing.T) {
	store := isolateSlots(t)
	m := slotMeta(t, "s1", "/tmp/repo", "2026-08-22T10:00:00+09:00")
	slot := session.UUID(m.Dir, m.Name)
	store.Write(slot, imposedID)

	list := func(string) []CLISession {
		return []CLISession{
			{ID: imposedID, Created: ts(t, "2026-08-01T09:00:00+09:00")}, // fork 元の作成時刻
			{ID: driftedID, Created: ts(t, "2026-08-22T10:00:30+09:00")},
		}
	}
	if got := ResolveImposedSID(store, m, list); got != imposedID {
		t.Fatalf("= %q, want the honored id %q kept", got, imposedID)
	}
	if got := store.Read(slot); got != imposedID {
		t.Fatalf("ledger = %q, 健全な（分岐した）会話を手放している", got)
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// 共有 app-server の「需要ゼロで畳む」判定は、この数え方が正しいことに全面的に
// 依存している（docs/log/27 §7.1）。0 を返し続けるバグは静かで、症状は
// **動いている codex TUI セッションの会話が突然止まる**という最悪の形で出る。
func TestCountCodexTUISessions(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())

	write := func(name, kind, driver string, archived bool) {
		session.WriteMeta(session.Meta{Name: name, Kind: kind, Driver: driver, Archived: archived})
	}
	write("cx-live", session.KindCodex, "", false) // TUI・生存 → 数える
	write("cx-live2", session.KindCodex, session.DriverTUI, false)
	write("cx-dead", session.KindCodex, "", false) // TUI だが tmux に居ない
	write("cx-managed", session.KindCodex, session.DriverManaged, false)
	write("cx-archived", session.KindCodex, "", true)
	write("cl-live", session.KindClaude, "", false) // 別 kind は app-server を使わない

	live := map[string]bool{"cx-live": true, "cx-live2": true, "cx-managed": true, "cl-live": true}
	if got := countCodexTUISessions(live); got != 2 {
		t.Fatalf("countCodexTUISessions = %d, want 2 (TUI かつ生存の codex だけ)", got)
	}
	if got := countCodexTUISessions(nil); got != 0 {
		t.Fatalf("countCodexTUISessions(nil) = %d, want 0", got)
	}
}

// countCodexTUISessions は live のキーを「接頭辞を剥いだセッション名」だと決め打って
// 索く（live[m.Name]）。tmuxx.LiveSessionNames がいつか接頭辞つきで返すようになったら
// 数えは**静かに常時 0** になり、需要ゼロ判定が生きている TUI の backend を引き抜く。
//
// 読み取り専用で実 tmux に当てて契約を固定する（セッションは作らない — 作るなら
// fleet の claude_* 名前空間を汚すことになり、Console に幽霊セッションが出る）。
func TestLiveSessionNamesCarryNoPrefix(t *testing.T) {
	live := tmuxx.LiveSessionNames()
	if len(live) == 0 {
		t.Skip("tmux に fleet のセッションが無い — 契約を観測できない")
	}
	for name := range live {
		if strings.HasPrefix(name, session.TmuxPrefix) {
			t.Fatalf("LiveSessionNames が接頭辞つきの %q を返した — countCodexTUISessions の "+
				"live[m.Name] が常に外れ、TUI セッションが需要として数えられなくなる", name)
		}
		if session.TmuxName(name) != session.TmuxPrefix+name {
			t.Fatalf("TmuxName(%q) と接頭辞の組み立てが食い違う", name)
		}
	}
}

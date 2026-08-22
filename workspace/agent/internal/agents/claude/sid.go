package claude

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// slot sid → claude が実際に会話を書いている session id の台帳。
//
// 我々は claude を必ず `--session-id <決定論 sid>` で起動するので、普段この台帳は空の
// ままで、転写の所在も status のキーも決定論 sid のままでいい。ずれるのは claude が
// 自分でセッションを作り直したときだけ:
//
//	claude はいくつかの操作で自分自身を起動し直す（フルスクリーン TUI への切替
//	/tui・サインイン後の再起動・モデル切替）。その再起動 argv は「設定系フラグだけ」
//	から組み直され、--session-id と --name は構造上そこに入らない。2.1.239 実測: 生きて
//	いるプロセスの argv が
//	  claude.exe --allow-dangerously-skip-permissions --model opus --permission-mode bypassPermissions
//	になっていた（起動コマンドには --session-id も --name もあった）。--session-id を失った
//	claude は新しいランダム id で**まっさらな会話**を始めるので、決定論 sid の jsonl は
//	二度と現れない。決定論 sid しか見ていないと、ミラーは「まだ会話はありません」のまま、
//	hook 由来の status も別 id で書かれ、Console からはセッションが丸ごと消える。
//
// 決定論 sid は AF 側の slot キー（status・pending ペイロード・貼り付け画像…）として
// 使い続け、「claude が今どの id で書いているか」だけをここで追う。codex/opencode が
// 最初から持っている仕組みと同じもの（agents.SidStore）を claude にも用意する。
var sids = agents.NewSidStore("claude-sid")

// LiveSID returns the session id claude is actually writing under for our slot —
// slot itself in the normal case.
//
// 台帳を先に見る（決定論 sid のログより優先する）のは、ドリフト後に「決定論 sid の
// jsonl が別経路で用意された」場合でも、生きている会話の方を選ぶため。台帳の値が
// 指すログが無ければ黙って slot に戻るので、古い記載が残っていても害は無い。
func LiveSID(slot string) string {
	if live := sids.Read(slot); live != "" && live != slot && len(rawJSONLPaths(live)) > 0 {
		return live
	}
	return slot
}

// NormalizeHookSID maps the session_id a claude hook announced back onto our slot sid,
// recording the mapping when claude has drifted onto an id of its own.
//
// 手掛かりは AF_SESSION_NAME。tmux セッションの env として渡してある（session_tmux.go）
// ので、claude が自分を起動し直しても、その子として動く hook まで残る（実測: ドリフト
// 後のプロセスにも AF_SESSION_NAME があった）。cwd 一致のような当て推量と違い、これは
// 取り違えようがない。AF 管理外の claude（ユーザーが自分で起動したもの）には
// AF_SESSION_NAME が無いので、その hook は素通りする。
func NormalizeHookSID(live string) string {
	slot := hookSlotSID()
	if live == "" || slot == "" {
		return live
	}
	if slot == live {
		// ドリフトしていない。以前の記載が残っていれば消す — 再起動で --session-id が
		// 効いた状態に戻ったのに古い id を指し続けると、そちらを resume してしまう。
		if sids.Read(slot) != "" {
			sids.Remove(slot)
		}
		return live
	}
	sids.Write(slot, live)
	return slot
}

// hookSlotSID resolves the slot sid of the session this hook process belongs to.
func hookSlotSID() string {
	name := os.Getenv("AF_SESSION_NAME")
	if name == "" {
		return ""
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Kind != session.KindClaude {
		return ""
	}
	return session.UUID(m.Dir, m.Name)
}

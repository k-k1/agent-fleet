package agents

// 「いま画面に出ている人待ちのモーダル」を kind 側から構造で取り出す seam
// （docs/log/75 P5）。
//
// なぜ必要か: 持ち越し（carried interaction）の昇格は claude では status ストアの
// pending-question / pending-plan / pending-perm を読めば済む — hooks が ask 時点で
// ディスクへ書いているからである。他の kind にその hook は無く、保留は
//
//   - 会話 DB の最終 step（agy）
//   - events.jsonl の未完了 permission.requested（copilot）
//   - ペインのフッタ（kiro の TUI 承認パネル）
//   - **driver のメモリ上の handle**（ACP 3 種の session/request_permission）
//   - ネイティブストアの未応答レコード（codex / opencode の質問ツール）
//
// と散らばっている。畳む側（halt / 停止）が kind ごとの事情を知らずに済むよう、
// 「畳む直前に 1 回訊く」1 メソッドへ寄せる。
//
// ★実装には「生きているうちにしか答えられない」ものがある（ACP の handle・ペイン）。
// 呼ぶ側は必ず**畳む前に**呼ぶこと（session_carried.go の promoteCarriedFor と、
// その呼び出し元である halt / gracefulShutdown の順序）。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// PendingModal is one kind's answer to「いま人待ちで止まっているか、その中身は何か」。
type PendingModal struct {
	// Kind は "question" | "permission"。plan は claude 固有（ExitPlanMode）なので
	// ここには現れない。
	//
	// この 2 つを分けるのは**再開後にできること**が違うからである。質問の回答は文章
	// として配達すれば意味を持つが、許可の可否は**死んだツール呼び出しには届かない**
	// （ACP の JSON-RPC id も TUI のモーダルもプロセスと一緒に消える）。許可で持ち越
	// せるのは「何を訊かれていたか」という事実だけ（docs/log/75 §75.6.4）。
	//
	// ★ここを取り違えると実害が出る: 許可を question として運ぶと、Console は
	// 「Yes / No」を選ばせるカードを描き、選ばれた答えを**届かない宛先へ送ったつもり**に
	// なる。利用者から見れば許可したのに実行されない（あるいはその逆）。
	Kind string
	// Questions は Kind=="question" のときの回答フォーム。Console がこれを描いて
	// 選ばせ、選ばれたラベルが再開後の配達文になる。
	Questions []transcript.Question
	// Detail は Kind=="permission" のとき、何を訊かれていたか（"Bash · npm ci" 相当）。
	// 取れないこともある（copilot の events.jsonl は requestId しか持たないことがある）
	// ——そのときは空で、カードは事実だけを述べる。
	Detail string
	// Text は質問直前の地の文。無ければ空。
	Text string
}

// ModalReporter is implemented by the kinds whose pending modal is NOT in the status
// store. claude は実装しない — そちらは hooks が書く pending-* が正で、同じことを
// 2 か所から主張させない。
type ModalReporter interface {
	PendingModal(m session.Meta) (PendingModal, bool)
}

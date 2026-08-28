package tmuxx

import (
	"hash/fnv"
	"sync"
	"time"
)

// 「待機プロンプトに見えるフレーム」を「本当に待機している」と読み替えてよいか、を
// 時間で決める層。単発のフレームでは決められないことが実測で分かったので分けてある。
//
// # なぜ 1 フレームでは決められないのか（2026-08-28 実測・claude 2.1.25x）
//
// claude はアシスタントの本文ブロックを描いている間、スピナー行を出さない。その間の
// ペインはヘッダ・転写・入力欄・モード表示フッタが揃った、待機プロンプトと構造的に
// まったく同じ絵になる。自分のセッション（claude_scpsygq）を 0.25 秒間隔で採った 150
// 秒のフレーム列では、本文を描いている 21.4 秒がまるごと spinnerActive=false /
// atIdlePrompt=true で、その前後だけスピナーが出ていた。つまり本文が TUI に流れている
// 最中の 20 秒間、ペイン由来の判定は「入力待ち」と言い切る。
//
// 差分でも救えない。claude は markdown をブロック単位で描くので、長い段落を生成して
// いる間はペインが一度も再描画されない。同じ実測でこの 21.4 秒のフレーム 82 枚は 5 種類
// しかなく、同一バイト列が続いた最長は 11.4 秒だった。数秒間隔のポーリングでは同じ絵を
// 2 回続けて引くので、「前回と変わっていなければ待機」も同じ穴に落ちる。
//
// 救いは非対称性のほうにある: 本当に入力待ちのペインは、次に何かが起きるまで永久に
// 変わらない（他セッション claude_swovou6 を 1 秒間隔で 47 秒採取して 1 種類）。一方、
// 本文描画中のペインはブロックの切れ目ごとに必ず書き変わる。よって「待機に見える絵が、
// 書き変わらないまま idleSettleWindow 以上続いた」を条件にすれば、
//
//   - 本文描画中は各ブロックの再描画が時計を巻き戻すので settled にならない。回答が
//     どれだけ長くても、1 ブロックが窓を超えない限り誤判定しない。
//   - 本当の待機は再描画が起きないので、窓のぶんだけ遅れて必ず settled になる。
//
// # 窓の値
//
// 実測の最長静止（11.4 秒・長い段落 1 つ）に対して 4 倍の余裕。長くするほど本文描画の
// 取りこぼしは減るが、フックが鳴かない詰まり方（強制終了して再開した・API エラーで
// ターンが切れた・モーダルを放棄した）を 進行中 のまま見せる時間も同じだけ伸びる。
// 誤って「入力待ち」と名乗る害（停止ボタンが消える・完了通知や報告が早撃ちされる・
// アイドル判定に触れる）のほうが、数十秒バッジが遅れる害より大きいので、余裕を取る側に
// 倒してある。
const idleSettleWindow = 45 * time.Second

// idleSettleNow は時計。テストだけが差し替える。
var idleSettleNow = time.Now

// paneSighting は 1 セッションぶんの「最後にペインの絵が変わった時刻」。
type paneSighting struct {
	sig     uint64
	changed time.Time // この絵になった時刻（＝直前の絵と違って見えた時刻）
	seen    time.Time // 最後に観測した時刻（掃除用）
}

var (
	sightMu sync.Mutex
	sights  = map[string]paneSighting{}
)

// sightingTTL: 消えたセッションの残骸を落とす。ポーリングは数秒間隔なので、これだけ
// 見えていなければもう存在しない（あるいは一度 settled になり直しても構わない）。
const sightingTTL = 30 * time.Minute

// frameSig は 1 フレームの指紋。中身の比較にしか使わないので衝突耐性は要らない。
func frameSig(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// observeFrame records this capture and reports whether the pane has looked the same for
// at least idleSettleWindow. Callers combine it with the idle verdict itself (a pane can
// sit unchanged while it is busy — the thinking spinner does change, but a modal does
// not, and those are already excluded by atIdlePrompt).
//
// 初回観測は「いま変わった」として扱う（保守側に倒す）。エージェント再起動の直後は、
// 本当に待機しているセッションでも窓のぶんだけ 進行中 のままになるが、逆よりよい。
func observeFrame(name, frame string) bool {
	now := idleSettleNow()
	sig := frameSig(frame)
	sightMu.Lock()
	defer sightMu.Unlock()
	prev, ok := sights[name]
	if !ok || prev.sig != sig {
		prev = paneSighting{sig: sig, changed: now}
	}
	prev.seen = now
	sights[name] = prev
	for n, s := range sights {
		if now.Sub(s.seen) > sightingTTL {
			delete(sights, n)
		}
	}
	return now.Sub(prev.changed) >= idleSettleWindow
}

// ForgetPane drops a session's recorded sighting. Called when a pane is known to be gone
// so a later session reusing the name does not inherit its clock.
func ForgetPane(name string) {
	sightMu.Lock()
	delete(sights, name)
	sightMu.Unlock()
}

package main

// セッション同士のメッセージ（docs/58 / ADR 0041）の**サーバ側の砦**。
//
// 送信そのものは既存の投入経路（/input の {prompt}）をそのまま使う。ここが持つのは
// 「peer 送信であることによって課される制約」だけで、置き場をサーバにしたのは意図的:
// 封筒・宛先ポリシー・arm 非干渉・レート制限は、呼び出し元（MCP プロセス）が実装すると
// 迂回できてしまう。MCP は `peer_from` を1つ足すだけの薄い層に保ち、守るべき不変条件は
// 全部この層で閉じる。
//
// **arm を触らない理由**（ADR 0041 決定4）: docs/51 のリコンサイラは「機械的 idle」を
// 証拠に完了を推定する。peer メッセージは conv を持たず、idle 相手には新ターンを開始
// するので、指示台帳に載せると「利用者の新指示」と誤認して早期 settle / 早期消費を
// 起こす。しかも AF の投入は TUI への打鍵なので、受信側の transcript では通常入力と
// 区別が付かない（ネイティブ経路の `origin.kind:"peer"` に相当する印を後から付けられない
// — docs/58 §58.12 実測）。「後で出自を見て弾く」逃げ道が無いぶん、入口で載せないことが
// 唯一の防御になる。

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// peerMaxMessageBytes は本文の上限。通知1本という用途に対して十分で、TUI への
	// 打鍵として現実的な長さでもある。超過は無言で切り詰めず、エラーで返す（切り詰めは
	// 「送ったのに肝心の後半が消えている」という最悪の失敗になる）。
	peerMaxMessageBytes = 2000

	// peerRateWindow / peerRatePerWindow は送信元セッションあたりのレート制限。
	// 既存の send_to_session に無いのは送信者がオペレーター1人だったからで、送信者が
	// N になれば A→B→A は自然に起きる。
	peerRateWindow    = time.Minute
	peerRatePerWindow = 6

	// peerDuplicateWindow の間に届いた同一の (宛先, 本文) は捨てる。往復ループは
	// 同じ文面を投げ合う形になりやすく、レート制限だけだと上限まで無駄に走る。
	peerDuplicateWindow = 2 * time.Minute
)

// peerRejection は「送らせない」判断。Code はそのまま HTTP のエラーコードになる。
type peerRejection struct {
	Code string
	Msg  string
}

func (e *peerRejection) Error() string { return e.Msg }

func peerReject(code, format string, a ...any) *peerRejection {
	return &peerRejection{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// peerEnvelope は投入する本文の先頭に置く1行を組み立てる。
//
// プロンプト前置なのは、各 kind の TUI / driver への打鍵が唯一の共通投入層で、claude
// 以外に副帯域が無いため（ADR 0041 決定6）。selfReportHintLine の `[agent-fleet]` 注記と
// 同じ層・同じ作法で、受け取り方の常設ルールは workspace-notes.md 側が持つ。
//
// 封筒はサーバが必ず付ける。呼び出し元に組ませると、付け忘れ・詐称（他セッション名を
// 名乗る）がそのまま通ってしまう。
func peerEnvelope(from, message string) string {
	return "[agent-fleet:peer from=" + from + "] " + strings.TrimSpace(message)
}

// peerTargetAllowed は「この kind へ peer メッセージを送ってよいか」。
//
// shell / ssm を弾くのが本命（ADR 0041 決定5）。生シェルへの送信は任意コマンド実行で、
// 汚染されたリポジトリを読んだセッションが他所で任意のコマンドを走らせられる形になる。
// 判定を normalizeKind に通さず生の値で見るのは、normalizeKind が未知/空を claude へ
// 倒すため — 空 Kind のメタが1つあるだけで shell 以外の穴が開くのを避ける。
func peerTargetAllowed(kind string) bool {
	switch kind {
	case session.KindShell, session.KindSSM, "":
		return false
	}
	for _, k := range mcpreg.MaterializedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// peerReachableSessions は from から見える宛先候補（list_peer_sessions の母集合）。
// 自分自身・archived・送れない kind を除く。停止中は**含める** — AF は停止中セッションを
// 再開して届けられるので、一覧から落とすと届く相手が見えなくなる。
func peerReachableSessions(from string) []session.Meta {
	var out []session.Meta
	for _, m := range session.ListMetas() {
		if m.Archived || m.Name == from || !session.ValidName(m.Name) {
			continue
		}
		if !peerTargetAllowed(m.Kind) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// peerPolicy は送信の可否を判定する。宛先メタを返すのは、呼び出し側が kind を再取得
// しないで済むようにするため。
func peerPolicy(from, to string) (session.Meta, error) {
	if !session.ValidName(from) {
		return session.Meta{}, peerReject("bad_peer_from", "peer_from が不正なセッション名です")
	}
	if !session.ValidName(to) {
		return session.Meta{}, peerReject("bad_name", "宛先が不正なセッション名です")
	}
	if from == to {
		return session.Meta{}, peerReject("peer_self", "自分自身には送れません")
	}
	src, ok := session.ReadMeta(from)
	if !ok || src.Archived {
		return session.Meta{}, peerReject("peer_from_unknown", "送信元セッション %s が見つかりません", from)
	}
	// 送信元も同じ allowlist で見る。ツールを配っていない kind から `peer_from` を
	// 名乗られても通さない（MCP を持たない shell から REST を直叩きする経路の封じ）。
	if !peerTargetAllowed(src.Kind) {
		return session.Meta{}, peerReject("peer_from_forbidden",
			"この種別のセッション（%s）はメッセージを送れません", src.Kind)
	}
	dst, ok := session.ReadMeta(to)
	if !ok || dst.Archived {
		return session.Meta{}, peerReject("peer_target_unknown", "宛先セッション %s が見つかりません", to)
	}
	if !peerTargetAllowed(dst.Kind) {
		return session.Meta{}, peerReject("peer_target_forbidden",
			"この種別のセッション（%s）へは送れません", dst.Kind)
	}
	return dst, nil
}

// peerValidateMessage は本文の検査。
func peerValidateMessage(message string) error {
	m := strings.TrimSpace(message)
	if m == "" {
		return peerReject("empty_message", "message（送信本文）が必要です")
	}
	if !utf8.ValidString(m) {
		return peerReject("bad_message", "message は UTF-8 文字列にしてください")
	}
	if len(m) > peerMaxMessageBytes {
		return peerReject("message_too_long",
			"message は %d byte 以内にしてください（現在 %d byte）", peerMaxMessageBytes, len(m))
	}
	return nil
}

// peerLimiter はレート制限と重複 drop の状態。Agent プロセスは常駐なのでメモリで持つ
// （MCP プロセスは呼び出しごとに生き死にするので、そちらには置けない）。再起動で
// リセットされるが、これはループを止めるための弁であって監査ではないので許容する。
type peerLimiter struct {
	mu     sync.Mutex
	sends  map[string][]time.Time // 送信元 → 直近の送信時刻
	recent map[string]time.Time   // 送信元|宛先|本文 → 最後に通した時刻
}

var peerRate = &peerLimiter{
	sends:  map[string][]time.Time{},
	recent: map[string]time.Time{},
}

// allow はレート制限と重複を判定し、通す場合だけ状態を更新する。
func (l *peerLimiter) allow(from, to, message string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 重複: 同じ相手へ同じ文面を短時間に繰り返すのは往復ループの形。
	key := from + "|" + to + "|" + message
	if at, ok := l.recent[key]; ok && now.Sub(at) < peerDuplicateWindow {
		return peerReject("peer_duplicate",
			"同じ内容を同じ宛先へ連続して送ろうとしています（%s 以内は捨てます）", peerDuplicateWindow)
	}

	// レート: 送信元あたり peerRatePerWindow 通 / peerRateWindow。
	times := l.sends[from][:0:0]
	for _, t := range l.sends[from] {
		if now.Sub(t) < peerRateWindow {
			times = append(times, t)
		}
	}
	if len(times) >= peerRatePerWindow {
		l.sends[from] = times
		return peerReject("peer_rate_limited",
			"送信が多すぎます（%s あたり %d 通まで）", peerRateWindow, peerRatePerWindow)
	}

	l.sends[from] = append(times, now)
	l.recent[key] = now
	l.pruneLocked(now)
	return nil
}

// pruneLocked は古い重複キーを落とす。放置すると長寿命 Agent でメモリが単調増加する。
func (l *peerLimiter) pruneLocked(now time.Time) {
	for k, at := range l.recent {
		if now.Sub(at) >= peerDuplicateWindow {
			delete(l.recent, k)
		}
	}
	for from, times := range l.sends {
		if len(times) == 0 {
			delete(l.sends, from)
		}
	}
}

package main

// 台帳の (ref, idx) 重複排除（docs/log/46 §3-b / ADR0029 §5）。
//
// 折り込みの冪等性を担保しているのは watermark（`usage/state.json`）だが、**行を追記した
// 直後・watermark を書く前**に落ちると、そのセッションの数ターン分は次のパスでもう一度
// 追記される。commitSessionUsageFold で窓は1セッション分まで縮めた（29bd9f2d）が、窓自体は
// 消せない — 追記先（raw jsonl）と watermark（state.json）は別のファイルで、原子的に
// 書けないため。**書き手側で完全に閉じられない以上、読み手側で落とす。**
//
// 落とし方は台帳の形がそのまま与えてくれる。feature=session の行は (ref, idx) が論理ターンを
// 一意に指す（idx は転写の論理ターン通し番号・ADR0029 §5）ので、同じ (ref, idx) を二度
// 数えなければよい。
//
// 持つのは **ref ごとに1エントリ**（計上済みの最大 idx と、観測した最大 ts）。(ref, idx) の
// 集合そのものは持たない — 158 セッション × 数千ターンの集合を無期限に抱えると rollup の
// 「小さい」前提が壊れる。集合が要らないのは、行の追記が次の形しか取らないから:
//
//   - idx はセッションごとに 1 から単調増加で追記される（foldTurnRows が転写の先頭から
//     番号を振り、watermark 以降だけを書く）。
//   - 重複は必ず「追記済みの末尾を、後から、もう一度」という形で現れる。
//
// したがって「その ref で既に計上した最大 idx 以下」が重複の十分条件になる。watermark を
// 見失った（state.json の消失・破損）場合に idx 1 から全部やり直す縮退も、これで丸ごと
// 吸収される。
//
// ts 条件を併せて見るのは **slug の再利用** への保険。セッション名は 30bit の乱数 slug で、
// 生存中のメタとしか衝突検査をしない（session_name.go）ので、削除済みセッションの名前が
// いつか再び払い出されうる。その時 idx は 1 からやり直しになるため、idx だけで判定すると
// 新しいセッションの消費を「重複」として落としてしまう（＝静かな取りこぼし。重複より悪い）。
// 新しい incarnation の消費時刻は必ず古い方より後なので、**idx が既計上以下 かつ ts が
// 既観測以下** の時だけ落とす。判定に迷う側は「重複を残す」に倒す — 重複は raw を見れば
// 分かるが、落とした消費は二度と戻らない。
//
// キーは ref の SHA-256 前半。rollup 側は無期限に残る領域で、そこへ ADR0029 §8 が
// 「入れない」と決めた ref（セッション名 / 会話 id）を平文で溜め込みたくない。重複排除には
// 等値比較しか要らないのでハッシュで足りる（監査時は照合したい ref を同じ関数に通す）。

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// usageDedupMark は1つの ref について計上済みの水位。
type usageDedupMark struct {
	Idx int   `json:"idx"` // 計上済みの最大論理ターン番号
	TS  int64 `json:"ts"`  // 観測した最大消費時刻（Unix 秒）
}

// usageDedupIndex は ref ハッシュ → 水位。rollup state.json に載って無期限に残る
// （raw が prune された後の重複も落とせるようにするため）。1 ref ≈ 40B。
type usageDedupIndex map[string]usageDedupMark

// usageRefKey は ref を索引キーへ写す。平文の ref を無期限領域へ残さないためのハッシュで、
// 秘匿が目的ではない（16 hex = 64bit・衝突は実質起きない）。
func usageRefKey(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:8])
}

// accept は行を計上してよいか返す。false なら (ref, idx) の重複＝集計から落とす。
// ts は行の消費時刻（usageRowTime の結果）— 呼び出し側が既に解いているので受け取る。
//
// **呼び出しは行の追記順に、かつ期間で絞る前に**行う。どの行を「最初の1件」とみなすかが
// クエリ期間で変わると、期間を変えただけで合計が動く（同じ日の数字が期間指定で揺れる）。
func (d usageDedupIndex) accept(r usageRecord, ts time.Time) bool {
	// 補助呼び出しには idx が無い（1回の呼び出しをその場で1行書くだけなので、
	// 重複の生じる経路そのものが無い）。
	if r.Feature != usageFeatureSession || r.Ref == "" || r.Idx <= 0 {
		return true
	}
	k := usageRefKey(r.Ref)
	m, seen := d[k]
	sec := ts.Unix()
	if seen && r.Idx <= m.Idx && sec <= m.TS {
		return false
	}
	if !seen || r.Idx > m.Idx {
		m.Idx = r.Idx
	}
	if !seen || sec > m.TS {
		m.TS = sec
	}
	d[k] = m
	return true
}

// clone は索引を複製する。読み取り経路（collectUsageSamples）は畳み済み分の水位を
// 起点に使うが、その場の走査で進めた水位を rollup 側の state へ持ち帰ってはいけない。
func (d usageDedupIndex) clone() usageDedupIndex {
	out := make(usageDedupIndex, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

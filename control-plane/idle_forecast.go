package main

import (
	"sort"
	"time"
)

// idleForecast は「この Workspace はいつ止まるか、止まらないなら誰が止めているか」
// （docs/75 P4）。reaper が毎スイープで manager へ置き、管理画面が読む。
//
// なぜ要るか: 自動停止が効かないとき、これまで運用者に見えるものが何も無かった。
// reaper はログを出すだけで、「なぜ止まらないのか」を返す API も画面も無く、調べる
// 唯一の手段が他人のコンテナへ docker exec して status ファイルを読むことだった
// （docs/75 D7）。しかも P0〜P3 で判定材料が増えた（人待ち・背景作業・在席・ピン）ので、
// 見えないままだと運用者は「止まらない」を説明できない。
type idleForecast struct {
	// Enabled はこのテナントで tier2（Workspace 停止）が有効か。0 = 無効のときは
	// 「予定なし」ではなく「機能が切ってある」と言えないと、設定ミスと区別できない。
	Enabled bool `json:"enabled"`
	// StopAt は今の観測が続いた場合の停止予定時刻。Holders が空のときだけ意味を持つ。
	StopAt time.Time `json:"stopAt,omitempty"`
	// Holders は止めない理由。空 = 何も止めていない（StopAt に向かって数えている）。
	Holders []idleHolder `json:"holders,omitempty"`
	// ObservedAt は観測時刻。画面は「いつ時点の話か」を出せる必要がある —
	// スイープ間隔ぶん古いので、秒単位の断言をさせない。
	ObservedAt time.Time `json:"observedAt"`
}

// idleHolder は「止まらない理由」1 件。Kind は画面が文言を選ぶための語彙で、
// Session はセッション名（あれば）。
type idleHolder struct {
	// Kind: "working"（ターン実行中）/ "background"（背景ジョブ・サブエージェント）/
	// "pin"（自動停止しないピン）/ "watching"（人が触っている）/ "recent"（直近の操作）
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	// Until はピンの期限（Kind=="pin" のときだけ）。
	Until string `json:"until,omitempty"`
}

// holdersOf は 1 スイープぶんのセッション一覧と在席から「止めない理由」を作る。
// 純関数 — reaper の判定と同じ材料から、同じ順序で並べる。
func holdersOf(sessions []sessionWire, watched bool, now time.Time) []idleHolder {
	var out []idleHolder
	for _, s := range sessions {
		if !s.Alive {
			continue
		}
		switch {
		case keepAwake(s.KeepAwakeUntil, now):
			// ピンを working より先に見る: 利用者が明示的に宣言したものは、たまたま
			// 今ターンが走っていることより説明として強い（解除すれば止まる）。
			out = append(out, idleHolder{Kind: "pin", Session: s.Name, Until: s.KeepAwakeUntil})
		case s.BackgroundBusy:
			out = append(out, idleHolder{Kind: "background", Session: s.Name})
		case busyState(s.State):
			// 状態名を直接並べない: reaper の busy 判定（sessionActivity）と同じ
			// 述語を見る。working だけを見ていた頃、compacting は reaper では
			// machineBusy なのに画面では holders が空＝ StopAt が出ていた
			// （docs/75 決定 11 違反）。
			out = append(out, idleHolder{Kind: "working", Session: s.Name})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	if watched {
		// セッション由来の理由の後ろに置く: 「誰かが見ている」は外しようがないが、
		// 走っているセッションは具体的な対処（待つ・止める）につながる。
		out = append(out, idleHolder{Kind: "watching"})
	}
	return out
}

// putIdleForecast records the reaper's latest read of one workspace.
func (m *manager) putIdleForecast(wsID string, f idleForecast) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idleForecasts == nil {
		m.idleForecasts = map[string]idleForecast{}
	}
	m.idleForecasts[wsID] = f
}

// idleForecastFor returns the last recorded read, if any.
//
// 古い観測はそのまま返す（TTL で消さない）: 「観測が無い」と「1 分前の観測」は画面上で
// 意味が違い、後者は ObservedAt を出せば読み手が判断できる。reaper が止まっている
// デプロイでは最初から何も入らないので、無効との区別もつく。
func (m *manager) idleForecastFor(wsID string) (idleForecast, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.idleForecasts[wsID]
	return f, ok
}

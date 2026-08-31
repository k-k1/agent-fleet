package main

import (
	"testing"
	"time"
)

// 「なぜ止まらないか」の語彙。ここが reaper の判定とズレると、調べるための画面が
// 別の答えを出す（docs/log/75 P4）— それなら画面が無い方がまし、という性質の機能なので、
// 材料と優先順位を固定しておく。
func TestHoldersOf(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Minute).Format(time.RFC3339)

	t.Run("何も無ければ空", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, State: stateIdle}}, false, now, 0)
		if len(got) != 0 {
			t.Errorf("holders = %+v, want empty", got)
		}
	})

	t.Run("人待ちは止める理由にならない", func(t *testing.T) {
		// docs/log/75 の本題そのもの: 質問が出ていても Workspace は止まる。
		for _, st := range []string{stateQuestion, statePlan, statePermission, stateBlocked, stateAuth, stateSpendLimit, stateLimited} {
			if got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: st}}, false, now, 0); len(got) != 0 {
				t.Errorf("state %q が止める理由になっている: %+v", st, got)
			}
		}
	})

	t.Run("実行中・背景作業・在席をそれぞれ挙げる", func(t *testing.T) {
		got := holdersOf([]sessionWire{
			{Alive: true, Name: "s2", State: stateWorking},
			{Alive: true, Name: "s1", State: stateIdle, BackgroundBusy: true},
			{Alive: false, Name: "s9", State: stateWorking}, // 停止中は数えない
		}, true, now, 0)
		if len(got) != 3 {
			t.Fatalf("holders = %+v, want 3", got)
		}
		// セッションは名前順、在席は最後（具体的な対処につながる理由を先に出す）。
		if got[0].Session != "s1" || got[0].Kind != "background" {
			t.Errorf("1 件目 = %+v, want background/s1", got[0])
		}
		if got[1].Session != "s2" || got[1].Kind != "working" {
			t.Errorf("2 件目 = %+v, want working/s2", got[1])
		}
		if got[2].Kind != "watching" || got[2].Session != "" {
			t.Errorf("3 件目 = %+v, want watching", got[2])
		}
	})

	// ★ reaper が busy と見る状態は working だけではない。compacting（codex の文脈圧縮）
	// を holders が落とすと、reaper は止めないのに画面は StopAt を出す＝「もうすぐ
	// 止まります」と言いながら止まらない、という docs/log/75 決定 11 違反になる。
	t.Run("reaper が busy と見る状態は全部挙げる", func(t *testing.T) {
		for _, st := range []string{stateWorking, stateCompacting} {
			s := sessionWire{Alive: true, Name: "s1", State: st}
			got := holdersOf([]sessionWire{s}, false, now, 0)
			if len(got) != 1 || got[0].Kind != "working" {
				t.Errorf("state %q: holders = %+v, want working（holdsWorkspace=%v）", st, got, holdsWorkspace(s))
			}
			if !holdsWorkspace(s) {
				t.Errorf("state %q が reaper 側で busy でなくなっている（前提が変わった）", st)
			}
		}
	})

	t.Run("ピンは working より先に説明される", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: stateWorking, KeepAwakeUntil: future}}, false, now, 0)
		if len(got) != 1 || got[0].Kind != "pin" || got[0].Until != future {
			t.Errorf("holders = %+v, want pin（解除すれば止まる、が正しい説明）", got)
		}
	})

	t.Run("期限切れのピンは理由にならない", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: stateIdle, KeepAwakeUntil: past}}, false, now, 0)
		if len(got) != 0 {
			t.Errorf("holders = %+v, want empty", got)
		}
	})

	// ★ セッションが 1 つも無い Workspace でも、取り込み中なら止まらない（docs/log/78）。
	// reaper 側の busy と対で入れてある — 片方だけだと「止まらないのに理由が空」になる。
	t.Run("取り込み中はセッションが無くても理由になる", func(t *testing.T) {
		got := holdersOf(nil, false, now, 1)
		if len(got) != 1 || got[0].Kind != "repojob" {
			t.Errorf("holders = %+v, want repojob", got)
		}
	})
}

func TestIdleForecastStore(t *testing.T) {
	m := &manager{}
	if _, ok := m.idleForecastFor("ws1"); ok {
		t.Error("観測が無いのに返した")
	}
	f := idleForecast{Enabled: true, StopAt: time.Now().Add(time.Hour), ObservedAt: time.Now()}
	m.putIdleForecast("ws1", f)
	got, ok := m.idleForecastFor("ws1")
	if !ok || !got.StopAt.Equal(f.StopAt) || !got.Enabled {
		t.Errorf("idleForecastFor = %+v/%v, want %+v", got, ok, f)
	}
}

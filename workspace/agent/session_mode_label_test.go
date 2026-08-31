package main

// claude のモード表示（チップのラベル）と、その裏にある readiness ゲート。
//
// 文字列は 2026-08-24 に claude 2.1.241 を実 tmux で起動して採取したもの（各
// --permission-mode を 1 本ずつ立ててフッタを capture-pane した）。★manual だけ
// "(shift+tab to cycle)" を出さず、しかも manual は「権限確認あり」起動（docs/log/76）の
// 落ちる先そのものなので、ここが空文字を返すと初回プロンプトの配達が 30 秒待たされる。

import "testing"

func TestClaudeModeLabel(t *testing.T) {
	cases := []struct{ name, tail, want string }{
		// 実測フッタ（末尾の "· ← for agents" まで含めて採取したまま）。
		{"manual（--permission-mode 無しの既定）", "  ⏸ manual mode on · ← for agents", "Manual"},
		{"auto", "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", "Auto"},
		{"acceptEdits", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents", "Accept Edits"},
		{"bypassPermissions", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", "Bypass"},
		{"dontAsk", "  ⏵⏵ don't ask on (shift+tab to cycle) · ← for agents", "Don't ask"},
		{"plan", "  ⏸ plan mode on (shift+tab to cycle) · ← for agents", "Plan"},
		// 背景作業があるとヒントが別のセグメントに置き換わる（tmuxx の実測メモと同型）。
		{"背景作業でヒントが消える", "  ⏵⏵ bypass permissions on · 1 shell · ← for agents", "Bypass"},
		// 名前が増えた/改名された未来: フッタ帯が見えている限り「未描画」に倒さない。
		{"未知のモード名でもフッタ帯があれば描画済み", "  ⏵⏵ something new on (shift+tab to cycle)", "Default"},
		{"古いビルドの合言葉だけ", "  ? for shortcuts · shift+tab to cycle", "Default"},
		// ブート中・モーダルが被っている: 本当に未描画。
		{"ブート画面", "Loading…", ""},
		{"モード帯でない本文", "私は計画について説明します", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeModeLabel(tc.tail); got != tc.want {
				t.Fatalf("claudeModeLabel(%q) = %q, want %q", tc.tail, got, tc.want)
			}
		})
	}
}

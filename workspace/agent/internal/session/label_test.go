package session

import "testing"

// TestLabelRoundTrip — ラベルの組み立てと読み戻し。
//
// 一番効くのは**旧形式 `[AF] <title>` も剥がせる**こと: ラベルは作成時に meta へ焼かれる
// ので、この変更を入れても既存セッションは古い形のまま残り、新旧が同時に画面に並ぶ。
// 新形式だけ剥がす strip を書くと、古いセッションの行にだけタグが残る。
func TestLabelRoundTrip(t *testing.T) {
	for _, c := range []struct{ label, strip, name string }{
		{"[AF:s6bbilu] 94-freeze 試走2本", "94-freeze 試走2本", "s6bbilu"},
		{"[AF:s6bbilu] agent-fleet @0831-1922", "agent-fleet @0831-1922", "s6bbilu"},
		{"[AF] 旧形式のまま残ったラベル", "旧形式のまま残ったラベル", ""},
		{"[AF]タイトル", "タイトル", ""}, // 空白なしも一応剥がす
		{"どこか他所で付いた --name", "どこか他所で付いた --name", ""},
		{"", "", ""},
		// タイトルが "[AF:" で始まっていても、名前として通るのは ValidName の字種だけ。
		// ここを緩めると、利用者のタイトルの一部を送信者名として表示しかねない。
		{"[AF:日本語] t", "[AF:日本語] t", ""},
	} {
		if got := StripLabel(c.label); got != c.strip {
			t.Errorf("StripLabel(%q) = %q, want %q", c.label, got, c.strip)
		}
		if got := LabelSessionName(c.label); got != c.name {
			t.Errorf("LabelSessionName(%q) = %q, want %q", c.label, got, c.name)
		}
	}

	// 組み立て → 読み戻しが一致すること。名前が不正なら旧タグへ落ちる（起動は止めない）。
	if got := LabelPrefix("s6bbilu"); got != "[AF:s6bbilu] " {
		t.Errorf("LabelPrefix = %q", got)
	}
	if got := LabelPrefix("../etc"); got != "[AF] " {
		t.Errorf("LabelPrefix(不正な名前) = %q, want %q", got, "[AF] ")
	}
	if got := LabelSessionName(LabelPrefix("sabc123") + "タイトル"); got != "sabc123" {
		t.Errorf("round trip = %q", got)
	}
}

// TestDisplayStripsLabelTag — 表示名がタグを剥がしたラベルになること（タイトル未設定時）。
func TestDisplayStripsLabelTag(t *testing.T) {
	if got := Display(Meta{Label: "[AF:s6bbilu] agent-fleet @0831-1922"}); got != "agent-fleet @0831-1922" {
		t.Errorf("Display = %q", got)
	}
	if got := Display(Meta{Title: "題", Label: "[AF:s6bbilu] 別"}); got != "題" {
		t.Errorf("Display（タイトル優先） = %q", got)
	}
}

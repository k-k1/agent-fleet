package mdblock

import "testing"

func TestSetAppendsAndReplacesInPlaceOfItsOwnBlock(t *testing.T) {
	s := Set("", "rtk", "use rtk")
	if got, ok := Get(s, "rtk"); !ok || got != "use rtk" {
		t.Fatalf("first Set: got %q ok=%v\n%s", got, ok, s)
	}
	// Re-setting must not stack a second copy.
	s = Set(s, "rtk", "use rtk v2")
	if n := countMarkers(s, "rtk"); n != 1 {
		t.Fatalf("expected exactly 1 block, got %d:\n%s", n, s)
	}
	if got, _ := Get(s, "rtk"); got != "use rtk v2" {
		t.Fatalf("replace: got %q", got)
	}
}

// ★ 本命の回帰: マーカー外（利用者が書いた文章）を絶対に消さない。
func TestSetPreservesTextOutsideMarkers(t *testing.T) {
	base := "# my own notes\n\nalways speak Japanese\n"
	s := Set(base, "user-notes", "hello")
	if !contains(s, "always speak Japanese") {
		t.Fatalf("user text lost:\n%s", s)
	}
	s = Set(s, "user-notes", "")
	if !contains(s, "always speak Japanese") {
		t.Fatalf("user text lost on removal:\n%s", s)
	}
	if Has(s, "user-notes") {
		t.Fatalf("empty body must remove the block:\n%s", s)
	}
}

func TestSetOrderFollowsCallOrder(t *testing.T) {
	s := ""
	s = Set(s, "fleet", "FLEET")
	s = Set(s, "user-notes", "USER")
	s = Set(s, "rtk", "RTK")
	fi, ui, ri := index(s, "FLEET"), index(s, "USER"), index(s, "RTK")
	if !(fi < ui && ui < ri) {
		t.Fatalf("order fleet<user<rtk not held (%d,%d,%d):\n%s", fi, ui, ri, s)
	}
	// Rewriting the middle block moves it to the end but must not duplicate or drop.
	s = Set(s, "user-notes", "USER2")
	for _, want := range []string{"FLEET", "USER2", "RTK"} {
		if !contains(s, want) {
			t.Fatalf("%s missing after rewrite:\n%s", want, s)
		}
	}
	if contains(s, "USER\n") {
		t.Fatalf("stale body left behind:\n%s", s)
	}
}

func TestStripHandlesMissingEndMarker(t *testing.T) {
	start, _ := Markers("rtk")
	s := "keep me\n\n" + start + "\nrunaway block with no end\n"
	out := Strip(s, "rtk")
	if out != "keep me\n" {
		t.Fatalf("got %q", out)
	}
}

func TestStripUnknownBlockIsNoop(t *testing.T) {
	s := "# doc\n"
	if out := Strip(s, "nope"); out != s {
		t.Fatalf("got %q", out)
	}
}

// 移行の要: cp -f 時代の生のフリート方針を1度だけ剥がし、AF ブロックは残す。
func TestStripLegacyPrefixDropsOldCopyKeepsBlocks(t *testing.T) {
	fleet := "# Workspace Guide (operating policy)\n\nold body\n"
	start, end := Markers("rtk")
	s := fleet + "\n" + start + "\nRTK\n" + end + "\n"
	out := StripLegacyPrefix(s, "# Workspace Guide (operating policy)\n\nNEW body (image bumped)\n")
	if contains(out, "old body") {
		t.Fatalf("legacy copy survived:\n%s", out)
	}
	if !contains(out, "RTK") {
		t.Fatalf("af block was eaten:\n%s", out)
	}
}

func TestStripLegacyPrefixLeavesUserWrittenText(t *testing.T) {
	s := "# my own notes\n\nalways speak Japanese\n"
	if out := StripLegacyPrefix(s, "# Workspace Guide (operating policy)\n"); out != s {
		t.Fatalf("user text must never match the legacy signature:\n%s", out)
	}
	// フリート方針が読めない環境（legacy 空）では何も剥がさない。
	if out := StripLegacyPrefix(s, ""); out != s {
		t.Fatalf("empty legacy must be a no-op:\n%s", out)
	}
}

func countMarkers(s, name string) int {
	start, _ := Markers(name)
	n := 0
	for i := 0; ; {
		k := index(s[i:], start)
		if k < 0 {
			return n
		}
		n++
		i += k + len(start)
	}
}

func contains(s, sub string) bool { return index(s, sub) >= 0 }

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

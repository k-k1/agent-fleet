package kiro

import "testing"

func TestLiveContextOf(t *testing.T) {
	lc, ok := liveContextOf(33.3, 1000, 2.5, "m", true)
	if !ok || lc.Tokens != 333 || lc.Window != 1000 || lc.Pct != 33.3 || lc.WindowSource != "estimated" ||
		lc.Model != "m" || lc.Credits == nil || *lc.Credits != 2.5 {
		t.Errorf("got %+v, %v", lc, ok)
	}
	if lc, _ := liveContextOf(50.5, 3, 0, "", true); lc.Tokens != 2 { // 1.515 rounds to 2
		t.Errorf("rounding: tokens = %d, want 2", lc.Tokens)
	}
	if _, ok := liveContextOf(10, 0, 0, "", true); ok {
		t.Error("a non-positive window must yield nothing")
	}
	if _, ok := liveContextOf(10, 100, 0, "", false); ok {
		t.Error("no live handle must yield nothing")
	}
	if lc, ok := liveContextOf(10, 100, 0, "", true); !ok || lc.Credits == nil || *lc.Credits != 0 {
		t.Errorf("a metered 0 must stay a reported 0: %+v", lc)
	}
}

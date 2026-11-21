package muse

import "testing"

func TestLiveContextOf(t *testing.T) {
	win := int64(2000)
	lc, ok := liveContextOf(500, &win, true)
	if !ok || lc.Tokens != 500 || lc.Window != 2000 || lc.WindowSource != "recorded" || lc.Pct != 25 || lc.Credits != nil {
		t.Errorf("recorded window: %+v, %v", lc, ok)
	}
	lc, _ = liveContextOf(500, nil, true)
	if lc.Window != MuseDefaultWindow || lc.WindowSource != "estimated" {
		t.Errorf("no basis limit: %+v", lc)
	}
	zero := int64(0)
	if lc, _ = liveContextOf(500, &zero, true); lc.WindowSource != "estimated" {
		t.Errorf("a zero window is no limit: %+v", lc)
	}
	if _, ok := liveContextOf(500, nil, false); ok {
		t.Error("no report yet must yield nothing")
	}
}

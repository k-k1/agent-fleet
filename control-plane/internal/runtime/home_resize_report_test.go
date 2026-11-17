package runtime

import "testing"

// A shrink is reported only when the save changed the disk number; the volume being
// larger than an unchanged setting is not news on every save.
func TestHomeResizeReportOnSave(t *testing.T) {
	cases := []struct {
		outcome     string
		diskChanged bool
		want        bool
	}{
		{HomeResizeShrink, false, false},
		{HomeResizeShrink, true, true},
		{HomeResizeGrowing, true, true},
		{HomeResizeGrowing, false, true},
		{HomeResizeSame, false, true},
		{HomeResizeNoHome, false, true},
		{HomeResizeFailed, false, true},
		{HomeResizeSame, true, true},
		{HomeResizeNoHome, true, true},
		{HomeResizeFailed, true, true},
		{"", true, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := (HomeResize{Outcome: c.outcome}).ReportOnSave(c.diskChanged); got != c.want {
			t.Errorf("ReportOnSave(%q, changed=%v) = %v, want %v", c.outcome, c.diskChanged, got, c.want)
		}
	}
}

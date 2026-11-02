package regionfmt

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	// 2026-03-08 07:30 UTC is after New York's spring-forward (02:00 EST -> 03:00 EDT), so the
	// offset is -4h; 2026-03-07 23:30 UTC is -5h and lands on the previous local day.
	after := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)
	before := time.Date(2026, 3, 8, 3, 30, 0, 0, time.UTC)
	const fb = "Jan 2 15:04"
	cases := []struct {
		name, region, tz string
		at               time.Time
		want             string
	}{
		{"auto keeps the caller layout", "", "UTC", after, "Mar 8 07:30"},
		{"unknown region keeps the caller layout", "xx-YY", "UTC", after, "Mar 8 07:30"},
		{"ja", "ja-JP", "UTC", after, "2026/03/08 07:30"},
		{"en-US 12h", "en-US", "UTC", after, "03/08/2026, 7:30 AM"},
		{"en-GB", "en-GB", "UTC", after, "08/03/2026, 07:30"},
		{"de", "de-DE", "UTC", after, "08.03.2026, 07:30"},
		{"fr", "fr-FR", "UTC", after, "08/03/2026 07:30"},
		{"DST after the shift", "ja-JP", "America/New_York", after, "2026/03/08 03:30"},
		{"standard time before the shift", "ja-JP", "America/New_York", before, "2026/03/07 22:30"},
		{"day boundary moves with the zone", "", "Asia/Tokyo", time.Date(2026, 3, 7, 16, 0, 0, 0, time.UTC), "Mar 8 01:00"},
	}
	for _, c := range cases {
		if got := format(c.at, fb, c.region, c.tz); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestBadZoneFallsBack(t *testing.T) {
	at := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)
	want := at.In(time.Local).Format("2006-01-02 15:04")
	for _, tz := range []string{"", "auto", "Not/AZone", "Local"} {
		if got := format(at, "2006-01-02 15:04", "", tz); got != want {
			t.Errorf("tz %q: got %q want %q", tz, got, want)
		}
	}
}

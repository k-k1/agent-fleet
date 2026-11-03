package regionfmt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
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

// "auto" follows the browser zone the Console published; an explicit zone still wins, and a
// garbage published value falls back to time.Local without an error.
func TestDateTimeUsesPublishedBrowserZone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Date(2026, 3, 7, 16, 0, 0, 0, time.UTC) // 11:00 on Mar 7 in New York (EST)
	const fb = "Jan 2 15:04"
	write := func(body string) {
		p := uiprefs.Path()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	local := at.In(time.Local).Format(fb)
	cases := []struct{ body, want string }{
		{`{"timeZone":"auto","browserTimeZone":"America/New_York"}`, "Mar 7 11:00"},
		{`{"browserTimeZone":"America/New_York"}`, "Mar 7 11:00"},
		{`{"timeZone":"UTC","browserTimeZone":"America/New_York"}`, "Mar 7 16:00"},
		{`{"timeZone":"auto","browserTimeZone":"Not/AZone"}`, local},
		{`{"timeZone":"auto","browserTimeZone":""}`, local},
		{`{"timeZone":"auto"}`, local},
	}
	for _, c := range cases {
		write(c.body)
		if got := DateTime(at, fb); got != c.want {
			t.Errorf("%s: got %q want %q", c.body, got, c.want)
		}
	}
	write(`{"timeZone":"auto","browserTimeZone":"America/New_York"}`)
	if got := Location().String(); got != "America/New_York" {
		t.Errorf("Location() = %q, want America/New_York", got)
	}
}

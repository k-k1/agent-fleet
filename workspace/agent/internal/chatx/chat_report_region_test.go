package chatx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func writePrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := uiprefs.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// With both settings on "auto" the notes read exactly as before the settings reached the Agent
// (container clock, the original layouts); an explicit zone and region change them.
func TestReportNotesFollowRegionFormat(t *testing.T) {
	ms := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC).UnixMilli()
	at := time.UnixMilli(ms)

	writePrefs(t, `{"timeZone":"auto","regionFormat":"auto"}`)
	if got, want := rateLimitResumeFact(ms, "en"), "booked for "+at.Local().Format("Jan 2 15:04")+" ("; !strings.Contains(got, want) {
		t.Errorf("auto en: %q lacks %q", got, want)
	}
	if got, want := rateLimitResumeFact(ms, "ja"), at.Local().Format("1月2日 15:04")+"（"; !strings.Contains(got, want) {
		t.Errorf("auto ja: %q lacks %q", got, want)
	}
	if got, want := reopenTargetFact(ms, "en"), "of "+at.Local().Format("2006-01-02 15:04")+")"; !strings.Contains(got, want) {
		t.Errorf("auto reopen: %q lacks %q", got, want)
	}

	// New York is on daylight time here (UTC-4): 07:30Z is 03:30 local.
	writePrefs(t, `{"timeZone":"America/New_York","regionFormat":"en-GB"}`)
	if got := rateLimitResumeFact(ms, "en"); !strings.Contains(got, "08/03/2026, 03:30") {
		t.Errorf("explicit: %q", got)
	}
	if got := reopenTargetFact(ms, "ja"); !strings.Contains(got, "08/03/2026, 03:30") {
		t.Errorf("explicit reopen: %q", got)
	}

	// A zone name that is not an IANA zone falls back instead of failing.
	writePrefs(t, `{"timeZone":"Not/AZone","regionFormat":"en-GB"}`)
	if got, want := reopenTargetFact(ms, "en"), at.Local().Format("02/01/2006, 15:04"); !strings.Contains(got, want) {
		t.Errorf("bad zone: %q lacks %q", got, want)
	}
}

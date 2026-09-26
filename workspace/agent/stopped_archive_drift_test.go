package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The archive period is picked in the Console and applied in the Agent, and the two ends carry
// the choices separately: STOPPED_ARCHIVE_DAYS / STOPPED_ARCHIVE_NEVER are the buttons,
// session.StoppedArchiveDayChoices / StoppedArchiveNever are what the Agent accepts. A button the
// Agent does not accept is read as "not set", so it silently applies the deployment default
// instead of the period the user picked.
func TestDriftStoppedArchiveChoices(t *testing.T) {
	src := consoleSettingsSource(t)

	m := regexp.MustCompile(`STOPPED_ARCHIVE_DAYS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no STOPPED_ARCHIVE_DAYS array in console/src/lib/settings.ts (renamed? then update this test and session.StoppedArchiveDayChoices together)")
	}
	var got []int
	for _, f := range strings.Split(m[1], ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("STOPPED_ARCHIVE_DAYS holds a non-number %q", f)
		}
		got = append(got, n)
	}
	if !slices.Equal(got, session.StoppedArchiveDayChoices) {
		t.Fatalf("STOPPED_ARCHIVE_DAYS = %v, session.StoppedArchiveDayChoices = %v", got, session.StoppedArchiveDayChoices)
	}

	m = regexp.MustCompile(`STOPPED_ARCHIVE_NEVER\s*=\s*(-?\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no STOPPED_ARCHIVE_NEVER in console/src/lib/settings.ts (renamed?)")
	}
	if n, _ := strconv.Atoi(m[1]); n != session.StoppedArchiveNever {
		t.Fatalf("Console STOPPED_ARCHIVE_NEVER = %d, session.StoppedArchiveNever = %d", n, session.StoppedArchiveNever)
	}

	// The Console's stored default has to be the value the Agent reads as "not set", or a user
	// who never opened the row overrides the deployment's AF_SESSION_STOPPED_TTL.
	m = regexp.MustCompile(`sessionStoppedArchiveDays:\s*(-?\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no sessionStoppedArchiveDays default in console/src/lib/settings.ts (renamed?)")
	}
	if n, _ := strconv.Atoi(m[1]); session.NormalizeStoppedArchiveDays(n) != 0 {
		t.Fatalf("Console default sessionStoppedArchiveDays = %d, which the Agent reads as a choice, not as unset", n)
	}
}

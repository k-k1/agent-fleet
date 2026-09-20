package fleetgraph

import (
	"os"
	"testing"
	"time"
)

// TestBuildPage_CoverageSurvivesOneUnparsableTimestamp is B7 from the S-BE review: a
// single lineage line with a corrupt `ts` must not blank coverage.lineageSince for the
// WHOLE page — readLineageFile's parseMillis returns 0 on a parse failure, and treating 0
// as "no minimum seen yet" (rather than a real, tiny timestamp) freezes the running
// minimum at 0 forever, which then reads as "no lineage kept" even though real lineage
// exists.
func TestBuildPage_CoverageSurvivesOneUnparsableTimestamp(t *testing.T) {
	withTempState(t)
	RecordBirth(Birth{Name: "ok1", Kind: "claude", Origin: OriginUser})
	setLastLineageTs(t, "2026-09-01T00:00:00.000Z")

	// Append a line with a garbage ts by hand — RecordBirth always writes a good one, so
	// this simulates the only way a bad one gets in (a corrupted/partial write).
	appendRawLineageLine(t, `{"ev":"birth","ts":"not-a-timestamp","name":"corrupt1","kind":"claude","origin":"user"}`)

	page, err := BuildPage(0, time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if page.Coverage.LineageSince == nil {
		t.Fatal("coverage.lineageSince is nil — one unparsable line blanked the whole page's coverage")
	}
	want := mustMillis(t, "2026-09-01T00:00:00.000Z")
	if *page.Coverage.LineageSince != want {
		t.Fatalf("coverage.lineageSince = %d, want %d", *page.Coverage.LineageSince, want)
	}
}

func appendRawLineageLine(t *testing.T, line string) {
	t.Helper()
	f, err := os.OpenFile(lineagePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

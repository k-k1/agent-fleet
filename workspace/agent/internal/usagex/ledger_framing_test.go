package usagex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A write cut inside a row (a full disk) leaves the day's file ending in a JSON prefix. The next
// append starts on a line of its own, so the prefix is the only thing the reader drops.
func TestAppendAfterATruncatedRowKeepsLineFraming(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("AF_USAGE_RECORD", "")
	seed := Record{TS: time.Now().UTC().Format(time.RFC3339), Feature: FeatureRouteGateway, Ref: "s1", Key: "route.gateway:seed", In: 1}
	if err := AppendRows([]Record{seed}); err != nil {
		t.Fatal(err)
	}
	if got := ReadRows(); len(got) != 1 {
		t.Fatalf("positive control: %d rows, want the seed", len(got))
	}
	day := filepath.Join(RawDir(), time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(day, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"ts":`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	retry := seed
	retry.Key, retry.In = "route.gateway:retry", 2
	if err := AppendRows([]Record{retry}); err != nil {
		t.Fatal(err)
	}
	got := ReadRows()
	if len(got) != 2 || got[1].Key != "route.gateway:retry" {
		t.Fatalf("after a truncated row and a retry: %+v, want the seed and the retry", got)
	}
	// A file that already ends on a line boundary gets no blank line.
	before, _ := os.ReadFile(day)
	if err := AppendRows([]Record{seed}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(day)
	if string(after[len(before):len(before)+1]) != "{" {
		t.Fatalf("an extra newline was written before a row on a clean boundary")
	}
}

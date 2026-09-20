package lcpp

import (
	"os"
	"testing"
)

// The probe behind docs/log/99's re-examination of ADR 0093 decision 3's storage shape. It is
// skipped unless AF_PROBE=1 because its only output is a syscall count, which needs strace
// around it to be worth anything (same idiom as internal/session/meta_probe_test.go):
//
//	go test -c -o /tmp/lcpp-probe ./internal/agents/lcpp/
//	AF_PROBE=1 strace -f -c /tmp/lcpp-probe -test.run '^TestProbeAppend$'
//
// ⚠️ Do NOT filter with `-e trace=file` (or any narrower set): append()'s Close/Write take no
// file name and fall outside that set, same trap internal/session/meta_probe_test.go documents.
// Plain `strace -f -c` with no -e counts everything, which is the only way to be sure nothing is
// silently dropped.
func TestProbeAppend(t *testing.T) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	const records = 500 // order of magnitude of log-coder-rerun2.txt's 434-turn run (docs/log/99)

	home := t.TempDir()
	t.Setenv("HOME", home)
	s := Open("probe-sid")
	t.Logf("probing %d AppendUser calls against %s", records, s.Path())
	for i := 0; i < records; i++ {
		if _, err := s.AppendUser("x"); err != nil {
			t.Fatalf("AppendUser: %v", err)
		}
	}
	recs, truncated, err := s.Records()
	if err != nil || truncated || len(recs) != records {
		t.Fatalf("Records() = %d, truncated=%v, err=%v — the probe measured the wrong thing", len(recs), truncated, err)
	}
}

package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The probes behind ADR 0087's source B. Skipped unless AF_PROBE=1: their output is a
// syscall count, so they are only worth anything under strace.
//
//	go test -c -o /tmp/probe ./internal/agents/claude/
//	AF_PROBE=1 strace -f -c -e trace=getdents64,openat,newfstatat,read,close \
//	  /tmp/probe -test.run '^TestProbeSubagentSafety$'
//
// ⚠️ MEASURE THE TWO SIDES SEPARATELY, and expect DIFFERENT numbers. The display side should
// fall to single digits; the safety side should NOT — it still searches, and demanding that
// it also fall would mean an implementation that deleted the safety check passes the
// measurement. The fixture is the expensive case on both sides: a session with no background
// agents at all, which is most of them, and the one where "not found" is never remembered.
//
// ⚠️ -e trace=file is not enough (read and close carry no file name), and a running Agent
// cannot be attached to (ptrace_scope=1) — hence the real code frozen into a test binary.
// probeCalls is overridable so the per-call cost can be taken as a SLOPE (run at two
// counts, divide the difference): the binary's own startup and the fixture's 39 project
// directories are in every total, and at single-digit per-call costs that baseline is
// bigger than the thing being measured.
func probeCalls(t *testing.T) int {
	if v := os.Getenv("AF_PROBE_CALLS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("AF_PROBE_CALLS=%q: %v", v, err)
		}
		return n
	}
	return 100
}

func TestProbeSubagentSafety(t *testing.T) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	sid, _, _ := fixture(t, 38) // the production workspace measured on 2026-09-16
	for range probeCalls(t) {
		if len(SubagentLogs(sid)) != 0 {
			t.Fatal("the fixture must have no background agents — that is the case being measured")
		}
	}
}

func TestProbeSubagentDisplay(t *testing.T) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	sid, _, _ := fixture(t, 38)
	for range probeCalls(t) {
		if len(subagentLogsDisplay(sid)) != 0 {
			t.Fatal("the fixture must have no background agents")
		}
	}
}

// The transcript side of the same change: memoTTL sends every remembered hit back through
// the search once a minute, so what that re-search costs is what the TTL costs. Derived, it
// is one Lstat; without a cwd to derive from it is the sweep it always was. Measure both the
// same way (slope over AF_PROBE_CALLS) and compare.
func TestProbeTranscriptResearchDerived(t *testing.T) {
	probeTranscriptResearch(t, true)
}

func TestProbeTranscriptResearchSwept(t *testing.T) {
	probeTranscriptResearch(t, false)
}

func probeTranscriptResearch(t *testing.T, derived bool) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	sid, cwd, _ := fixture(t, 38)
	if !derived {
		// A sid no meta has been read for: session.CWDForUUID answers "", so there is
		// nothing to derive from and the search is the original sweep.
		sid = "00000000-0000-5000-8000-000000000001"
	}
	if err := os.WriteFile(filepath.Join(ConfigDir(), "projects", projectKey(cwd), sid+".jsonl"),
		[]byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range probeCalls(t) {
		jsonlMemo = pathMemo{} // what the TTL does once a minute
		if len(jsonlPaths(sid)) != 1 {
			t.Fatalf("the probe measured the wrong thing: %v", jsonlPaths(sid))
		}
	}
}

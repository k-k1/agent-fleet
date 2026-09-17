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
//	  /tmp/probe -test.run '^TestProbeSubagentLookup$'
//
// ⚠️ WHAT IS MEASURED IS A LOOKUP THAT STILL LOOKS AT THE DISK, and a low number is only
// worth something while that stays true. The first implementation of this decision got its
// number from a negative cache, confined to what looked like the two badge call sites —
// review found that one of them is not a badge (WireLive's value travels the wire into the
// CP's reaper, which stops the workspace on it). So
// TestAnAgentStartingIsVisibleImmediately is the other half of this measurement, and neither
// half means anything without the other.
//
// The fixture is the expensive case: a session with no background agents at all, which is
// most of them, and the one whose absence used to cost a sweep on every poll.
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

func TestProbeSubagentLookup(t *testing.T) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	sid, cwd, _ := fixture(t, 38) // the production workspace measured on 2026-09-16
	writeTranscript(t, cwd, sid)  // every live session has one; the lookup hangs off it
	for range probeCalls(t) {
		if len(SubagentLogs(sid)) != 0 {
			t.Fatal("the fixture must have no background agents — that is the case being measured")
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

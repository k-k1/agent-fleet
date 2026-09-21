package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 🔴 The hint tells a session to call a tool. Whether it HAS that tool is a different
// question from whether af writes its config file, and the two used to be the same list.
// muse is where they part: it receives af's MCP servers on the wire (ADR 0095 decision 11)
// and af writes no file for it, so the file-writing list would send it a line naming a tool
// it was told it does not get — a failure whose only symptom is a report that never arrives.
//
// shell and ssm are the pair on the other side: they get no af server at all, and a line
// telling them to call one is a line they would try to run.
func TestSelfReportHintFollowsTheToolNotTheConfigFile(t *testing.T) {
	for _, kind := range []string{session.KindClaude, session.KindCodex, session.KindMuse} {
		if !selfReportToolAvailable(kind) {
			t.Errorf("%s gets the af server but no self-report hint", kind)
		}
	}
	for _, kind := range []string{session.KindShell, session.KindSSM} {
		if selfReportToolAvailable(kind) {
			t.Errorf("%s has no af server and must not be told to call af_report", kind)
		}
	}
}

func TestSelfReportHintIsAppendedForMuse(t *testing.T) {
	m := session.Meta{Kind: session.KindMuse, Name: "muse-abc"}
	got := withSelfReportHint("do the thing", m)
	if !strings.Contains(got, "af_report") || !strings.Contains(got, "muse-abc") {
		t.Fatalf("no hint for a muse session:\n%s", got)
	}
	// Empty input still gets nothing: there is no instruction to finish.
	if withSelfReportHint("  ", m) != "  " {
		t.Error("a hint was appended to an empty prompt")
	}
}

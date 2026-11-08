package memoryx

import (
	"testing"
	"time"
)

var fbNow = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

func fbSetup(t *testing.T) (agentMemCaller, string) {
	t.Helper()
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	return c, rel
}

func fbSave(t *testing.T, c agentMemCaller, name, body string, rev int) {
	t.Helper()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, Revision: rev}, fbNow); err != nil {
		t.Fatal(err)
	}
}

func fbReport(c agentMemCaller, name string, rev int, verdict, note string) (agentMemFeedbackResult, error) {
	return agentMemFeedback(c, agentMemFeedbackReq{Scope: agentMemScopeProject, Name: name, Revision: rev, Verdict: verdict, Note: note}, fbNow)
}

func TestFeedbackCurrentRevisionCountsAndMarks(t *testing.T) {
	c, rel := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	res, err := fbReport(c, "m", 1, "stale", "the flag was renamed")
	if err != nil || res.Outcome != "counted" || res.Commit == "" {
		t.Fatalf("stale: %+v %v", res, err)
	}
	got := lifeStored(t, c, agentMemScopeProject, "m")
	if len(got.Reasons) != 1 || got.Reasons[0] != (agentMemReason{By: "feedback", Rule: "stale", Note: "the flag was renamed", Revision: 1}) || got.Revision != 1 {
		t.Errorf("reasons = %+v at revision %d", got.Reasons, got.Revision)
	}
	if n := agentMemCounterCount(rel, agentMemCounterStale, "m"); n != 1 {
		t.Errorf("stale counter = %d", n)
	}
	// Another session on the same revision still counts: the mark does not move the revision.
	other := agentMemCallerT(t, "codex-wt")
	if res, err := fbReport(other, "m", 1, "wrong", "never true"); err != nil || res.Outcome != "counted" {
		t.Fatalf("second session: %+v %v", res, err)
	}
	if n := len(lifeStored(t, c, agentMemScopeProject, "m").Reasons); n != 2 {
		t.Errorf("reasons = %d, want 2", n)
	}
}

func TestFeedbackConfirmedCountsAndNeverClearsAReason(t *testing.T) {
	c, rel := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	if _, err := fbReport(c, "m", 1, "stale", "x"); err != nil {
		t.Fatal(err)
	}
	other := agentMemCallerT(t, "codex-wt")
	res, err := fbReport(other, "m", 1, "confirmed", "")
	if err != nil || res.Outcome != "counted" || res.Commit != "" {
		t.Fatalf("confirmed: %+v %v", res, err)
	}
	if n := agentMemCounterCount(rel, agentMemCounterConfirmed, "m"); n != 1 {
		t.Errorf("confirmed counter = %d", n)
	}
	if n := len(lifeStored(t, c, agentMemScopeProject, "m").Reasons); n != 1 {
		t.Errorf("a confirmation changed the reasons: %d", n)
	}
}

// A report on revision 3 must not mark revision 4.
func TestFeedbackOlderRevisionIsOnlyAnObservation(t *testing.T) {
	c, rel := fbSetup(t)
	fbSave(t, c, "m", "v1", 0)
	fbSave(t, c, "m", "v2", 1)
	fbSave(t, c, "m", "v3", 2)
	fbSave(t, c, "m", "v4", 3)
	res, err := fbReport(c, "m", 3, "wrong", "v3 was wrong")
	if err != nil || res.Outcome != "observation" || res.Commit != "" {
		t.Fatalf("older: %+v %v", res, err)
	}
	got := lifeStored(t, c, agentMemScopeProject, "m")
	if len(got.Reasons) != 0 || got.Revision != 4 {
		t.Errorf("revision 4 was marked: %+v", got.agentMemLife)
	}
	if agentMemCounterCount(rel, agentMemCounterStale, "m") != 0 || agentMemCounterCount(rel, agentMemCounterConfirmed, "m") != 0 {
		t.Error("an observation moved a counter")
	}
	rs := agentMemFeedbackRecords(rel, "m")
	if len(rs) != 1 || rs[0].Session != c.Session || rs[0].Revision != 3 || rs[0].At == "" {
		t.Errorf("record = %+v", rs)
	}
}

func TestFeedbackOnePerSessionPerRevision(t *testing.T) {
	c, rel := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	if _, err := fbReport(c, "m", 1, "confirmed", ""); err != nil {
		t.Fatal(err)
	}
	res, err := fbReport(c, "m", 1, "stale", "changed my mind")
	if err != nil || res.Outcome != "duplicate" {
		t.Fatalf("duplicate: %+v %v", res, err)
	}
	if n := len(lifeStored(t, c, agentMemScopeProject, "m").Reasons); n != 0 || agentMemCounterCount(rel, agentMemCounterConfirmed, "m") != 1 || len(agentMemFeedbackRecords(rel, "m")) != 1 {
		t.Errorf("the second report counted: reasons %d", n)
	}
}

func TestFeedbackRefusals(t *testing.T) {
	c, _ := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	cases := []struct {
		name string
		call func() error
	}{
		{"future revision", func() error { _, e := fbReport(c, "m", 2, "stale", "x"); return e }},
		{"bad verdict", func() error { _, e := fbReport(c, "m", 1, "meh", "x"); return e }},
		{"no note", func() error { _, e := fbReport(c, "m", 1, "stale", ""); return e }},
		{"multi-line note", func() error { _, e := fbReport(c, "m", 1, "stale", "a\nb"); return e }},
		{"secret in note", func() error {
			_, e := fbReport(c, "m", 1, "stale", "key AKIA"+"ABCDEFGHIJKLMNOP")
			return e
		}},
		{"unknown memory", func() error { _, e := fbReport(c, "nope", 1, "stale", "x"); return e }},
		{"no session", func() error {
			_, e := agentMemFeedback(agentMemCaller{Session: agentMemUnknown, Kind: agentMemUnknown, Project: c.Project},
				agentMemFeedbackReq{Scope: agentMemScopeProject, Name: "m", Revision: 1, Verdict: "stale", Note: "x"}, fbNow)
			return e
		}},
	}
	for _, tc := range cases {
		if err := tc.call(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if n := len(lifeStored(t, c, agentMemScopeProject, "m").Reasons); n != 0 {
		t.Errorf("a refused report left %d reasons", n)
	}
}

func TestFeedbackReasonIsDroppedByABodyChangeOnly(t *testing.T) {
	c, _ := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	if _, err := fbReport(c, "m", 1, "stale", "x"); err != nil {
		t.Fatal(err)
	}
	fbSave(t, c, "m", "new body", 1)
	if n := len(lifeStored(t, c, agentMemScopeProject, "m").Reasons); n != 0 {
		t.Errorf("a body change left %d feedback reasons", n)
	}
}

func TestFeedbackForgetClearsRecords(t *testing.T) {
	c, rel := fbSetup(t)
	fbSave(t, c, "m", "body", 0)
	if _, err := fbReport(c, "m", 1, "confirmed", ""); err != nil {
		t.Fatal(err)
	}
	if len(agentMemFeedbackRecords(rel, "m")) != 1 {
		t.Fatal("no record")
	}
	agentMemClearUsage(rel, "m")
	if len(agentMemFeedbackRecords(rel, "m")) != 0 {
		t.Error("forget left the feedback record")
	}
}

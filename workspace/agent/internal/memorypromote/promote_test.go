package memorypromote

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestImportsNoAFPackage fails when a non-test file of this package imports any Agent Fleet
// package: the engine has to be movable to its own module (ADR 0130 decision 8).
func TestImportsNoAFPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, "github.com/k-k1/") {
				t.Errorf("%s imports %s: the promotion engine must import no AF package", f, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test file was examined")
	}
}

var now = time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

// proven is a memory that meets every requirement; each case below breaks exactly one.
func proven(name string) Memory {
	return Memory{
		Name: name, Kind: "procedure", Type: "project", Revision: 3,
		Updated: now.Add(-8 * 24 * time.Hour), Anchors: []string{"scripts/release.sh"},
		Reports: []Confirmation{{"s1", 3, VerdictConfirmed}, {"s2", 3, VerdictConfirmed}},
	}
}

func rules(h Held) []string {
	var out []string
	for _, m := range h.Missing {
		out = append(out, m.Rule)
	}
	return out
}

func TestSelectEachRuleHoldsOnItsOwn(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Memory)
		want   string
	}{
		{"about-member", func(m *Memory) { m.Type = "user" }, RuleAboutMember},
		{"suspect", func(m *Memory) { m.Reasons = 1 }, RuleOpenReasons},
		{"no-anchors", func(m *Memory) { m.Anchors = nil }, RuleNoAnchors},
		{"lint", func(m *Memory) { m.Findings = []string{"missing-path", "missing-path"} }, RuleLint},
		{"one-session", func(m *Memory) { m.Reports = m.Reports[:1] }, RuleConfirmation},
		// The same session twice on the current revision is still one session.
		{"same-session-twice", func(m *Memory) { m.Reports[1].Session = "s1" }, RuleConfirmation},
		// A confirmation of an earlier body does not count for the current one.
		{"older-revision", func(m *Memory) { m.Reports[1].Revision = 2 }, RuleConfirmation},
		// A stale report is not a confirmation (on an earlier revision, so it disputes nothing here).
		{"not-a-confirmation", func(m *Memory) { m.Reports[1].Verdict, m.Reports[1].Revision = "stale", 2 }, RuleConfirmation},
		{"no-session", func(m *Memory) { m.Reports[1].Session = "" }, RuleConfirmation},
		{"recent", func(m *Memory) { m.Updated = now.Add(-6 * 24 * time.Hour) }, RuleRecent},
		{"unknown-time", func(m *Memory) { m.Updated = time.Time{} }, RuleRecent},
		// A stale or wrong report on the current revision holds it even with no open reason (the
		// reason's commit may have failed); one on an earlier revision does not.
		{"disputed", func(m *Memory) { m.Reports = append(m.Reports, Confirmation{"s3", 3, "wrong"}) }, RuleDisputed},
		{"anchor-not-found", func(m *Memory) { m.Missing = []string{"scripts/release.sh"} }, RuleAnchor},
		{"moved", func(m *Memory) { m.Also = []Miss{{Rule: RuleMoved}} }, RuleMoved},
		{"unpublished", func(m *Memory) { m.Also = []Miss{{Rule: RuleUnpublished}} }, RuleUnpublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := proven(tc.name)
			tc.mutate(&m)
			res := Select([]Memory{proven("ok"), m}, Options{Now: now})
			if len(res.Candidates) != 1 || res.Candidates[0].Name != "ok" {
				t.Fatalf("candidates = %+v", res.Candidates)
			}
			if len(res.Held) != 1 || !reflect.DeepEqual(rules(res.Held[0]), []string{tc.want}) {
				t.Fatalf("held = %+v, want only %s", res.Held, tc.want)
			}
		})
	}
}

func TestSelectIgnoresADisputeOfAnEarlierRevision(t *testing.T) {
	m := proven("m")
	m.Reports = append(m.Reports, Confirmation{"s3", 2, "stale"})
	if r := Select([]Memory{m}, Options{Now: now}); len(r.Candidates) != 1 {
		t.Fatalf("a report on revision 2 held revision 3: %+v", r)
	}
}

func TestSelectNeedsACheckedRepository(t *testing.T) {
	m := proven("m")
	m.Missing = []string{"x"} // not reported again: nothing was checked
	res := Select([]Memory{m}, Options{Now: now, Unverified: "no origin/HEAD"})
	if len(res.Candidates) != 0 || len(res.Held) != 1 || !reflect.DeepEqual(res.Held[0].Missing, []Miss{{Rule: RuleUnverified, Detail: "no origin/HEAD"}}) {
		t.Fatalf("unchecked repository: %+v", res)
	}
}

func TestSelectLeavesOutOtherKindsAndExcluded(t *testing.T) {
	var mems []Memory
	for _, k := range []string{"", "state", "log", "bogus"} {
		m := proven("kind-" + k)
		m.Kind = k
		mems = append(mems, m)
	}
	ex := proven("superseded")
	ex.Excluded = true
	mems = append(mems, ex)
	res := Select(mems, Options{Now: now})
	if len(res.Candidates) != 0 || len(res.Held) != 0 {
		t.Fatalf("got %+v", res)
	}
	for _, k := range []string{"lesson", "procedure", "preference"} {
		m := proven("k")
		m.Kind = k
		if r := Select([]Memory{m}, Options{Now: now}); len(r.Candidates) != 1 {
			t.Errorf("kind %s is not promoted: %+v", k, r)
		}
	}
}

func TestSelectOrderAndOutput(t *testing.T) {
	a, b, c := proven("b-two"), proven("a-two"), proven("c-three")
	c.Reports = append(c.Reports, Confirmation{"s3", 3, VerdictConfirmed})
	res := Select([]Memory{a, b, c}, Options{Now: now})
	var names []string
	for _, x := range res.Candidates {
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "c-three,a-two,b-two" {
		t.Errorf("order = %v", names)
	}
	want := Candidate{Name: "c-three", Revision: 3, Kind: "procedure", Sessions: 3, Anchors: []string{"scripts/release.sh"}, Updated: "2026-11-16T09:00:00Z"}
	if !reflect.DeepEqual(res.Candidates[0], want) {
		t.Errorf("candidate = %+v", res.Candidates[0])
	}
	// The quiet period and the session count can be raised, never lowered below decision 11's two.
	if r := Select([]Memory{a}, Options{Now: now, Quiet: 10 * 24 * time.Hour}); len(r.Candidates) != 0 {
		t.Errorf("a longer quiet period still promotes: %+v", r)
	}
	if r := Select([]Memory{a}, Options{Now: now, MinSessions: 1}); len(r.Candidates) != 1 {
		t.Errorf("min sessions below two changed the result: %+v", r)
	}
	if r := Select([]Memory{a}, Options{Now: now, MinSessions: 3}); len(r.Candidates) != 0 {
		t.Errorf("min sessions 3 still promotes two: %+v", r)
	}
	if r := Select([]Memory{a}, Options{Now: now, MinSessions: 1}); r.Candidates[0].Sessions != 2 {
		t.Errorf("sessions = %d", r.Candidates[0].Sessions)
	}
}

func TestSelectListsEveryMissOfAHeldMemory(t *testing.T) {
	m := proven("m")
	m.Type, m.Reasons, m.Findings, m.Reports, m.Updated = "user", 2, []string{"retired-term", "missing-path"}, nil, now
	res := Select([]Memory{m}, Options{Now: now})
	want := []Miss{
		{Rule: RuleAboutMember},
		{Rule: RuleOpenReasons, Detail: "2 open"},
		{Rule: RuleLint, Detail: "missing-path"},
		{Rule: RuleLint, Detail: "retired-term"},
		{Rule: RuleConfirmation, Detail: "0 of 2 sessions on revision 3"},
		{Rule: RuleRecent, Detail: "changed 2026-11-24T09:00:00Z"},
	}
	if len(res.Held) != 1 || !reflect.DeepEqual(res.Held[0].Missing, want) {
		t.Fatalf("held = %+v", res.Held)
	}
}

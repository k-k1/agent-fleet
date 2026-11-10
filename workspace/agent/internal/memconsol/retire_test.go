package memconsol

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// queuedRetire runs one topic whose both tiers answer retire.
func queuedRetire(t *testing.T, f *fakeStore) Proposal {
	t.Helper()
	f.wrong("flake-note", "comment on #2310 when the test fails", 3)
	cls := classifyAs("retire", "flake-note")
	small, large := &scriptModel{classify: cls}, &scriptModel{classify: cls}
	rep := mustRun(t, input(small, large, t0))
	if rep.Status != RunOK || len(rep.Queued) != 1 || large.nClass != 1 {
		t.Fatalf("report = %+v, large classify calls %d (a retire must be confirmed by the large tier)", rep, large.nClass)
	}
	return pendingList(t, t0)[0]
}

// A retire answer queues one retire item and writes nothing; the preview shows what agents lose;
// approving publishes exactly that item at the revision the engine read.
func TestRetireQueuesAnItemAndApprovePublishesIt(t *testing.T) {
	f := env(t)
	p := queuedRetire(t, f)
	it := p.Items[0]
	if len(p.Items) != 1 || it.Op != OpRetire || it.Name != "flake-note" || it.Revision != 3 || it.Reason != "because" || it.Role != "large" {
		t.Fatalf("items = %+v", p.Items)
	}
	if f.nPublished() != 0 {
		t.Fatal("a run wrote the store")
	}
	v, err := View(p.ID, t0)
	if err != nil || v.Views[0].Stale != "" || v.Views[0].Retire == nil ||
		!strings.Contains(v.Views[0].Retire.IndexLine, "flake-note") || v.Views[0].Retire.StubReason != "because" ||
		!strings.Contains(v.Views[0].Diff, "-comment on #2310") {
		t.Fatalf("view = %+v, %v", v.Views, err)
	}
	got, err := Approve(p.ID, t0.Add(time.Hour))
	if err != nil || got.Status != StatusApproved || got.Items[0].Commit == "" {
		t.Fatalf("approve = %+v, %v", got, err)
	}
	if len(f.published) != 1 || f.published[0].Op != "retire" || f.published[0].Name != "flake-note" ||
		f.published[0].Revision != 3 || f.published[0].Reason != "because" {
		t.Fatalf("published = %+v", f.published)
	}
}

// Approving after the memory changed is refused as stale and writes nothing.
func TestRetireIsRevalidatedAtApproval(t *testing.T) {
	f := env(t)
	p := queuedRetire(t, f)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "flake-note", Description: "d", Type: "project", Revision: 4, Body: "edited"})
	if v, _ := View(p.ID, t0); v.Views[0].Stale == "" {
		t.Error("the view does not flag a moved memory")
	}
	_, err := Approve(p.ID, t0.Add(time.Hour))
	var pr *Problem
	if !errors.As(err, &pr) || pr.Kind != ProbStale || f.nPublished() != 0 {
		t.Fatalf("err = %v, published %d", err, f.nPublished())
	}
}

// A pinned memory is the member's word: the retire is advice, not a write.
func TestRetireOfAPinnedMemoryIsAdviceOnly(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "pinned", Description: "d", Type: "project", Revision: 1, Body: "b", Pinned: true,
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "stale", Note: "old", Revision: 1}}})
	cls := classifyAs("retire", "pinned")
	mustRun(t, input(&scriptModel{classify: cls}, &scriptModel{classify: cls}, t0))
	ps := pendingList(t, t0)
	if len(ps) != 1 || ps[0].Items[0].Op != OpNote || ps[0].Items[0].Advice != "retire" {
		t.Fatalf("pending = %+v", ps)
	}
	if _, err := Approve(ps[0].ID, t0.Add(time.Hour)); err == nil || f.nPublished() != 0 {
		t.Fatalf("an advice-only proposal was approved: %v", err)
	}
}

// A multi-line or overlong model reason is reduced to one line the store accepts.
func TestRetireReasonIsOneLine(t *testing.T) {
	if got := retireReason("closed\n\n  in  October "); got != "closed in October" {
		t.Errorf("got %q", got)
	}
	if got := retireReason(strings.Repeat("あ", 500)); len([]rune(got)) != maxRetireReason {
		t.Errorf("long reason has %d runes", len([]rune(got)))
	}
	if retireReason(" \n") == "" {
		t.Error("an empty reason must get a plain default")
	}
}

// A retire and a patch of the same memory cannot both be in one proposal.
func TestRetireAndPatchOfOneMemoryDoNotClash(t *testing.T) {
	f := env(t)
	f.wrong("a", "one", 1)
	f.wrong("b", "two", 1)
	n := 0
	cls := func(string) (string, error) {
		n++
		if n == 1 {
			return `{"operation":"retire","targets":["a"],"reason":"gone"}`, nil
		}
		return `{"operation":"patch","targets":["a"],"reason":"fix"}`, nil
	}
	small := &scriptModel{classify: cls, patch: patchEdit("one", "uno")}
	large := &scriptModel{classify: classifyAs("retire", "a")}
	mustRun(t, input(small, large, t0))
	for _, p := range pendingList(t, t0) {
		seen := map[string]int{}
		for _, it := range p.Items {
			if it.Op == OpRetire || it.Op == OpPatch {
				seen[it.Name]++
			}
		}
		for name, c := range seen {
			if c > 1 {
				t.Errorf("proposal holds %d publishable items for %s", c, name)
			}
		}
	}
}

// ---- evidence ----------------------------------------------------------------------------

func stateLookup(m map[string]RefState, fail map[string]bool, calls *atomic.Int32) RefLookup {
	return func(ctx context.Context, repo string, n int) (RefState, error) {
		calls.Add(1)
		key := repo + "#" + itoa(n)
		if fail[key] {
			return "", errors.New("network down")
		}
		if s, ok := m[key]; ok {
			return s, nil
		}
		return "", errors.New("not found")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func refInput(small, large *scriptModel, c *RefChecker) RunInput {
	in := input(small, large, t0)
	in.Refs = c
	return in
}

// The live case: an exported observation cites a closed issue. The prompt says so, as a checked
// fact, before the model decides; and with the model leaning no-op nothing is queued.
func TestExportedObservationCitingAClosedIssueIsMarkedInThePrompt(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "anchor", Description: "d", Type: "project", Revision: 1, Body: "b"})
	writeExport(t, exported(1, "create", "opencode-contract-flake-2310", "known flaky test",
		"TestContractSharedServeTerminalSessions is flaky; comment on #2310 when it fails and rerun.", t0.Add(-48*time.Hour)))
	var calls atomic.Int32
	c := &RefChecker{Lookup: stateLookup(map[string]RefState{"k-k1/agent-fleet#2310": RefClosed}, nil, &calls),
		RepoOf: func(string) string { return "k-k1/agent-fleet" }}
	small := &scriptModel{classify: classifyAs("no-op")}
	rep := mustRun(t, refInput(small, nil, c))
	p := small.lastPrompt()
	for _, want := range []string{"## Facts checked by the Agent", "k-k1/agent-fleet#2310 closed", "cites finished work"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if rep.Status != RunOK || len(rep.Queued) != 0 || len(pendingList(t, t0)) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// A note that names only closed issues is shown as such, and a retire answer can follow.
func TestNoteNamingOnlyClosedIssuesIsReportedAsFinished(t *testing.T) {
	f := env(t)
	f.wrong("flake-note", "comment on #2310, fixed by #2312", 3)
	var calls atomic.Int32
	c := &RefChecker{Lookup: stateLookup(map[string]RefState{"k-k1/agent-fleet#2310": RefClosed, "k-k1/agent-fleet#2312": RefMerged}, nil, &calls),
		RepoOf: func(string) string { return "k-k1/agent-fleet" }}
	cls := classifyAs("retire", "flake-note")
	small := &scriptModel{classify: cls}
	rep := mustRun(t, refInput(small, &scriptModel{classify: cls}, c))
	if want := "Note `flake-note` names only finished work (k-k1/agent-fleet#2310 closed, k-k1/agent-fleet#2312 merged)"; !strings.Contains(small.lastPrompt(), want) {
		t.Errorf("prompt lacks %q:\n%s", want, small.lastPrompt())
	}
	if len(rep.Queued) != 1 || pendingList(t, t0)[0].Items[0].Op != OpRetire {
		t.Fatalf("report = %+v", rep)
	}
}

// A failed lookup is unknown: it is not shown, and a note is not called finished when one of the
// issues it names could not be checked.
func TestFailedLookupIsUnknownNeverClosed(t *testing.T) {
	f := env(t)
	f.wrong("flake-note", "comment on #2310, see also #2400", 3)
	var calls atomic.Int32
	c := &RefChecker{Lookup: stateLookup(map[string]RefState{"k-k1/agent-fleet#2310": RefClosed}, map[string]bool{"k-k1/agent-fleet#2400": true}, &calls),
		RepoOf: func(string) string { return "k-k1/agent-fleet" }}
	small := &scriptModel{classify: classifyAs("no-op")}
	mustRun(t, refInput(small, nil, c))
	p := small.lastPrompt()
	facts, _, _ := strings.Cut(p, "## Existing notes")
	if strings.Contains(facts, "only finished work") || strings.Contains(facts, "#2400") {
		t.Errorf("an unchecked issue leaked into the facts:\n%s", p)
	}
	if !strings.Contains(p, "Note `flake-note` names k-k1/agent-fleet#2310 closed.") {
		t.Errorf("the checked half is missing:\n%s", p)
	}
	// With the lookup wired to nothing at all, no facts section appears.
	f2 := env(t)
	f2.wrong("n", "see #2310", 1)
	small2 := &scriptModel{classify: classifyAs("no-op")}
	mustRun(t, refInput(small2, nil, &RefChecker{RepoOf: func(string) string { return "k-k1/agent-fleet" }}))
	if strings.Contains(small2.lastPrompt(), "Facts checked") {
		t.Error("a checker without a lookup produced facts")
	}
}

// Lookups are bounded: results are cached across calls, the run stops after consecutive failures,
// and a slow lookup is cut by its timeout.
func TestRefLookupsAreBounded(t *testing.T) {
	var calls atomic.Int32
	c := &RefChecker{Lookup: stateLookup(map[string]RefState{"o/r#1": RefClosed, "o/r#2": RefOpen}, nil, &calls)}
	run := c.Begin()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if run.State(ctx, "o/r", 1) != RefClosed {
			t.Fatal("closed not returned")
		}
	}
	if calls.Load() != 1 {
		t.Errorf("a repeated reference was looked up %d times", calls.Load())
	}
	// A second run shares the cache.
	if c.Begin().State(ctx, "o/r", 1) != RefClosed || calls.Load() != 1 {
		t.Errorf("the cache is not shared across runs (%d lookups)", calls.Load())
	}
	// An open answer expires sooner than a closed one.
	now := time.Now()
	c.Now = func() time.Time { return now }
	c.cache = nil
	c.Begin().State(ctx, "o/r", 2)
	c.Now = func() time.Time { return now.Add(refOpenTTL + time.Minute) }
	before := calls.Load()
	c.Begin().State(ctx, "o/r", 2)
	if calls.Load() != before+1 {
		t.Error("an open answer outlived its TTL")
	}

	// Consecutive failures stop the run's lookups.
	var fcalls atomic.Int32
	down := &RefChecker{Lookup: func(context.Context, string, int) (RefState, error) { fcalls.Add(1); return "", errors.New("down") }}
	dr := down.Begin()
	for i := 1; i <= 10; i++ {
		if dr.State(ctx, "o/r", i) != "" {
			t.Fatal("a failure was reported as a state")
		}
	}
	if fcalls.Load() != refFailuresStop {
		t.Errorf("%d lookups after the network went down, want %d", fcalls.Load(), refFailuresStop)
	}

	// The cap holds, and a lookup that never answers is cut by the timeout.
	var ccalls atomic.Int32
	many := &RefChecker{Lookup: func(context.Context, string, int) (RefState, error) { ccalls.Add(1); return RefOpen, nil }}
	mr := many.Begin()
	for i := 1; i <= refCapPerRun+10; i++ {
		mr.State(ctx, "o/r", i)
	}
	if ccalls.Load() != refCapPerRun {
		t.Errorf("%d lookups in one run, cap %d", ccalls.Load(), refCapPerRun)
	}
	hang := &RefChecker{Lookup: func(ctx context.Context, _ string, _ int) (RefState, error) { <-ctx.Done(); return "", ctx.Err() }}
	start := time.Now()
	if hang.Begin().State(ctx, "o/r", 1) != "" || time.Since(start) > refCallTimeout+2*time.Second {
		t.Error("a hanging lookup was not cut")
	}
}

// A reference that can never be looked up (a 404) is unknown but says nothing about the network:
// it does not trip the consecutive-failure stop, and it is asked once per run.
func TestPermanentFailureDoesNotStopTheRunsLookups(t *testing.T) {
	var calls atomic.Int32
	c := &RefChecker{Lookup: func(_ context.Context, repo string, n int) (RefState, error) {
		calls.Add(1)
		if repo == "o/private" {
			return "", ErrRefGone
		}
		return RefClosed, nil
	}}
	r := c.Begin()
	ctx := context.Background()
	for i := 1; i <= 2*refFailuresStop; i++ {
		if r.State(ctx, "o/private", i) != "" {
			t.Fatal("a 404 produced a state")
		}
	}
	if r.State(ctx, "o/private", 1) != "" || calls.Load() != int32(2*refFailuresStop) {
		t.Errorf("a gone reference was asked again (%d calls)", calls.Load())
	}
	if r.State(ctx, "o/public", 1) != RefClosed {
		t.Error("permanent failures silenced the run's later lookups")
	}
}

// The view flags a retire whose memory was archived or superseded after it was queued, as approval will.
func TestRetireViewFlagsAnExcludedMemory(t *testing.T) {
	f := env(t)
	p := queuedRetire(t, f)
	f.excluded["project:"+testProject+"/flake-note"] = true
	if v, _ := View(p.ID, t0); v.Views[0].Stale == "" {
		t.Error("the view shows an already-archived memory as approvable")
	}
}

func TestExtractRefs(t *testing.T) {
	text := "see #12, k-k1/x#7 and https://github.com/o/p/pull/99; not &#123; or C#3 or color #123456 or a/b/c#4; #12 again"
	var got []string
	for _, k := range extractRefs(text, "k-k1/agent-fleet") {
		got = append(got, k.String())
	}
	want := "k-k1/agent-fleet#12 k-k1/x#7 o/p#99"
	if strings.Join(got, " ") != want {
		t.Errorf("refs = %v, want %s", got, want)
	}
	if n := len(extractRefs("see #12", "")); n != 0 {
		t.Errorf("a bare number without a repository gave %d refs", n)
	}
}

func TestRefStateOf(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   RefState
		bad    bool
	}{
		{200, `{"state":"open"}`, RefOpen, false},
		{200, `{"state":"closed"}`, RefClosed, false},
		{200, `{"state":"closed","pull_request":{"merged_at":"2026-10-22T00:00:00Z"}}`, RefMerged, false},
		{200, `{"state":"closed","pull_request":{"merged_at":null}}`, RefClosed, false},
		{404, `{"state":"closed"}`, "", true},
		{403, ``, "", true},
		{401, ``, "", true},
		{200, `not json`, "", true},
		{200, `{"state":"locked"}`, "", true},
	}
	for _, c := range cases {
		got, err := refStateOf(c.status, strings.NewReader(c.body))
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%d %s: got %q, %v", c.status, c.body, got, err)
		}
		if gone := errors.Is(err, ErrRefGone); gone != (c.status == 404) {
			t.Errorf("%d: ErrRefGone = %v", c.status, gone)
		}
	}
}

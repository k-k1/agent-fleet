package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
)

func bbOpen() *branchpr.PR {
	return &branchpr.PR{Number: 4, State: "open", URL: "https://bitbucket.org/w/r/pull-requests/4", Head: "c0ffee4",
		Checks: "success", ChecksDigest: "d1", Mergeable: "MERGEABLE", ReviewDecision: "APPROVED"}
}

func bbSource(pr **branchpr.PR) *Bitbucket {
	return &Bitbucket{Read: func(_ context.Context, repo string, n int) (*branchpr.PR, error) {
		if repo != "w/r" || n != 4 {
			return nil, nil
		}
		return *pr, nil
	}}
}

func TestBitbucketPollReportsFromTheCache(t *testing.T) {
	pr := bbOpen()
	b := bbSource(&pr)
	evs, cur, hint, err := b.Poll(context.Background(), "bitbucket:w/r#4", nil, nil)
	if err != nil || types(evs) != "pr.checks.completed,pr.mergeable,pr.ready" || hint.After != bbOpenAfter || evs[0].Failed {
		t.Fatalf("green PR: %v %s %v", err, types(evs), hint)
	}
	v1 := evs[0].Version

	// A re-run on the same head is a new check version; a failure names the checks.
	pr.Checks, pr.ChecksDigest = "failure", "d2"
	pr.FailingChecks = []branchpr.Check{{Name: "unit", URL: "https://ci.test/1"}}
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if types(evs) != "pr.checks.completed,pr.checks.failed,pr.mergeable" || evs[0].Version == v1 || evs[1].Data[0].Text != "unit" || !evs[0].Failed {
		t.Fatalf("failed checks: %s %+v", types(evs), evs)
	}

	// Running checks and an unknown mergeability report nothing about either.
	pr.ChecksPending, pr.Mergeable = true, "UNKNOWN"
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if types(evs) != "" {
		t.Fatalf("pending: %s", types(evs))
	}

	// A conflict that clears and comes back on the same head is a new episode.
	pr.ChecksPending, pr.Mergeable = false, "CONFLICTING"
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	first := ""
	for _, e := range evs {
		if e.Type == "pr.conflict" {
			first = e.Version
		}
	}
	pr.Mergeable = "MERGEABLE"
	_, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	pr.Mergeable = "CONFLICTING"
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	second := ""
	for _, e := range evs {
		if e.Type == "pr.conflict" {
			second = e.Version
		}
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("conflict versions %q %q", first, second)
	}

	// A failed conflict read in the middle of one conflict is not its end.
	pr.Mergeable = "UNKNOWN"
	_, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	pr.Mergeable = "CONFLICTING"
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	for _, e := range evs {
		if e.Type == "pr.conflict" && e.Version != second {
			t.Fatalf("an unknown read started a new conflict episode: %q after %q", e.Version, second)
		}
	}

	// A conflict held through an unknown read, then a decline and a reopen still in conflict:
	// the reopened conflict is a new episode. Run on a copy so the rest of the test is unchanged.
	{
		c := cur
		pr.Mergeable = "UNKNOWN"
		_, c, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", c, nil)
		pr.State = "closed"
		_, c, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", c, nil)
		pr.State, pr.Mergeable = "open", "CONFLICTING"
		evs, _, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", c, nil)
		reopened := ""
		for _, e := range evs {
			if e.Type == "pr.conflict" {
				reopened = e.Version
			}
		}
		if reopened == "" || reopened == second {
			t.Fatalf("a conflict after a reopen kept the old episode: %q (before %q)", reopened, second)
		}
	}

	// Change requests count episodes the same way.
	pr.ChangesRequested = true
	evs, _, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if !strings.Contains(types(evs), "pr.review.changes_requested") {
		t.Fatalf("change request: %s", types(evs))
	}

	pr = &branchpr.PR{Number: 4, State: "merged"}
	evs, _, hint, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if types(evs) != "pr.merged" || !hint.Settled {
		t.Fatalf("merged: %s %+v", types(evs), hint)
	}
}

func TestBitbucketResolveAndRefusals(t *testing.T) {
	pr := bbOpen()
	b := bbSource(&pr)
	ctx := context.Background()
	for _, ref := range []string{"w/r#4", "#4", "W/R#4", "https://bitbucket.org/w/r/pull-requests/4/overview"} {
		res, err := b.Resolve(ctx, ref, "w/r")
		if err != nil || res.Subject != "bitbucket:w/r#4" || res.URL != pr.URL {
			t.Errorf("Resolve(%q) = %+v %v", ref, res, err)
		}
	}
	var ref *ErrRefused
	for _, bad := range []string{"w/r#5", "w/r", "../r#4", "w/r4"} {
		if _, err := b.Resolve(ctx, bad, "w/r"); !errors.As(err, &ref) {
			t.Errorf("Resolve(%q) = %v, want a refusal", bad, err)
		}
	}
	if strings.Contains(strings.Join(b.Types(), ","), "threads") {
		t.Fatal("threads offered on Bitbucket")
	}
	nc := &Bitbucket{Read: func(context.Context, string, int) (*branchpr.PR, error) {
		_, err := branchpr.NewBitbucket(func() (string, error) { return "", nil }).Fetch(context.Background(), []branchpr.Key{{Repo: "w/r", Number: 1}})
		return nil, err
	}}
	if _, err := nc.Resolve(ctx, "w/r#4", ""); !errors.As(err, &ref) {
		t.Fatalf("not connected: %v", err)
	}
	var gone *ErrGone
	if _, _, _, err := b.Poll(ctx, "bitbucket:w/r#5", json.RawMessage(nil), nil); !errors.As(err, &gone) {
		t.Fatalf("a vanished PR = %v, want ErrGone", err)
	}
	failing := &Bitbucket{Read: func(context.Context, string, int) (*branchpr.PR, error) { return nil, errors.New("boom") }}
	if _, _, _, err := failing.Poll(ctx, "bitbucket:w/r#4", nil, nil); err == nil || errors.As(err, &gone) {
		t.Fatalf("a failed read = %v, want a plain error", err)
	}
}

func TestBitbucketReadyOnlyWhenEverythingHolds(t *testing.T) {
	for name, spoil := range map[string]func(*branchpr.PR){
		"draft":       func(p *branchpr.PR) { p.Draft = true },
		"unknown":     func(p *branchpr.PR) { p.Mergeable = "UNKNOWN" },
		"no approval": func(p *branchpr.PR) { p.ReviewDecision = "REVIEW_REQUIRED" },
		"changes":     func(p *branchpr.PR) { p.ReviewDecision = "CHANGES_REQUESTED"; p.ChangesRequested = true },
		"checks run":  func(p *branchpr.PR) { p.Checks, p.ChecksPending = "pending", true },
		"checks fail": func(p *branchpr.PR) { p.Checks = "failure" },
		"no checks":   func(p *branchpr.PR) { p.Checks = "" },
	} {
		pr := bbOpen()
		spoil(pr)
		evs, _, _, _ := bbSource(&pr).Poll(context.Background(), "bitbucket:w/r#4", nil, nil)
		if strings.Contains(types(evs), "pr.ready") {
			t.Errorf("%s: ready reported: %s", name, types(evs))
		}
	}
}

func TestBitbucketMergeableAgainAndClosedAgainAreNews(t *testing.T) {
	pr := bbOpen()
	b := bbSource(&pr)
	version := func(evs []Event, typ string) string {
		for _, e := range evs {
			if e.Type == typ {
				return e.Version
			}
		}
		return ""
	}
	evs, cur, _, _ := b.Poll(context.Background(), "bitbucket:w/r#4", nil, nil)
	m1 := version(evs, "pr.mergeable")
	pr.Mergeable = "CONFLICTING"
	_, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	pr.Mergeable = "MERGEABLE"
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if m2 := version(evs, "pr.mergeable"); m1 == "" || m2 == m1 || version(evs, "pr.ready") != m2 {
		t.Fatalf("mergeable versions %q %q, ready %q", m1, m2, version(evs, "pr.ready"))
	}
	pr = &branchpr.PR{Number: 4, State: "closed"}
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	c1 := version(evs, "pr.closed")
	pr = bbOpen()
	_, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	pr = &branchpr.PR{Number: 4, State: "closed"}
	evs, _, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if c2 := version(evs, "pr.closed"); c1 == "" || c1 == c2 {
		t.Fatalf("closed versions %q %q", c1, c2)
	}
}

func TestBitbucketWatchesAreCappedPerWorkspace(t *testing.T) {
	pr := bbOpen()
	e, _, _ := newTestEngine(bbSource(&pr), &fakeSink{answer: Wait})
	ctx := context.Background()
	for i := 0; i < bbMaxSubscriptions; i++ {
		owner := "o" + string(rune('a'+i))
		if _, err := e.Subscribe(ctx, Request{Owner: owner, Source: "bitbucket", Ref: "w/r#4", Types: []string{"pr.merged"}}); err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
	}
	var ref *ErrRefused
	if _, err := e.Subscribe(ctx, Request{Owner: "z", Source: "bitbucket", Ref: "w/r#4", Types: []string{"pr.merged"}}); !errors.As(err, &ref) {
		t.Fatalf("an eleventh Bitbucket watch: %v", err)
	}
}

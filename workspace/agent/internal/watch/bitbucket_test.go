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
	if err != nil || types(evs) != "pr.checks.completed,pr.mergeable,pr.ready" || hint.After != bbOpenAfter {
		t.Fatalf("green PR: %v %s %v", err, types(evs), hint)
	}
	v1 := evs[0].Version

	// A re-run on the same head is a new check version; a failure names the checks.
	pr.Checks, pr.ChecksDigest = "failure", "d2"
	pr.FailingChecks = []branchpr.Check{{Name: "unit", URL: "https://ci.test/1"}}
	evs, cur, _, _ = b.Poll(context.Background(), "bitbucket:w/r#4", cur, nil)
	if types(evs) != "pr.checks.completed,pr.checks.failed,pr.mergeable" || evs[0].Version == v1 || evs[1].Data[0].Text != "unit" {
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
	if _, _, _, err := b.Poll(ctx, "bitbucket:w/r#5", json.RawMessage(nil), nil); err == nil {
		t.Fatal("a vanished PR read as fine")
	}
}

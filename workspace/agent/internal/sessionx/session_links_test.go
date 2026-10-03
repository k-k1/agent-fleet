package sessionx

import (
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stubLinks replaces the three lookups annotateLinks reads for one test.
func stubLinks(t *testing.T, origins map[string]string, prs map[branchpr.Key]*branchpr.PR, ports map[string][]int) *[][]branchpr.Key {
	t.Helper()
	oldPR, oldPorts, oldOrigin := lookupPRs, lookupPorts, originOf
	t.Cleanup(func() { lookupPRs, lookupPorts, originOf = oldPR, oldPorts, oldOrigin })
	var asked [][]branchpr.Key
	lookupPRs = func(keys []branchpr.Key, _ time.Time) map[branchpr.Key]*branchpr.PR {
		asked = append(asked, keys)
		return prs
	}
	lookupPorts = func(time.Time) map[string][]int { return ports }
	originOf = func(dir string, _ time.Time) string { return origins[dir] }
	return &asked
}

func TestAnnotateLinksMatchesEachRowToItsOwnBranchAndPorts(t *testing.T) {
	open := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7", Checks: "failure"}
	merged := &branchpr.PR{Number: 5, State: "merged", URL: "https://github.com/o/r/pull/5"}
	asked := stubLinks(t,
		map[string]string{"/wt/a": "o/r", "/wt/b": "o/r", "/bb": ""},
		map[branchpr.Key]*branchpr.PR{
			{Repo: "o/r", Branch: "feature/a"}: open,
			{Repo: "o/r", Branch: "fix/b"}:     merged,
		},
		map[string][]int{"a": {5173}, "b": {8080}, "c": {9000}, "gone": {3000}},
	)
	sessions := []session.Session{
		// Started on feature/a; the copy is still there.
		{Name: "a", Dir: "/wt/a", Branch: "feature/a", Alive: true},
		// Stopped: its PR still shows, its ports cannot (nothing of it is running).
		{Name: "b", Dir: "/wt/b", Branch: "fix/b", Alive: false},
		// A Bitbucket working copy: no PR lookup at all.
		{Name: "c", Dir: "/bb", Branch: "x", Alive: true},
		// No working copy (an orphan tmux row).
		{Name: "d", Alive: true},
	}
	annotateLinks(sessions, map[string]string{"/wt/a": "feature/a", "/wt/b": "fix/b", "/bb": "x"}, time.Now())

	if sessions[0].PR != open || !reflect.DeepEqual(sessions[0].Ports, []int{5173}) {
		t.Errorf("a: pr=%+v ports=%v, want #7 and [5173]", sessions[0].PR, sessions[0].Ports)
	}
	if sessions[1].PR != merged || sessions[1].Ports != nil {
		t.Errorf("b: pr=%+v ports=%v, want #5 and no ports", sessions[1].PR, sessions[1].Ports)
	}
	// Ports go by the session's own name only: a row never shows another session's server.
	if !reflect.DeepEqual(sessions[2].Ports, []int{9000}) || sessions[3].Ports != nil {
		t.Errorf("c/d: ports=%v/%v, want [9000] and none", sessions[2].Ports, sessions[3].Ports)
	}
	if sessions[2].PR != nil || sessions[3].PR != nil {
		t.Errorf("c/d: pr=%+v/%+v, want none", sessions[2].PR, sessions[3].PR)
	}
	if len(*asked) != 1 || len((*asked)[0]) != 2 {
		t.Fatalf("asked = %v, want one lookup carrying the two GitHub branches", *asked)
	}
}

func TestAnnotateLinksFollowsTheWorkingCopyAfterADrift(t *testing.T) {
	pr := &branchpr.PR{Number: 9, State: "open"}
	stubLinks(t, map[string]string{"/wt": "o/r"},
		map[branchpr.Key]*branchpr.PR{{Repo: "o/r", Branch: "moved"}: pr}, nil)
	sessions := []session.Session{{Name: "a", Dir: "/wt", Branch: "started", Alive: true}}
	annotateLinks(sessions, map[string]string{"/wt": "moved"}, time.Now())
	if sessions[0].PR != pr {
		t.Fatalf("pr = %+v, want the PR of the branch the copy is on now", sessions[0].PR)
	}
}

func TestAnnotateLinksAsksNothingWithoutAGitHubRow(t *testing.T) {
	asked := stubLinks(t, map[string]string{"/x": ""}, nil, nil)
	sessions := []session.Session{{Name: "a", Dir: "/x", Branch: "b"}}
	annotateLinks(sessions, map[string]string{"/x": "(detached)"}, time.Now())
	if len(*asked) != 0 {
		t.Fatalf("asked = %v, want no PR lookup", *asked)
	}
}

func TestAnnotateLinksShowsNoPRForADetachedHead(t *testing.T) {
	started := &branchpr.PR{Number: 4, State: "open"}
	asked := stubLinks(t, map[string]string{"/wt": "o/r"},
		map[branchpr.Key]*branchpr.PR{{Repo: "o/r", Branch: "started"}: started}, nil)
	sessions := []session.Session{{Name: "a", Dir: "/wt", Branch: "started", Alive: true}}
	annotateLinks(sessions, map[string]string{"/wt": "(detached)"}, time.Now())
	if sessions[0].PR != nil || len(*asked) != 0 {
		t.Fatalf("pr = %+v asked = %v, want no PR: the copy is no longer on its start branch", sessions[0].PR, *asked)
	}
	// An unread working copy (no answer for the dir) still falls back to the start branch.
	annotateLinks(sessions, map[string]string{}, time.Now())
	if sessions[0].PR != started {
		t.Fatalf("pr = %+v, want the start branch's PR when the copy was not read", sessions[0].PR)
	}
}

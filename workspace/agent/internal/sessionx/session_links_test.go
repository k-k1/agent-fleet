package sessionx

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stubLinks replaces the lookups annotateLinks reads for one test. An origin "bb:w/r" is a
// bitbucket.org repository, any other non-empty one a github.com repository. Bitbucket answers
// come from bbPRs; asked records GitHub's lookups only.
func stubLinks(t *testing.T, origins map[string]string, prs map[branchpr.Key]*branchpr.PR, ports map[string][]int) *[][]branchpr.Key {
	t.Helper()
	return stubLinksBB(t, origins, prs, nil, ports, nil)
}

func stubLinksBB(t *testing.T, origins map[string]string, prs, bbPRs map[branchpr.Key]*branchpr.PR,
	ports map[string][]int, bbAsked *[][]branchpr.Key) *[][]branchpr.Key {
	t.Helper()
	oldPR, oldBB, oldPorts, oldOrigin := lookupPRs, lookupBitbucketPRs, lookupPorts, originOf
	t.Cleanup(func() { lookupPRs, lookupBitbucketPRs, lookupPorts, originOf = oldPR, oldBB, oldPorts, oldOrigin })
	var asked [][]branchpr.Key
	lookupPRs = func(keys []branchpr.Key, _ time.Time) map[branchpr.Key]*branchpr.PR {
		asked = append(asked, keys)
		return prs
	}
	lookupBitbucketPRs = func(keys []branchpr.Key, _ time.Time) map[branchpr.Key]*branchpr.PR {
		if bbAsked == nil {
			t.Fatalf("Bitbucket asked about %v", keys)
		}
		*bbAsked = append(*bbAsked, keys)
		return bbPRs
	}
	lookupPorts = func(time.Time) map[string][]int { return ports }
	originOf = func(dir string, _ time.Time) originRepo {
		o := origins[dir]
		if r, ok := strings.CutPrefix(o, "bb:"); ok {
			return originRepo{"bitbucket", r}
		}
		if o == "" {
			return originRepo{}
		}
		return originRepo{"github", o}
	}
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
		// An origin on a host with no PR lookup.
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

func TestAnnotateLinksAsksBitbucketForABitbucketCopyOnly(t *testing.T) {
	gh := &branchpr.PR{Number: 7, State: "open", URL: "https://github.com/o/r/pull/7"}
	bb := &branchpr.PR{Number: 3, State: "merged", URL: "https://bitbucket.org/w/r/pull-requests/3"}
	var bbAsked [][]branchpr.Key
	// The same owner/name and branch on both hosts: each row must get its own provider's answer.
	asked := stubLinksBB(t, map[string]string{"/gh": "w/r", "/bb": "bb:w/r"},
		map[branchpr.Key]*branchpr.PR{{Repo: "w/r", Branch: "f"}: gh},
		map[branchpr.Key]*branchpr.PR{{Repo: "w/r", Branch: "f"}: bb},
		nil, &bbAsked)
	sessions := []session.Session{{Name: "a", Dir: "/gh", Branch: "f"}, {Name: "b", Dir: "/bb", Branch: "f"}}
	annotateLinks(sessions, map[string]string{"/gh": "f", "/bb": "f"}, time.Now())
	if sessions[0].PR != gh || sessions[1].PR != bb {
		t.Fatalf("pr = %+v / %+v, want GitHub's #7 and Bitbucket's #3", sessions[0].PR, sessions[1].PR)
	}
	if len(*asked) != 1 || len((*asked)[0]) != 1 || len(bbAsked) != 1 || len(bbAsked[0]) != 1 {
		t.Fatalf("asked github=%v bitbucket=%v, want one key each", *asked, bbAsked)
	}
}

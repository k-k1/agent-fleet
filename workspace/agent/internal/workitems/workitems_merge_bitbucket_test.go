package workitems

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Bitbucket half of the merge write: the head the panel displayed is checked against a fresh
// read before anything is sent, and Bitbucket's own refusals reach the Console with a code.

type bbMergeFake struct {
	srv       *httptest.Server
	mergeBody string // the POST body, "" when no merge was sent
	posted    bool
	// location is sent as the Location header of the merge answer; task serves its status link.
	location string
	task     []string // successive task-status bodies (the last one repeats)
	taskHits int
	head     string // the full commit id the commit API returns ("" = fullHeadA)
}

// fullHeadA and fullHeadB share their first 12 characters: what the 12-character payload head
// cannot tell apart.
const (
	fullHeadA = "ab37a6993eb1" + "0000000000000000000000000000"
	fullHeadB = "ab37a6993eb1" + "ffffffffffffffffffffffffffff"
)

// fakeBitbucketMerge serves the pre-merge read (`pr`) and answers the merge with status/reply.
func fakeBitbucketMerge(t *testing.T, pr string, status int, reply string) *bbMergeFake {
	t.Helper()
	f := &bbMergeFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge"):
			b, _ := io.ReadAll(r.Body)
			f.mergeBody, f.posted = string(b), true
			if f.location != "" {
				w.Header().Set("Location", f.location)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		case strings.Contains(r.URL.Path, "/task-status/"):
			i := f.taskHits
			if i >= len(f.task) {
				i = len(f.task) - 1
			}
			f.taskHits++
			if rest, failed := strings.CutPrefix(f.task[i], "400:"); failed {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(rest))
				return
			}
			_, _ = w.Write([]byte(f.task[i]))
		case strings.Contains(r.URL.Path, "/commit/"):
			// The commit API answers with the full id of whatever the pull request's head is now.
			h := f.head
			if h == "" {
				h = fullHeadA
			}
			_, _ = w.Write([]byte(`{"hash":"` + h + `"}`))
		default:
			_, _ = w.Write([]byte(strings.ReplaceAll(pr, "BASE", f.srv.URL)))
		}
	}))
	prev, prevEvery := BitbucketAPIBase, bitbucketMergePollEvery
	BitbucketAPIBase, bitbucketMergePollEvery = f.srv.URL, time.Millisecond
	t.Cleanup(func() { BitbucketAPIBase, bitbucketMergePollEvery = prev, prevEvery; f.srv.Close() })
	return f
}

// bbPR is the pre-merge read: the abbreviated head and the link the payload carries (BASE is the
// fake's address, filled in when it serves).
func bbPR(state, short string) string {
	return `{"state":"` + state + `","source":{"commit":{"hash":"` + short + `","links":{"self":{"href":"BASE/2.0/repositories/ws/repo/commit/` + short + `"}}}}}`
}

func TestBitbucketMergeSendsStrategyAndCloseSource(t *testing.T) {
	f := fakeBitbucketMerge(t, bbPR("OPEN", "ab37a6993eb1"), 200, `{"merge_commit":{"hash":"feedfacefeed"}}`)
	no := false
	got, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "squash", &no)
	if err != nil || got != "feedfacefeed" {
		t.Fatalf("got %q, %v", got, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(f.mergeBody), &body)
	if body["merge_strategy"] != "squash" || body["close_source_branch"] != false {
		t.Errorf("merge body = %s", f.mergeBody)
	}
	// Left unset, the pull request's own setting applies: the field is not sent at all.
	if _, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.mergeBody, "close_source_branch") {
		t.Errorf("close_source_branch sent without a choice: %s", f.mergeBody)
	}
}

// The guard compares full commit ids. Two commits sharing the 12 characters the payload carries
// are different heads, and a head that cannot be resolved is not merged either.
func TestBitbucketMergeRefusesBeforeSendingWhenThePanelIsStale(t *testing.T) {
	for name, tc := range map[string]struct {
		pr, head, code string
		wantErr        string // for a plain (non-coded) refusal
	}{
		"head moved":                         {bbPR("OPEN", "999999999999"), "999999999999" + "1111111111111111111111111111", "head_changed", ""},
		"same 12 characters, another commit": {bbPR("OPEN", "ab37a6993eb1"), fullHeadB, "head_changed", ""},
		"head not resolvable":                {bbPR("OPEN", "ab37a6993eb1"), "abc", "", "full id"},
		"no head in the payload":             {`{"state":"OPEN"}`, "", "", "where the head commit is"},
		"merged":                             {bbPR("MERGED", "ab37a6993eb1"), "", "not_mergeable", ""},
		"declined":                           {bbPR("DECLINED", "ab37a6993eb1"), "", "not_mergeable", ""},
	} {
		f := fakeBitbucketMerge(t, tc.pr, 200, `{}`)
		f.head = tc.head
		_, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil)
		me, coded := err.(*githubMergeError)
		switch {
		case err == nil:
			t.Errorf("%s: merged", name)
		case tc.code != "" && (!coded || me.code != tc.code):
			t.Errorf("%s: err = %v, want code %s", name, err, tc.code)
		case tc.code == "" && (coded || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want a plain error containing %q", name, err, tc.wantErr)
		}
		if f.posted {
			t.Errorf("%s: a merge was sent for a stale or unconfirmed head", name)
		}
	}
}

func TestBitbucketMergeOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		reply    string
		code     string // "" = a plain provider error
		contains string
	}{
		"conflict":       {409, `{"error":{"message":"You can't merge until you resolve all merge conflicts."}}`, "not_mergeable", "resolve all merge conflicts"},
		"failed check":   {400, `{"error":{"message":"Merge checks failed"}}`, "not_mergeable", "Merge checks failed"},
		"no write scope": {403, `{}`, "", "write permission"},
		"gone":           {404, `{}`, "", "no ws/repo#1"},
	} {
		fakeBitbucketMerge(t, bbPR("OPEN", "ab37a6993eb1"), tc.status, tc.reply)
		_, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil)
		if err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.contains)
			continue
		}
		me, isMerge := err.(*githubMergeError)
		if (tc.code == "") == isMerge || (isMerge && me.code != tc.code) {
			t.Errorf("%s: err = %#v, want code %q", name, err, tc.code)
		}
	}
}

// A 202 is followed through the task-status link for a bounded time: a task that finishes is a
// merge, one that fails is a refusal, one still running is reported as in flight.
func TestBitbucketMergeFollowsTheTaskOf202(t *testing.T) {
	for name, tc := range map[string]struct {
		task     []string
		location string // "" = the fake's own task link
		wantSHA  string
		code     string
	}{
		"succeeds after pending": {[]string{`{"task_status":"PENDING"}`, `{"task_status":"SUCCESS","merge_result":{"merge_commit":{"hash":"feedfacefeed"}}}`}, "", "feedfacefeed", ""},
		"stays pending":          {[]string{`{"task_status":"PENDING"}`}, "", "", "merge_pending"},
		"no link given":          {[]string{`{"task_status":"SUCCESS"}`}, "none", "", "merge_pending"},
	} {
		f := fakeBitbucketMerge(t, bbPR("OPEN", "ab37a6993eb1"), 202, `{}`)
		f.task = tc.task
		switch tc.location {
		case "":
			f.location = f.srv.URL + "/2.0/repositories/ws/repo/pullrequests/1/merge/task-status/t1"
		case "none":
		default:
			f.location = tc.location
		}
		got, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil)
		me, coded := err.(*githubMergeError)
		switch {
		case tc.code == "" && (err != nil || got != tc.wantSHA):
			t.Errorf("%s: got %q, %v", name, got, err)
		case tc.code != "" && (!coded || me.code != tc.code):
			t.Errorf("%s: err = %v, want code %s", name, err, tc.code)
		}
	}
	// A task that fails after being accepted is a refusal, with Bitbucket's own words.
	f := fakeBitbucketMerge(t, bbPR("OPEN", "ab37a6993eb1"), 202, `{}`)
	f.location = f.srv.URL + "/2.0/repositories/ws/repo/pullrequests/1/merge/task-status/t1"
	f.task = []string{`{"task_status":"PENDING"}`, `400:{"error":{"message":"left the merge queue"}}`}
	_, err := bitbucketMergePullRequest("Bearer x", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil)
	if me, ok := err.(*githubMergeError); !ok || me.code != "not_mergeable" || !strings.Contains(me.msg, "left the merge queue") {
		t.Errorf("failed task: err = %v", err)
	}
}

// The Authorization header goes with every poll, so a task link on another host is never followed.
func TestBitbucketMergeNeverFollowsAForeignTaskLink(t *testing.T) {
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"task_status":"SUCCESS","merge_result":{"merge_commit":{"hash":"deadbeefdead"}}}`))
	}))
	defer other.Close()
	f := fakeBitbucketMerge(t, bbPR("OPEN", "ab37a6993eb1"), 202, `{}`)
	f.location = other.URL + "/2.0/repositories/ws/repo/pullrequests/1/merge/task-status/x"
	_, err := bitbucketMergePullRequest("Bearer secret", "ws/repo", 1, "ws/repo#1", fullHeadA, "merge_commit", nil)
	if me, ok := err.(*githubMergeError); !ok || me.code != "merge_pending" {
		t.Errorf("err = %v, want merge_pending", err)
	}
	if hits != 0 {
		t.Errorf("the foreign host was called %d times", hits)
	}
}

// The handler refuses a bad request before it reads the connection: nothing to send, no store.
func TestHandleBitbucketMergeValidatesFirst(t *testing.T) {
	for name, c := range map[string]struct{ key, sha, method string }{
		"bad key":        {"nope", "ab37a6993eb1", "merge_commit"},
		"short sha":      {"ws/repo#1", "ab37", "merge_commit"},
		"github method":  {"ws/repo#1", "ab37a6993eb1", "merge"},
		"unknown method": {"ws/repo#1", "ab37a6993eb1", "ff"},
		"not hex":        {"ws/repo#1", "zzzzzzzzzzzz", "merge_commit"},
		"empty strategy": {"ws/repo#1", "ab37a6993eb1", ""},
	} {
		w := httptest.NewRecorder()
		handleBitbucketMerge(w, c.key, c.sha, c.method, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}

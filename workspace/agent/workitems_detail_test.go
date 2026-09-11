package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/log/80 §80.24 — the live pull request read. What is pinned here is the folding: four
// provider shapes (reviews, check runs, Bitbucket participants, Bitbucket statuses) each
// collapse into one line, and every one of those lines is a claim somebody acts on ("approved",
// "green", "no conflicts"). The HTTP plumbing around them is the same as the list adapter's.

func TestParseGitHubPullRequest(t *testing.T) {
	body := []byte(`{"number":518,"title":"埋め込みシェルを出す","state":"open",
	  "html_url":"https://github.com/acme/web/pull/518","updated_at":"2026-09-11T01:00:00Z",
	  "draft":false,"merged":false,"mergeable":null,
	  "additions":120,"deletions":30,"changed_files":7,"comments":2,"review_comments":3,
	  "user":{"login":"taro"},"assignees":[],"labels":[{"name":"infra"}],
	  "requested_reviewers":[{"login":"hanako"}],
	  "base":{"ref":"develop","repo":{"full_name":"acme/web"}},
	  "head":{"ref":"feature/x","sha":"deadbeef"}}`)
	out, head, err := parseGitHubPullRequest(body, "acme/web#518")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if head != "deadbeef" {
		t.Errorf("head sha = %q — the check runs hang off it", head)
	}
	if out.Repo != "acme/web" || out.BaseBranch != "develop" || out.HeadBranch != "feature/x" {
		t.Errorf("repo/branches = %q %q %q", out.Repo, out.BaseBranch, out.HeadBranch)
	}
	if out.Author != "taro" || out.State != "open" {
		t.Errorf("author/state = %q/%q", out.Author, out.State)
	}
	// null means "GitHub is still computing it". Rendering that as mergeable is how a reviewer
	// is sent to a branch that does not apply.
	if out.Mergeable != "unknown" {
		t.Errorf("mergeable = %q, want unknown for null", out.Mergeable)
	}
	// Both counters, or a PR discussed entirely in line comments reads as untouched.
	if out.Comments != 5 {
		t.Errorf("comments = %d, want 2+3", out.Comments)
	}
	if out.Additions != 120 || out.Deletions != 30 || out.ChangedFiles != 7 {
		t.Errorf("diffstat = +%d -%d in %d", out.Additions, out.Deletions, out.ChangedFiles)
	}
	// A requested reviewer who has not answered is the answer to "who is this waiting on".
	if len(out.Reviews) != 1 || out.Reviews[0].Name != "hanako" || out.Reviews[0].State != "pending" {
		t.Errorf("requested reviewers = %+v", out.Reviews)
	}
	// Empty arrays, never null: the Console iterates these and a null blanked the whole app once
	// (docs/log/80 §80.17.5). Checked on the JSON of a PR that has neither, which is what
	// actually crosses the boundary.
	bare, _, err := parseGitHubPullRequest([]byte(`{"number":1}`), "acme/web#1")
	if err != nil {
		t.Fatalf("parse bare: %v", err)
	}
	bareJSON, _ := json.Marshal(bare)
	for _, want := range []string{`"labels":[]`, `"reviews":[]`} {
		if !strings.Contains(string(bareJSON), want) {
			t.Errorf("JSON has no %s: %s", want, bareJSON)
		}
	}
	// The body promise (ADR 0061 decision 2): no description travels, so none can be stored.
	enc, _ := json.Marshal(out)
	for _, forbidden := range []string{`"body"`, `"description"`, `"diff"`} {
		if strings.Contains(string(enc), forbidden) {
			t.Errorf("detail JSON leaks %s: %s", forbidden, enc)
		}
	}
}

func TestParseGitHubPullRequestMergeableStates(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"mergeable":true}`, "clean"},
		{`{"mergeable":false}`, "conflict"},
		{`{}`, "unknown"},
	} {
		out, _, err := parseGitHubPullRequest([]byte(tc.in), "acme/web#1")
		if err != nil {
			t.Fatalf("parse %s: %v", tc.in, err)
		}
		if out.Mergeable != tc.want {
			t.Errorf("%s → %q, want %q", tc.in, out.Mergeable, tc.want)
		}
	}
}

// Only the last decision per reviewer counts, and only decisions count.
func TestParseGitHubReviews(t *testing.T) {
	body := []byte(`[
	  {"state":"CHANGES_REQUESTED","user":{"login":"taro"}},
	  {"state":"COMMENTED","user":{"login":"jiro"}},
	  {"state":"APPROVED","user":{"login":"taro"}},
	  {"state":"APPROVED","user":{"login":"saburo"}},
	  {"state":"DISMISSED","user":{"login":"saburo"}}
	]`)
	got := parseGitHubReviews(body)
	if len(got) != 1 || got[0].Name != "taro" || got[0].State != "approved" {
		// taro re-reviewed (changes → approved), jiro only commented, saburo's approval was
		// dismissed. Any other answer misstates who is happy with this PR.
		t.Fatalf("reviews = %+v, want taro approved only", got)
	}
}

// A re-requested reviewer is pending again even though an old review of theirs is on record.
func TestMergeGitHubReviewsPendingWins(t *testing.T) {
	decided := []workItemReviewOut{{Name: "taro", State: "approved"}, {Name: "jiro", State: "changes_requested"}}
	pending := []workItemReviewOut{{Name: "taro", State: "pending"}}
	got := mergeGitHubReviews(decided, pending)
	if len(got) != 2 {
		t.Fatalf("want one line per person, got %+v", got)
	}
	if got[0].Name != "jiro" || got[1].Name != "taro" || got[1].State != "pending" {
		t.Errorf("merged = %+v, want jiro's decision then taro pending", got)
	}
}

func TestParseGitHubCheckRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want workItemChecksOut
	}{
		{"no checks is not green", `{"check_runs":[]}`, workItemChecksOut{}},
		{"all green", `{"check_runs":[{"status":"completed","conclusion":"success"},
		  {"status":"completed","conclusion":"skipped"}]}`,
			workItemChecksOut{State: "success", Total: 2}},
		{"one red beats thirty green", `{"check_runs":[{"status":"completed","conclusion":"success"},
		  {"status":"completed","conclusion":"failure"},{"status":"in_progress"}]}`,
			workItemChecksOut{State: "failure", Total: 3, Failed: 1, Pending: 1}},
		{"still running", `{"check_runs":[{"status":"queued"},{"status":"completed","conclusion":"neutral"}]}`,
			workItemChecksOut{State: "pending", Total: 2, Pending: 1}},
	} {
		if got := parseGitHubCheckRuns([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestParseBitbucketPullRequest(t *testing.T) {
	body := []byte(`{"id":42,"title":"fix login","state":"OPEN","draft":false,
	  "updated_on":"2026-09-10T07:10:55.049604+00:00","comment_count":4,
	  "author":{"display_name":"Taro","nickname":"taro"},
	  "source":{"branch":{"name":"bugfix/login"}},
	  "destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/web"}},
	  "links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/42"}},
	  "participants":[
	    {"role":"REVIEWER","approved":true,"state":"approved","user":{"display_name":"Hanako"}},
	    {"role":"REVIEWER","approved":false,"state":"changes_requested","user":{"display_name":"Jiro"}},
	    {"role":"REVIEWER","approved":false,"state":null,"user":{"display_name":"Saburo"}},
	    {"role":"PARTICIPANT","approved":false,"user":{"display_name":"Bystander"}}]}`)
	out, err := parseBitbucketPullRequest(body, "acme/web#42")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.BaseBranch != "main" || out.HeadBranch != "bugfix/login" {
		t.Errorf("branches = %q ← %q", out.BaseBranch, out.HeadBranch)
	}
	// Offsets sort wrong against the GitHub rows, so the stamp is normalised here as in the list.
	if out.UpdatedAt != "2026-09-10T07:10:55Z" {
		t.Errorf("updatedAt = %q, want UTC RFC3339", out.UpdatedAt)
	}
	// Bitbucket cannot answer this without starting a merge task, and a guess would be a lie.
	if out.Mergeable != "unknown" {
		t.Errorf("mergeable = %q", out.Mergeable)
	}
	// Reviewers only. A participant list also holds everyone who commented, and counting those
	// as reviewers turns "4 reviewers" into a number nobody can act on.
	if len(out.Reviews) != 3 {
		t.Fatalf("reviews = %+v, want the three REVIEWERs", out.Reviews)
	}
	if out.Reviews[0].State != "approved" || out.Reviews[1].State != "changes_requested" || out.Reviews[2].State != "pending" {
		t.Errorf("review states = %+v", out.Reviews)
	}
}

func TestParseBitbucketStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want workItemChecksOut
	}{
		{"none reported", `{"values":[]}`, workItemChecksOut{}},
		{"green", `{"values":[{"state":"SUCCESSFUL"}]}`, workItemChecksOut{State: "success", Total: 1}},
		// A cancelled build never finished, so calling it a pass would claim a green PR.
		{"stopped counts as failing", `{"values":[{"state":"SUCCESSFUL"},{"state":"STOPPED"}]}`,
			workItemChecksOut{State: "failure", Total: 2, Failed: 1}},
		{"running", `{"values":[{"state":"INPROGRESS"}]}`, workItemChecksOut{State: "pending", Total: 1, Pending: 1}},
	} {
		if got := parseBitbucketStatuses([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// `size` is the whole diff's file count; len(values) is only the page that was asked for.
func TestParseBitbucketDiffstat(t *testing.T) {
	files, added, removed := parseBitbucketDiffstat([]byte(
		`{"size":9,"values":[{"lines_added":10,"lines_removed":2},{"lines_added":1,"lines_removed":0}]}`))
	if files != 9 || added != 11 || removed != 2 {
		t.Errorf("diffstat = %d files +%d -%d, want 9 +11 -2", files, added, removed)
	}
	if files, _, _ := parseBitbucketDiffstat([]byte(`{"values":[{"lines_added":1}]}`)); files != 1 {
		t.Errorf("missing size should fall back to the page length, got %d", files)
	}
}

// A Jira row has no pull request behind it, and the answer has to say so rather than 500.
func TestWorkItemsDetailRejectsProvidersWithoutPullRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct{ body, want string }{
		{`{"provider":"jira","key":"G3-1"}`, "GitHub and Bitbucket"},
		{`{"provider":"github","key":"  "}`, "key is required"},
	} {
		req := httptest.NewRequest("POST", "/work-items/detail", strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		handleWorkItemsDetail(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s → status %d, want 400", tc.body, w.Code)
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s → %s, want it to mention %q", tc.body, w.Body.String(), tc.want)
		}
	}
}

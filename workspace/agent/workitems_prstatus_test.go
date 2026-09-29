package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// #1113 — CI and conflict status on the rail's pull request rows.

func TestParseGitHubPRStatuses(t *testing.T) {
	body := []byte(`{"data":{"nodes":[
	  {"id":"PR_red","mergeable":"CONFLICTING","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":[{"state":"SUCCESS","count":37},{"state":"FAILURE","count":2},{"state":"IN_PROGRESS","count":1}],
	    "statusContextCountsByState":[{"state":"ERROR","count":1}]}}}}]}},
	  {"id":"PR_wait","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":[{"state":"SUCCESS","count":3},{"state":"QUEUED","count":1}],
	    "statusContextCountsByState":[{"state":"EXPECTED","count":1}]}}}}]}},
	  {"id":"PR_green","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":[{"state":"SUCCESS","count":6},{"state":"SKIPPED","count":1},{"state":"FAILURE","count":0}],
	    "statusContextCountsByState":[{"state":"SUCCESS","count":0}]}}}}]}},
	  {"id":"PR_none","mergeable":"UNKNOWN","commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}},
	  null
	]},"errors":[{"type":"NOT_FOUND","path":["nodes",4]}]}`)
	got := parseGitHubPRStatuses(body)
	if len(got) != 4 {
		t.Fatalf("want 4 statuses (the null node skipped, the errors array ignored), got %d: %+v", len(got), got)
	}
	cases := []struct {
		id        string
		checks    workItemChecksOut
		mergeable string
	}{
		// Check runs and commit statuses are one count: the status ERROR is a failure too.
		{"PR_red", workItemChecksOut{State: "failure", Total: 41, Failed: 3, Pending: 1}, "conflict"},
		// EXPECTED is a required status nothing has reported yet — waiting, not passed.
		{"PR_wait", workItemChecksOut{State: "pending", Total: 5, Pending: 2}, "clean"},
		{"PR_green", workItemChecksOut{State: "success", Total: 7}, "clean"},
		// No rollup = nothing ran: State stays "", never "success". UNKNOWN never reads as clean.
		{"PR_none", workItemChecksOut{}, "unknown"},
	}
	for _, c := range cases {
		st, ok := got[c.id]
		if !ok {
			t.Errorf("%s missing", c.id)
			continue
		}
		if st.checks != c.checks || st.mergeable != c.mergeable {
			t.Errorf("%s = %+v / %q, want %+v / %q", c.id, st.checks, st.mergeable, c.checks, c.mergeable)
		}
	}
	// A partial answer: one count array failed (null plus an error pointing into the node) while
	// the other arrived green. Summing what arrived would show a PR with failing checks as passing.
	partial := []byte(`{"data":{"nodes":[
	  {"id":"PR_part","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":null,"statusContextCountsByState":[{"state":"SUCCESS","count":1}]}}}}]}},
	  {"id":"PR_ok","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":[{"state":"SUCCESS","count":2}],"statusContextCountsByState":[]}}}}]}}
	]},"errors":[{"path":["nodes",0,"commits","nodes",0,"commit","statusCheckRollup","contexts","checkRunCountsByState"]}]}`)
	pg := parseGitHubPRStatuses(partial)
	if _, ok := pg["PR_part"]; ok {
		t.Errorf("a node an error points into must be left unread, got %+v", pg["PR_part"])
	}
	if pg["PR_ok"].checks.State != "success" {
		t.Errorf("the other node in the same answer = %+v, want success", pg["PR_ok"].checks)
	}
	// The same null without an error: the checks are unread, the mergeability still stands.
	nullOnly := []byte(`{"data":{"nodes":[{"id":"PR_n","mergeable":"CONFLICTING","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
	    "checkRunCountsByState":null,"statusContextCountsByState":[{"state":"SUCCESS","count":1}]}}}}]}}]}}`)
	if st := parseGitHubPRStatuses(nullOnly)["PR_n"]; st.checks != (workItemChecksOut{}) || st.mergeable != "conflict" {
		t.Errorf("null counts = %+v / %q, want unread checks and conflict", st.checks, st.mergeable)
	}
	if len(parseGitHubPRStatuses([]byte(`not json`))) != 0 {
		t.Error("a body that does not parse must yield nothing")
	}
}

// stubGitHub answers /search/issues with search and /graphql with graphql (status 200 unless
// graphqlStatus is set), recording every request.
type stubGitHub struct {
	search, graphql string
	graphqlStatus   int
	paths           []string
	graphqlBodies   []string
}

func (s *stubGitHub) install(t *testing.T) {
	t.Helper()
	orig := workItemHTTPClient.Transport
	workItemHTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.paths = append(s.paths, r.URL.Path)
		body, code := s.search, http.StatusOK
		if r.URL.Path == "/graphql" {
			b, _ := io.ReadAll(r.Body)
			s.graphqlBodies = append(s.graphqlBodies, string(b))
			body = s.graphql
			if s.graphqlStatus != 0 {
				code = s.graphqlStatus
			}
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { workItemHTTPClient.Transport = orig })
}

const stubSearchMixed = `{"total_count":4,"items":[
  {"node_id":"PR_open","number":1,"title":"open pr","state":"open","repository_url":"https://api.github.com/repos/acme/web","pull_request":{"url":"x"}},
  {"node_id":"PR_draft","number":2,"title":"draft pr","state":"open","draft":true,"repository_url":"https://api.github.com/repos/acme/web","pull_request":{"url":"x"}},
  {"node_id":"PR_merged","number":3,"title":"merged pr","state":"closed","repository_url":"https://api.github.com/repos/acme/web","pull_request":{"url":"x"}},
  {"node_id":"I_issue","number":4,"title":"an issue","state":"open","repository_url":"https://api.github.com/repos/acme/web"}
]}`

// The acceptance line of #1113: a refresh costs ONE extra call for the whole page, never one per
// row, and asks only about the open pull requests.
func TestGitHubSearchEnrichesOpenPullRequestsInOneCall(t *testing.T) {
	s := &stubGitHub{search: stubSearchMixed, graphql: `{"data":{"nodes":[
	  {"id":"PR_open","mergeable":"CONFLICTING","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"checkRunCountsByState":[{"state":"FAILURE","count":1}],"statusContextCountsByState":[]}}}}]}},
	  {"id":"PR_draft","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}
	]}}`}
	s.install(t)

	rows, _, err := githubSearchWorkItems("tok", "q1", "is:open")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Join(s.paths, " ") != "/search/issues /graphql" {
		t.Fatalf("requests = %v, want exactly search then one graphql", s.paths)
	}
	var sent struct {
		Variables struct {
			IDs []string `json:"ids"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(s.graphqlBodies[0]), &sent); err != nil {
		t.Fatalf("graphql body: %v", err)
	}
	if strings.Join(sent.Variables.IDs, ",") != "PR_open,PR_draft" {
		t.Errorf("asked about %v, want only the open and draft PRs", sent.Variables.IDs)
	}
	if rows[0].Checks.State != "failure" || rows[0].Mergeable != "conflict" {
		t.Errorf("open PR = %+v / %q", rows[0].Checks, rows[0].Mergeable)
	}
	if rows[1].Checks.State != "" || rows[1].Mergeable != "clean" {
		t.Errorf("draft PR = %+v / %q", rows[1].Checks, rows[1].Mergeable)
	}
	for _, r := range rows[2:] {
		if r.Checks != (workItemChecksOut{}) || r.Mergeable != "" {
			t.Errorf("%s was enriched: %+v / %q", r.Key, r.Checks, r.Mergeable)
		}
	}
	// The node ID is the lookup key only; it does not reach the wire.
	if enc, _ := json.Marshal(rows[0]); strings.Contains(string(enc), "PR_open") {
		t.Errorf("row JSON carries the node ID: %s", enc)
	}
}

// Best effort: GraphQL failing must not cost the list.
func TestGitHubSearchKeepsRowsWhenEnrichmentFails(t *testing.T) {
	s := &stubGitHub{search: stubSearchMixed, graphql: `{"message":"Bad credentials"}`, graphqlStatus: http.StatusBadGateway}
	s.install(t)

	rows, total, err := githubSearchWorkItems("tok", "q1", "is:open")
	if err != nil {
		t.Fatalf("search failed because enrichment did: %v", err)
	}
	if len(rows) != 4 || total != 4 {
		t.Fatalf("rows=%d total=%d, want 4 and 4", len(rows), total)
	}
	if rows[0].Checks != (workItemChecksOut{}) || rows[0].Mergeable != "" {
		t.Errorf("open PR = %+v / %q, want untouched", rows[0].Checks, rows[0].Mergeable)
	}
}

// A page with no open pull request makes no GraphQL call at all.
func TestGitHubSearchSkipsEnrichmentWithoutOpenPullRequests(t *testing.T) {
	s := &stubGitHub{search: `{"items":[{"node_id":"I_1","number":1,"title":"x","state":"open"}]}`}
	s.install(t)
	if _, _, err := githubSearchWorkItems("tok", "q1", "is:issue"); err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Join(s.paths, " ") != "/search/issues" {
		t.Errorf("requests = %v, want the search only", s.paths)
	}
}

func TestGitHubMergeableState(t *testing.T) {
	for in, want := range map[string]string{"MERGEABLE": "clean", "CONFLICTING": "conflict", "UNKNOWN": "unknown", "": "unknown"} {
		if got := githubMergeableState(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

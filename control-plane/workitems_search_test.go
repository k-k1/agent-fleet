package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestNarrowWorkItemQuery(t *testing.T) {
	for _, tc := range []struct {
		provider, query, needle string
		want                    string
		ok                      bool
	}{
		{"github", "is:open involves:@me", "#1028", "is:open involves:@me 1028", true},
		{"", "is:open involves:@me", "ssm login", "is:open involves:@me ssm login", true},
		// Without the parentheses the number would bind to review-requested only.
		{"github", "is:open assignee:@me OR review-requested:@me", "1028",
			"(is:open assignee:@me OR review-requested:@me) 1028", true},
		{"github", "is:open", "#", "", false},
		{"jira", "assignee = currentUser()", "login modal",
			`(assignee = currentUser()) AND text ~ "login modal"`, true},
		{"jira", "project = WEB ORDER BY priority DESC", "web-12",
			`(project = WEB) AND key = "WEB-12" ORDER BY priority DESC`, true},
		{"jira", "", `say "hi"`, `text ~ "say \"hi\""`, true},
		{"bitbucket", "acme/web", "x", "", false},
	} {
		got, ok := narrowWorkItemQuery(tc.provider, tc.query, tc.needle)
		if got != tc.want || ok != tc.ok {
			t.Errorf("narrow(%q, %q, %q) = %q, %v; want %q, %v", tc.provider, tc.query, tc.needle, got, ok, tc.want, tc.ok)
		}
	}
}

func searchReq(env *workItemEnv, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	env.api.search(w, httptest.NewRequest("POST", "/api/work-items/search", strings.NewReader(body)), env.res)
	return w
}

// A stopped Workspace is never started to search, and nothing reaches the Agent.
func TestWorkItemSearchStoppedDoesNotStart(t *testing.T) {
	env := newWorkItemEnv(t, "stopped")
	env.addQuery(t, "q1", "mine", "is:open involves:@me", true)
	if w := searchReq(env, `{"q":"1028"}`); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if *env.hits != 0 {
		t.Errorf("reached the agent %d times while stopped", *env.hits)
	}
}

func TestWorkItemSearchNeedsQ(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	for _, body := range []string{`{"q":"  "}`, `{"q":"` + strings.Repeat("x", 201) + `"}`} {
		if w := searchReq(env, body); w.Code != http.StatusBadRequest {
			t.Errorf("%.20s…: status = %d, want 400", body, w.Code)
		}
	}
}

// The search narrows each enabled saved query, answers one row per ticket, names the queries it
// could not narrow, and writes nothing into the rail's cache.
func TestWorkItemSearchNarrowsAndDoesNotCache(t *testing.T) {
	env := newWorkItemEnv(t, "running")
	ctx := context.Background()
	env.addQuery(t, "a", "mine", "is:open involves:@me", true)
	env.addQuery(t, "b", "reviews", "is:open review-requested:@me", true)
	env.addQuery(t, "off", "off", "is:closed", false)
	if err := env.st.CreateWorkItemQuery(ctx, store.WorkItemQuery{ID: "bb", MembershipID: env.mid,
		Provider: "bitbucket", Label: "bb", Query: "acme/web", Enabled: true, CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	row := `{"queryId":"%s","provider":"github","kind":"issue","key":"k-k1/agent-fleet#1028","title":"SSM login","state":"open","labels":[],"labelColors":{}}`
	env.body = func() string {
		return `{"items":[` + strings.Replace(row, "%s", "a", 1) + `,` + strings.Replace(row, "%s", "b", 1) + `],"errors":[]}`
	}
	w := searchReq(env, `{"q":"#1028"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var sent agentWorkItemsReq
	if err := json.Unmarshal([]byte(*env.sent), &sent); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, q := range sent.Queries {
		got[q.ID] = q.Query
	}
	want := map[string]string{"a": "is:open involves:@me 1028", "b": "is:open review-requested:@me 1028"}
	if len(got) != len(want) || got["a"] != want["a"] || got["b"] != want["b"] {
		t.Errorf("agent got %v, want %v", got, want)
	}
	var out workItemSearchWire
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].Key != "k-k1/agent-fleet#1028" {
		t.Errorf("items = %+v, want the one ticket once", out.Items)
	}
	if len(out.Skipped) != 1 || out.Skipped[0] != "bb" {
		t.Errorf("skipped = %v, want [bb]", out.Skipped)
	}
	if items, _ := env.st.ListWorkItems(ctx, env.mid); len(items) != 0 {
		t.Errorf("the search wrote %d rows into the cache", len(items))
	}
}

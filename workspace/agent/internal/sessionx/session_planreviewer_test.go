package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func planReviewerServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/plan-reviewer", HandleSetPlanReviewer)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postPlanReviewer(t *testing.T, srv *httptest.Server, name, body string) int {
	t.Helper()
	resp, err := http.Post(srv.URL+"/sessions/"+name+"/plan-reviewer", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPlanReviewerLinkPersistsOnTheMetaAndRidesTheList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude})
	srv := planReviewerServer(t)

	if c := postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","reviewer":"srev01"}`); c != 200 {
		t.Fatalf("link: status %d", c)
	}
	postPlanReviewer(t, srv, "planner", `{"path":"/p/b.md","reviewer":"srev02"}`)
	// A later launch for the same plan replaces the earlier reviewer.
	postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","reviewer":"srev03"}`)

	m, _ := session.ReadMeta("planner")
	want := []session.PlanReviewer{{Path: "/p/b.md", Reviewer: "srev02"}, {Path: "/p/a.md", Reviewer: "srev03"}}
	if fmt.Sprint(m.PlanReviewers) != fmt.Sprint(want) {
		t.Fatalf("meta = %v, want %v", m.PlanReviewers, want)
	}
	if got := wireSession(m, false).PlanReviewers; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list row = %v, want %v", got, want)
	}

	if c := postPlanReviewer(t, srv, "planner", `{"path":"/p/a.md","reviewer":""}`); c != 200 {
		t.Fatalf("unlink: status %d", c)
	}
	m, _ = session.ReadMeta("planner")
	if len(m.PlanReviewers) != 1 || m.PlanReviewers[0].Path != "/p/b.md" {
		t.Fatalf("after unlink = %v", m.PlanReviewers)
	}
}

func TestPlanReviewerRefusesBadInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude})
	srv := planReviewerServer(t)
	for name, c := range map[string]struct {
		session, body string
		want          int
	}{
		"no path":           {"planner", `{"path":"","reviewer":"srev01"}`, 400},
		"control char path": {"planner", `{"path":"/p/\u0000a.md","reviewer":"srev01"}`, 400},
		"long path":         {"planner", `{"path":"/` + strings.Repeat("a", 2000) + `","reviewer":"srev01"}`, 400},
		"self review":       {"planner", `{"path":"/p/a.md","reviewer":"planner"}`, 400},
		"bad reviewer name": {"planner", `{"path":"/p/a.md","reviewer":"../x"}`, 400},
		"unknown planner":   {"nosuch", `{"path":"/p/a.md","reviewer":"srev01"}`, 404},
		"bad json":          {"planner", `{`, 400},
	} {
		if got := postPlanReviewer(t, srv, c.session, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", name, got, c.want)
		}
	}
	if m, _ := session.ReadMeta("planner"); len(m.PlanReviewers) != 0 {
		t.Errorf("a refused request stored %v", m.PlanReviewers)
	}
}

func TestPlanReviewerIsBoundedOldestFirst(t *testing.T) {
	var list []session.PlanReviewer
	for i := 0; i < session.MaxPlanReviewers+5; i++ {
		list = withPlanReviewer(list, fmt.Sprintf("/p/%d.md", i), "srev01")
	}
	if len(list) != session.MaxPlanReviewers {
		t.Fatalf("len = %d, want %d", len(list), session.MaxPlanReviewers)
	}
	if list[0].Path != "/p/5.md" || list[len(list)-1].Path != fmt.Sprintf("/p/%d.md", session.MaxPlanReviewers+4) {
		t.Fatalf("kept %v ... %v", list[0], list[len(list)-1])
	}
}

// An Agent that knows the field always emits it, [] included: the Console reads the field's
// presence as "an absent link means unlinked", not "an older Agent".
func TestPlanReviewersAlwaysOnTheListRow(t *testing.T) {
	row := wireSession(session.Meta{Name: "planner", Dir: t.TempDir(), Kind: session.KindClaude}, false)
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"planReviewers":[]`) {
		t.Fatalf("row without links must carry an empty list: %s", b)
	}
}

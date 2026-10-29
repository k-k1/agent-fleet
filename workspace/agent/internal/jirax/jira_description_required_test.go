package jirax

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// createmetaStub serves the paged createmeta endpoints: two pages of issue types (Cloud's
// "issueTypes") and a field list (Data Center's "values") whose description is on page two.
func createmetaStub(t *testing.T, descRequired string) (*secrets.JiraCreds, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		start := r.URL.Query().Get("startAt")
		switch {
		case strings.HasSuffix(r.URL.Path, "/createmeta/AB/issuetypes") && start == "0":
			io.WriteString(w, `{"startAt":0,"total":2,"issueTypes":[{"id":"1","name":"Bug"}]}`)
		case strings.HasSuffix(r.URL.Path, "/createmeta/AB/issuetypes"):
			io.WriteString(w, `{"startAt":1,"total":2,"issueTypes":[{"id":"10002","name":"タスク","untranslatedName":"Task"}]}`)
		case strings.HasSuffix(r.URL.Path, "/createmeta/AB/issuetypes/10002") && start == "0":
			io.WriteString(w, `{"startAt":0,"isLast":false,"values":[{"fieldId":"summary","required":true}]}`)
		case strings.HasSuffix(r.URL.Path, "/createmeta/AB/issuetypes/10002"):
			io.WriteString(w, `{"startAt":1,"isLast":true,"values":[{"fieldId":"description","required":`+descRequired+`}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}, &paths
}

func TestJiraDescriptionRequired(t *testing.T) {
	for _, tc := range []struct {
		flag, typ string
		want      bool
	}{{"true", "task", true}, {"true", "タスク", true}, {"false", "Task", false}} {
		c, paths := createmetaStub(t, tc.flag)
		got, err := jiraDescriptionRequired(c, "AB", tc.typ)
		if err != nil || got != tc.want {
			t.Errorf("%s/%s: got %v err %v, want %v (calls %v)", tc.flag, tc.typ, got, err, tc.want, *paths)
		}
	}
	c, _ := createmetaStub(t, "true")
	if _, err := jiraDescriptionRequired(c, "AB", "Epic"); err == nil {
		t.Errorf("unknown issue type: no error")
	}
	c, _ = createmetaStub(t, "true")
	if _, err := jiraDescriptionRequired(c, "ZZ", "Task"); err == nil {
		t.Errorf("unknown project (404): no error")
	}
}

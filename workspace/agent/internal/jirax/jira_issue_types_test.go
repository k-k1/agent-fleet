package jirax

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

const createmetaBody = `{"projects":[{"key":"KEY","issuetypes":[
 {"id":"10001","name":"Task","untranslatedName":"Task","subtask":false},
 {"id":"10002","name":"サブタスク","untranslatedName":"Sub-task","subtask":true},
 {"id":"10003","name":"Bug","subtask":false}]}]}`

func TestJiraIssueTypesYAMLShape(t *testing.T) {
	got, err := jiraIssueTypesYAML([]byte(createmetaBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"issue:\n  types:\n",
		"    - id: \"10001\"\n      name: \"Task\"\n      handle: \"Task\"\n      subtask: false\n",
		"      name: \"サブタスク\"\n      handle: \"Sub-task\"\n      subtask: true\n",
		"    - id: \"10003\"\n      name: \"Bug\"\n      handle: \"Bug\"\n", // handle falls back to the name
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestJiraIssueTypesYAMLRejectsAnEmptyAnswer(t *testing.T) {
	for _, body := range []string{`{"projects":[]}`, `{"projects":[{"issuetypes":[]}]}`, `not json`, `{"projects":[{"issuetypes":[{"id":"","name":""}]}]}`} {
		if out, err := jiraIssueTypesYAML([]byte(body)); err == nil {
			t.Errorf("%q: got %q, want an error", body, out)
		}
	}
}

// The classic createmeta endpoint on the OAuth route, with the bearer token, and nothing
// secret on stdout.
func TestJiraIssueTypesGetCallsCreatemetaOnTheOAuthRoute(t *testing.T) {
	var path, query, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query, auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(createmetaBody))
	}))
	defer srv.Close()
	old := CloudAPIBase
	CloudAPIBase = srv.URL
	defer func() { CloudAPIBase = old }()
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix()}
	})
	var out bytes.Buffer
	if err := jiraIssueTypesGet(&out, "KEY"); err != nil {
		t.Fatal(err)
	}
	if path != "/ex/jira/cid-1/rest/api/2/issue/createmeta" || query != "projectKeys=KEY" {
		t.Errorf("requested %s?%s", path, query)
	}
	if auth != "Bearer fake-at" {
		t.Errorf("Authorization = %q", auth)
	}
	if strings.Contains(out.String(), "fake-at") || !strings.Contains(out.String(), `id: "10001"`) {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestJiraIssueTypesGetFailsClosed(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer srv.Close()
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}
	})
	for name, tc := range map[string]struct {
		key    string
		status int
	}{
		"bad key":       {"KEY;rm", 200},
		"key with path": {"../x", 200},
		"empty list":    {"KEY", 200},
		"forbidden":     {"KEY", 403},
		"server error":  {"KEY", 500},
	} {
		t.Run(name, func(t *testing.T) {
			status = tc.status
			var out bytes.Buffer
			if err := jiraIssueTypesGet(&out, tc.key); err == nil || out.Len() != 0 {
				t.Fatalf("err = %v, stdout = %q", err, out.String())
			}
		})
	}
	t.Run("no connection", func(t *testing.T) {
		seedJiraStore(t, func(s *secrets.Data) {})
		if err := jiraIssueTypesGet(&bytes.Buffer{}, "KEY"); err == nil {
			t.Fatal("want an error")
		}
	})
}

// API-token connections go to the site itself with Basic.
func TestJiraIssueTypesGetUsesBasicOnTheSite(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(createmetaBody))
	}))
	defer srv.Close()
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}
	})
	if err := jiraIssueTypesGet(&bytes.Buffer{}, "KEY"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("Authorization = %q", auth)
	}
}

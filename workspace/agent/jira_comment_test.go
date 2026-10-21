package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

type commentCall struct {
	method, path, auth string
	body               map[string]any
}

// commentStub answers every call with status and records it.
func commentStub(t *testing.T, status int, reply string) (*secrets.JiraCreds, *[]commentCall) {
	t.Helper()
	var calls []commentCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		cc := commentCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cc.body)
		}
		calls = append(calls, cc)
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}, &calls
}

func runComment(c *secrets.JiraCreds, stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	rc := jiraCommentRun(c, args, in, &out, &errb)
	return rc, out.String(), errb.String()
}

func TestJiraCommentEditSendsADF(t *testing.T) {
	c, calls := commentStub(t, 200, `{"id":"10001"}`)
	rc, out, errs := runComment(c, "", "issue", "comment", "edit", "-p", "T", "T-1", "10001", "-b", "Intro.\n\n- a\n- b")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	if len(*calls) != 2 || (*calls)[0].method != "GET" || (*calls)[1].method != "PUT" || (*calls)[1].path != "/rest/api/3/issue/T-1/comment/10001" {
		t.Fatalf("calls = %+v", *calls)
	}
	if !strings.HasPrefix((*calls)[1].auth, "Basic ") {
		t.Errorf("auth header %q", (*calls)[1].auth)
	}
	body := (*calls)[1].body["body"].(map[string]any)
	if body["type"] != "doc" || body["version"] != float64(1) {
		t.Errorf("not an ADF doc: %v", body)
	}
	if strings.Contains(out+errs, "fake-api-tok") {
		t.Errorf("token in output")
	}
}

// The #2476 case: a paragraph followed by a list must stay two blocks.
func TestJiraMarkdownADFParagraphThenList(t *testing.T) {
	doc := jiraMarkdownADF("Intro.\n\n- a\n- b\n")
	got, _ := json.Marshal(doc["content"])
	want := `[{"content":[{"text":"Intro.","type":"text"}],"type":"paragraph"},` +
		`{"content":[{"content":[{"content":[{"text":"a","type":"text"}],"type":"paragraph"}],"type":"listItem"},` +
		`{"content":[{"content":[{"text":"b","type":"text"}],"type":"paragraph"}],"type":"listItem"}],"type":"bulletList"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestJiraMarkdownADFConstructs(t *testing.T) {
	md := "# H\n\n1. one\n   - nested\n2. two\n\n> quote\n\n---\n\n```go\nx := 1\n```\n\n" +
		"**b** *i* `c` [l](http://x.y) end  \nnext\n"
	raw, _ := json.Marshal(jiraMarkdownADF(md))
	s := string(raw)
	for _, want := range []string{
		`"type":"heading"`, `"level":1`, `"type":"orderedList"`, `"type":"bulletList"`,
		`"type":"blockquote"`, `"type":"rule"`, `"type":"codeBlock"`, `"language":"go"`, `"text":"x := 1"`,
		`"type":"strong"`, `"type":"em"`, `"type":"code"`, `"href":"http://x.y"`, `"type":"hardBreak"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestJiraMarkdownADFFallbacks(t *testing.T) {
	// A table has no goldmark core node; it must come through as text, never panic or vanish.
	raw, _ := json.Marshal(jiraMarkdownADF("| a | b |\n|---|---|\n| 1 | 2 |\n"))
	if !strings.Contains(string(raw), "| a | b") || !strings.Contains(string(raw), "|---|---|") {
		t.Errorf("table text lost: %s", raw)
	}
	// Code mark next to strong: ADF refuses the pair, so only code stays.
	raw, _ = json.Marshal(jiraMarkdownADF("**`x`**"))
	if strings.Contains(string(raw), `"strong"`) {
		t.Errorf("strong kept beside code: %s", raw)
	}
	// Whitespace-only body still yields a non-empty doc.
	if c, _ := jiraMarkdownADF("  \n")["content"].([]any); len(c) == 0 {
		t.Errorf("empty doc")
	}
}

func TestJiraCommentDelete(t *testing.T) {
	c, calls := commentStub(t, 204, "")
	rc, out, errs := runComment(c, "", "issue", "comment", "delete", "T-1", "10001")
	if rc != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
	if (*calls)[0].method != "DELETE" || (*calls)[0].path != "/rest/api/3/issue/T-1/comment/10001" {
		t.Errorf("calls = %+v", *calls)
	}
}

func TestJiraCommentRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{403, "only the comment's author"},
		{404, "there is no comment 10001 on T-1"},
		{401, "re-connect Jira"},
		{400, "bad adf"},
	} {
		c, _ := commentStub(t, tc.status, `{"errorMessages":["bad adf"]}`)
		rc, _, errs := runComment(c, "", "issue", "comment", "edit", "T-1", "10001", "-b", "x")
		if rc != 1 || !strings.Contains(errs, tc.want) {
			t.Errorf("%d: rc=%d err=%q, want %q", tc.status, rc, errs, tc.want)
		}
		if strings.Count(strings.TrimSpace(errs), "\n") != 0 || strings.Contains(errs, "fake-api-tok") {
			t.Errorf("%d: not one clean line: %q", tc.status, errs)
		}
	}
}

func TestJiraCommentBodySources(t *testing.T) {
	c, calls := commentStub(t, 200, "{}")
	if rc, _, e := runComment(c, "from stdin", "issue", "comment", "edit", "T-1", "5"); rc != 0 {
		t.Fatalf("piped stdin: %s", e)
	}
	if rc, _, e := runComment(c, "from dash", "issue", "comment", "edit", "T-1", "5", "-T", "-"); rc != 0 {
		t.Fatalf("-T -: %s", e)
	}
	if len(*calls) != 4 { // a read and a write per edit
		t.Fatalf("calls = %d", len(*calls))
	}
	if rc, _, e := runComment(c, "", "issue", "comment", "edit", "T-1", "5"); rc != 2 || !strings.Contains(e, "empty") {
		t.Errorf("no body: rc=%d %q", rc, e)
	}
	if rc, _, _ := runComment(c, "", "issue", "comment", "delete", "T-1", "5", "-b", "x"); rc != 2 {
		t.Errorf("delete with a body: rc=%d", rc)
	}
}

func TestParseJiraCommentArgsRejects(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "comment", "edit", "T-1"},
		{"issue", "comment", "add", "T-1", "5"},
		{"issue", "comment", "edit", "../x", "5"},
		{"issue", "comment", "edit", "T-1", "5/../6"},
		{"issue", "comment", "edit", "T-1", "5", "-b"},
	} {
		if _, err := parseJiraCommentArgs(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if a, err := parseJiraCommentArgs([]string{"issues", "comment", "edit", "-c", "/x.yml", "T-1", "5", "--body=hi", "--no-input"}); err != nil || a.body == nil || *a.body != "hi" || a.key != "T-1" {
		t.Errorf("a=%+v err=%v", a, err)
	}
}

func TestJiraCommentNoConnection(t *testing.T) {
	if rc, _, e := runComment(nil, "", "issue", "comment", "delete", "T-1", "5"); rc != 1 || !strings.Contains(e, "no Jira connection") {
		t.Errorf("rc=%d %q", rc, e)
	}
}

// An edit hands the comment's visibility and properties back as they were.
func TestJiraCommentEditKeepsVisibility(t *testing.T) {
	c, calls := commentStub(t, 200, `{"id":"5","visibility":{"type":"role","value":"Administrators"},"properties":[{"key":"sd.public.comment","value":{"internal":true}}]}`)
	if rc, _, e := runComment(c, "", "issue", "comment", "edit", "T-1", "5", "-b", "x"); rc != 0 {
		t.Fatal(e)
	}
	put := (*calls)[1]
	if v, _ := put.body["visibility"].(map[string]any); v["value"] != "Administrators" {
		t.Errorf("visibility not kept: %v", put.body)
	}
	if p, _ := put.body["properties"].([]any); len(p) != 1 {
		t.Errorf("properties not kept: %v", put.body)
	}
	// A public comment sends neither.
	c, calls = commentStub(t, 200, `{"id":"5","visibility":null}`)
	runComment(c, "", "issue", "comment", "edit", "T-1", "5", "-b", "x")
	if _, ok := (*calls)[1].body["visibility"]; ok {
		t.Errorf("visibility invented: %v", (*calls)[1].body)
	}
}

// Escapes and character references are resolved, in text and in link targets.
func TestJiraMarkdownADFUnescapesAndExtensions(t *testing.T) {
	raw, _ := json.Marshal(jiraMarkdownADF(`a \*not em\* &amp; &copy; [l](http://x.y/a\_b) ~~s~~ https://e.example`))
	s := string(raw)
	for _, want := range []string{`a *not em* \u0026 © `, `http://x.y/a_b`, `"type":"strike"`, `"href":"https://e.example"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `\\`) || strings.Contains(s, "&amp;") {
		t.Errorf("raw escape left: %s", s)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJiraDescriptionSetsADF(t *testing.T) {
	c, calls := commentStub(t, 204, ``)
	var errb bytes.Buffer
	rc := jiraDescriptionRun(c, []string{"AB-12"}, strings.NewReader("- **すべて**。次へ\n\n段落\n- a\n"), &errb)
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errb.String())
	}
	if len(*calls) != 1 || (*calls)[0].method != "PUT" || (*calls)[0].path != "/rest/api/3/issue/AB-12" {
		t.Fatalf("calls = %+v", *calls)
	}
	doc := (*calls)[0].body["fields"].(map[string]any)["description"].(map[string]any)
	got, _ := json.Marshal(doc["content"])
	for _, want := range []string{
		`{"marks":[{"type":"strong"}],"text":"すべて","type":"text"},{"text":"。次へ","type":"text"}`,
		`{"content":[{"text":"段落","type":"text"}],"type":"paragraph"},{"content":[{"content":[{"content":[{"text":"a"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("description %s\nlacks %s", got, want)
		}
	}
}

func TestJiraDescriptionArgs(t *testing.T) {
	var errb bytes.Buffer
	if rc := jiraDescriptionRun(nil, []string{"AB-1", "--skip-notify"}, strings.NewReader("x"), &errb); rc != 1 ||
		!strings.Contains(errb.String(), "no Jira connection") {
		t.Fatalf("--skip-notify is not accepted: rc=%d err=%q", rc, errb.String())
	}
	for _, tc := range []struct {
		args  []string
		stdin string
	}{
		{nil, "x"},
		{[]string{"1"}, "x"},
		{[]string{"AB-1", "AB-2"}, "x"},
		{[]string{"AB-1"}, " \n"},
	} {
		c, calls := commentStub(t, 204, ``)
		if rc := jiraDescriptionRun(c, tc.args, strings.NewReader(tc.stdin), &errb); rc != 2 || len(*calls) != 0 {
			t.Errorf("%v %q: rc=%d calls=%d, want 2 and none", tc.args, tc.stdin, rc, len(*calls))
		}
	}
	errb.Reset()
	c, _ := commentStub(t, 400, `{"errors":{"description":"Operation value must be an Atlassian Document"}}`)
	if rc := jiraDescriptionRun(c, []string{"AB-1"}, strings.NewReader("x"), &errb); rc != 1 || !strings.Contains(errb.String(), "AB-1 (400)") {
		t.Errorf("400: rc=%d err=%q", rc, errb.String())
	}
	if rc := jiraDescriptionRun(nil, []string{"AB-1"}, strings.NewReader("x"), &errb); rc != 1 {
		t.Errorf("no connection: rc=%d", rc)
	}
}

package jirax

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestJiraMarkdownADFTable(t *testing.T) {
	md := "Before:\n\n| 処理 | **方針** |\n|:---|---:|\n| `a\\|b` |  |\n| 1 |\n"
	got, _ := json.Marshal(jiraMarkdownADF(md)["content"])
	cell := func(kind, inner string) string {
		return `{"attrs":{},"content":[{"content":[` + inner + `],"type":"paragraph"}],"type":"` + kind + `"}`
	}
	text := func(s string) string { return `{"text":"` + s + `","type":"text"}` }
	want := `[{"content":[` + text("Before:") + `],"type":"paragraph"},` +
		`{"attrs":{"isNumberColumnEnabled":false,"layout":"default"},"content":[` +
		`{"content":[` + cell("tableHeader", text("処理")) + `,` +
		cell("tableHeader", `{"marks":[{"type":"strong"}],"text":"方針","type":"text"}`) + `],"type":"tableRow"},` +
		`{"content":[` + cell("tableCell", `{"marks":[{"type":"code"}],"text":"a|b","type":"text"}`) + `,` +
		cell("tableCell", text(" ")) + `],"type":"tableRow"},` +
		// A short row is padded to the header's width: Jira renders ragged rows as a broken grid.
		`{"content":[` + cell("tableCell", text("1")) + `,` + cell("tableCell", text(" ")) + `],"type":"tableRow"}` +
		`],"type":"table"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// ADF admits no table inside a list item or a quote; there it degrades to a line per row.
func TestJiraMarkdownADFNestedTable(t *testing.T) {
	for _, md := range []string{"- item\n\n  | a | b |\n  |---|---|\n  | 1 | 2 |\n", "> | a | b |\n> |---|---|\n> | 1 | 2 |\n"} {
		raw, _ := json.Marshal(jiraMarkdownADF(md))
		s := string(raw)
		if strings.Contains(s, `"type":"table"`) {
			t.Errorf("table nested where ADF refuses it: %s", s)
		}
		for _, want := range []string{`"text":"a"`, `"text":" | "`, `"text":"2"`, `"type":"hardBreak"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s in %s", want, s)
			}
		}
	}
	// An empty header row keeps its line, so the body row stays second.
	got, _ := json.Marshal(jiraMarkdownADF("> | |\n> |---|\n> | x |\n")["content"])
	want := `[{"content":[{"content":[{"text":" ","type":"text"},{"type":"hardBreak"},{"text":"x","type":"text"}],"type":"paragraph"}],"type":"blockquote"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestJiraMarkdownADFFallbacks(t *testing.T) {
	// Code mark next to strong: ADF refuses the pair, so only code stays.
	raw, _ := json.Marshal(jiraMarkdownADF("**`x`**"))
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
		{"issue", "comment", "frob", "T-1", "5"},
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

func TestJiraMarkdownADFEmailLink(t *testing.T) {
	for _, md := range []string{"write x@y.example now", "write <x@y.example> now"} {
		raw, _ := json.Marshal(jiraMarkdownADF(md))
		if !strings.Contains(string(raw), `"href":"mailto:x@y.example"`) {
			t.Errorf("%q: %s", md, raw)
		}
	}
}

func TestJiraCommentAddPostsADF(t *testing.T) {
	c, calls := commentStub(t, 201, `{"id":"10077"}`)
	rc, out, errs := runComment(c, "", "issue", "comment", "add", "T-1", "認証（`/api/auth`）で送る", "--no-input")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	if len(*calls) != 1 || (*calls)[0].method != "POST" || (*calls)[0].path != "/rest/api/3/issue/T-1/comment" {
		t.Fatalf("calls = %+v", *calls)
	}
	got, _ := json.Marshal((*calls)[0].body["body"].(map[string]any)["content"])
	want := `[{"content":[{"text":"認証（","type":"text"},{"marks":[{"type":"code"}],"text":"/api/auth","type":"text"},{"text":"）で送る","type":"text"}],"type":"paragraph"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, ok := (*calls)[0].body["properties"]; ok {
		t.Errorf("properties sent without --internal")
	}
	if !strings.Contains(out, "?focusedCommentId=10077") {
		t.Errorf("stdout %q", out)
	}
}

// jira-cli's precedence: a positional body wins over -T, then stdin; --internal is the JSM flag.
func TestJiraCommentAddBodySources(t *testing.T) {
	dir := t.TempDir()
	tmpl := dir + "/c.md"
	if err := os.WriteFile(tmpl, []byte("fromfile"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stdin, want string
		args        []string
	}{
		{"", "arg", []string{"T-1", "arg", "-T", tmpl}},
		{"", "fromfile", []string{"T-1", "--template", tmpl}},
		{"piped", "piped", []string{"T-1"}},
		{"piped", "piped", []string{"T-1", "-T", "-"}},
		{"piped", "piped", []string{"T-1", ""}},
	} {
		c, calls := commentStub(t, 201, `{"id":"1"}`)
		rc, _, errs := runComment(c, tc.stdin, append([]string{"issue", "comment", "add"}, tc.args...)...)
		if rc != 0 || len(*calls) != 1 {
			t.Fatalf("%v: rc=%d err=%q", tc.args, rc, errs)
		}
		got, _ := json.Marshal((*calls)[0].body["body"])
		if !strings.Contains(string(got), `"text":"`+tc.want+`"`) {
			t.Errorf("%v: body %s, want %q", tc.args, got, tc.want)
		}
	}
	c, calls := commentStub(t, 201, `{"id":"1"}`)
	if rc, _, errs := runComment(c, "", "issue", "comment", "add", "T-1", "x", "--internal"); rc != 0 {
		t.Fatalf("--internal: rc=%d err=%q", rc, errs)
	}
	props, _ := json.Marshal((*calls)[0].body["properties"])
	if string(props) != `[{"key":"sd.public.comment","value":{"internal":true}}]` {
		t.Errorf("--internal: properties %s", props)
	}
}

func TestJiraCommentAddRefusals(t *testing.T) {
	c, calls := commentStub(t, 201, `{}`)
	for _, args := range [][]string{
		{"issue", "comment", "add", "T-1"},           // no body at all
		{"issue", "comment", "add", "T-1", "x", "y"}, // too many words
		{"issue", "comment", "add", "1", "x"},        // a bare number is jira-cli's to complete
	} {
		if rc, _, _ := runComment(c, "", args...); rc != 2 {
			t.Errorf("%v: rc=%d, want 2", args, rc)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("a refused command line made calls: %+v", *calls)
	}
	c, _ = commentStub(t, 403, `{"errorMessages":["no"]}`)
	rc, _, errs := runComment(c, "", "issue", "comment", "add", "T-1", "x")
	if rc != 1 || !strings.Contains(errs, "add a comment to T-1 (403)") {
		t.Errorf("403: rc=%d err=%q", rc, errs)
	}
}

// A POST cut off in transport is not re-sent, and the message says the comment may exist.
func TestJiraCommentAddTransportFailureIsNotRetried(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	c := &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}
	rc, _, errs := runComment(c, "", "issue", "comment", "add", "T-1", "x")
	if rc != 1 || n != 1 || !strings.Contains(errs, "may have been added") {
		t.Errorf("rc=%d attempts=%d err=%q", rc, n, errs)
	}
}

// An unknown or malformed flag is refused before any call, as cobra refuses it: a mistyped
// --internal must not turn into a public comment.
func TestJiraCommentAddRejectsUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "comment", "add", "T-1", "secret", "--internl"},
		{"issue", "comment", "add", "T-1", "secret", "--internal=maybe"},
		{"issue", "comment", "add", "T-1", "secret", "--help"},
		{"issue", "comment", "add", "T-1", "secret", "-h"},
		{"issue", "comment", "add", "T-1", "secret", "-h=true"},
		{"issue", "comment", "add", "T-1", "secret", "--debug"},
		{"issue", "comment", "add", "T-1", "secret", "--web=false", "--web"},
		{"issue", "comment", "add", "T-1", "secret", "--help=false", "-h"},
		{"issue", "comment", "add", "T-1", "secret", "--web"},
		{"issue", "comment", "add", "T-1", "-b", "secret"},
		{"issue", "comment", "add", "T-1", "secret", "-x"},
		{"issue", "comment", "edit", "T-1", "5", "-b", "x", "--internal"},
	} {
		c, calls := commentStub(t, 201, `{"id":"1"}`)
		if rc, _, errs := runComment(c, "", args...); rc != 2 || len(*calls) != 0 {
			t.Errorf("%v: rc=%d calls=%d err=%q, want 2 and no call", args, rc, len(*calls), errs)
		}
	}
	for _, tc := range []struct {
		flag     string
		internal bool
	}{{"--internal=FALSE", false}, {"--internal=0", false}, {"--internal=T", true}, {"--no-input=false", false},
		{"--web=false", false}, {"--debug=false", false}, {"--help=false", false}, {"-h=false", false},
		{"--web --web=false", false}, {"--web=true --web=false", false}, {"-h --help=false", false}} {
		c, calls := commentStub(t, 201, `{"id":"1"}`)
		args := append([]string{"issue", "comment", "add", "T-1", "x"}, strings.Fields(tc.flag)...)
		if rc, _, errs := runComment(c, "", args...); rc != 0 || len(*calls) != 1 {
			t.Fatalf("%s: rc=%d err=%q", tc.flag, rc, errs)
		}
		if _, got := (*calls)[0].body["properties"]; got != tc.internal {
			t.Errorf("%s: internal property sent=%v, want %v", tc.flag, got, tc.internal)
		}
	}
}

// Emphasis that starts or ends with full-width punctuation next to Japanese text is still
// emphasis (the CJK-friendly flanking rules); ASCII text parses as plain CommonMark.
func TestJiraMarkdownADFCJKFriendlyEmphasis(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"**「すべて」**です", `{"marks":[{"type":"strong"}],"text":"「すべて」","type":"text"}`},
		{"これは**「強調」**。", `{"marks":[{"type":"strong"}],"text":"「強調」","type":"text"}`},
		{"*（注）*を参照", `{"marks":[{"type":"em"}],"text":"（注）","type":"text"}`},
		{"~~「旧」~~です", `{"marks":[{"type":"strike"}],"text":"「旧」","type":"text"}`},
		{"- **すべて**。次へ", `{"marks":[{"type":"strong"}],"text":"すべて","type":"text"}`},
		{"a**b**c", `{"marks":[{"type":"strong"}],"text":"b","type":"text"}`},
		{"foo**bar", `[{"text":"foo**","type":"text"},{"text":"bar","type":"text"}]`},
	} {
		got, _ := json.Marshal(jiraMarkdownADF(tc.md)["content"])
		if !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: %s\nlacks %s", tc.md, got, tc.want)
		}
	}
}

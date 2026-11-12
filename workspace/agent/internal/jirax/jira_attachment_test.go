package jirax

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// attachCall is one request the stub saw, with the uploaded part decoded.
type attachCall struct {
	method, path, query, auth, xsrf, ctype string
	contentLength                          int64
	chunked                                bool
	filename, partType                     string
	content                                []byte
}

type attachStub struct {
	mu    sync.Mutex
	calls []attachCall
}

func (s *attachStub) seen() []attachCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]attachCall(nil), s.calls...)
}

// newAttachStub records every call and lets reply answer it.
func newAttachStub(t *testing.T, reply func(n int, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *attachStub) {
	t.Helper()
	st := &attachStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc := attachCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"),
			xsrf: r.Header.Get("X-Atlassian-Token"), ctype: r.Header.Get("Content-Type"),
			contentLength: r.ContentLength, chunked: len(r.TransferEncoding) > 0}
		if mt, params, err := mime.ParseMediaType(cc.ctype); err == nil && mt == "multipart/form-data" {
			mr := multipart.NewReader(r.Body, params["boundary"])
			if p, err := mr.NextPart(); err == nil && p.FormName() == "file" {
				cc.filename, cc.partType = p.FileName(), p.Header.Get("Content-Type")
				cc.content, _ = io.ReadAll(p)
			}
		} else {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		st.mu.Lock()
		st.calls = append(st.calls, cc)
		n := len(st.calls)
		st.mu.Unlock()
		reply(n, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func tokenCreds(srv *httptest.Server) *secrets.JiraCreds {
	return &secrets.JiraCreds{Site: srv.URL, Email: "me@example.com", Token: "fake-api-tok"}
}

func runAttach(c *secrets.JiraCreds, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := jiraAttachmentRun(c, args, &out, &errb)
	return rc, out.String(), errb.String()
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const issueWithAttachments = `{"fields":{"attachment":[
 {"id":"501","filename":"shot.png","size":3,"mimeType":"image/png","created":"2026-11-12T10:00:00.000+0900","author":{"displayName":"Me"}},
 {"id":"502","filename":"../../evil\ttab.txt","size":4,"created":"2026-11-12T11:00:00.000+0900","author":{"displayName":"Other"}}]}}`

func TestJiraAttachmentAddUploadsMultipart(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"id":"777","filename":"shot.png","size":5}]`)
	})
	dir := t.TempDir()
	f := writeFile(t, dir, "shot.png", "\x89PNG!")
	rc, out, errs := runAttach(tokenCreds(srv), "issue", "attachment", "add", "-p", "T", "T-1", f)
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	calls := st.seen()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	c := calls[0]
	if c.method != "POST" || c.path != "/rest/api/3/issue/T-1/attachments" {
		t.Errorf("request %s %s", c.method, c.path)
	}
	if c.xsrf != "no-check" {
		t.Errorf("X-Atlassian-Token = %q", c.xsrf)
	}
	if !strings.HasPrefix(c.auth, "Basic ") {
		t.Errorf("auth header %q", c.auth)
	}
	if c.chunked || c.contentLength <= int64(len("\x89PNG!")) {
		t.Errorf("not a sized upload: chunked=%v length=%d", c.chunked, c.contentLength)
	}
	if c.filename != "shot.png" || c.partType != "image/png" || string(c.content) != "\x89PNG!" {
		t.Errorf("part: name=%q type=%q content=%q", c.filename, c.partType, c.content)
	}
	if !strings.Contains(out, `Attached "shot.png" to T-1 (id 777, 5 bytes)`) || !strings.Contains(out, srv.URL+"/browse/T-1") {
		t.Errorf("out = %q", out)
	}
	if strings.Contains(out+errs, "fake-api-tok") {
		t.Errorf("token in output")
	}
}

// --name sets what Jira shows, non-ASCII included, and a file without a known extension is sniffed.
func TestJiraAttachmentAddNameAndSniffedType(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `[]`) })
	f := writeFile(t, t.TempDir(), "log", "plain text \"quoted\"\n")
	rc, _, errs := runAttach(tokenCreds(srv), "issue", "attachment", "add", "T-1", f, "--name=ログ \"1\"")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	c := st.seen()[0]
	if c.filename != `ログ "1"` || !strings.HasPrefix(c.partType, "text/plain") {
		t.Errorf("part: name=%q type=%q", c.filename, c.partType)
	}
}

// A POST is never re-sent: a 5xx or a cut connection stops the run after one attempt, says the
// file may be attached, and names the files not sent.
func TestJiraAttachmentAddIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	a, b := writeFile(t, dir, "a.txt", "a"), writeFile(t, dir, "b.txt", "b")

	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	rc, _, errs := runAttach(tokenCreds(srv), "issue", "attachment", "add", "T-1", a, b)
	if rc != 1 || len(st.seen()) != 1 || !strings.Contains(errs, "not sent: "+b) {
		t.Errorf("503: rc=%d calls=%d err=%q", rc, len(st.seen()), errs)
	}

	srv2, st2 := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})
	rc, _, errs = runAttach(tokenCreds(srv2), "issue", "attachment", "add", "T-1", a, b)
	if rc != 1 || len(st2.seen()) != 1 {
		t.Errorf("reset: rc=%d calls=%d", rc, len(st2.seen()))
	}
	if !strings.Contains(errs, "may have been attached") || !strings.Contains(errs, "not sent: "+b) {
		t.Errorf("reset: err=%q", errs)
	}
}

func TestJiraAttachmentAddRefusals(t *testing.T) {
	f := writeFile(t, t.TempDir(), "a.txt", "a")
	for status, want := range map[int]string{
		413: "attachment size limit",
		403: "Create Attachments",
		404: "no such issue",
	} {
		srv, _ := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		rc, _, errs := runAttach(tokenCreds(srv), "issue", "attachment", "add", "T-1", f)
		if rc != 1 || !strings.Contains(errs, want) {
			t.Errorf("%d: rc=%d err=%q", status, rc, errs)
		}
	}
}

// A missing file or a directory is told before anything is sent.
func TestJiraAttachmentAddChecksFilesFirst(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `[]`) })
	dir := t.TempDir()
	ok := writeFile(t, dir, "a.txt", "a")
	for _, f := range []string{filepath.Join(dir, "missing"), dir} {
		rc, _, errs := runAttach(tokenCreds(srv), "issue", "attachment", "add", "T-1", ok, f)
		if rc != 2 || errs == "" {
			t.Errorf("%s: rc=%d err=%q", f, rc, errs)
		}
	}
	if len(st.seen()) != 0 {
		t.Errorf("sent %d calls", len(st.seen()))
	}
}

func TestJiraAttachmentList(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { io.WriteString(w, issueWithAttachments) })
	rc, out, errs := runAttach(tokenCreds(srv), "issues", "attachments", "list", "T-1")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	c := st.seen()[0]
	if c.method != "GET" || c.path != "/rest/api/3/issue/T-1" || c.query != "fields=attachment" {
		t.Errorf("request %s %s?%s", c.method, c.path, c.query)
	}
	want := "ID\tNAME\tSIZE\tCREATED\tAUTHOR\n" +
		"501\tshot.png\t3\t2026-11-12T10:00:00.000+0900\tMe\n" +
		"502\t../../evil tab.txt\t4\t2026-11-12T11:00:00.000+0900\tOther\n"
	if out != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
}

// download looks the ID up on the issue, fetches the content without a redirect, writes it under
// the attachment's base name only, and never replaces a file without --force.
func TestJiraAttachmentDownload(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/rest/api/3/attachment/content/") {
			io.WriteString(w, "DATA")
			return
		}
		io.WriteString(w, issueWithAttachments)
	})
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	rc, out, errs := runAttach(tokenCreds(srv), "issue", "attachment", "download", "T-1", "502")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	calls := st.seen()
	if len(calls) != 2 || calls[1].path != "/rest/api/3/attachment/content/502" || calls[1].query != "redirect=false" {
		t.Fatalf("calls = %+v", calls)
	}
	got, err := os.ReadFile(filepath.Join(dir, "evil\ttab.txt"))
	if err != nil || string(got) != "DATA" {
		t.Fatalf("file: %q %v (out %q)", got, err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "evil\ttab.txt")); err == nil {
		t.Errorf("wrote outside the directory")
	}

	rc, _, errs = runAttach(tokenCreds(srv), "issue", "attachment", "download", "T-1", "502")
	if rc != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("existing: rc=%d err=%q", rc, errs)
	}
	rc, _, _ = runAttach(tokenCreds(srv), "issue", "attachment", "download", "T-1", "502", "--force")
	if rc != 0 {
		t.Errorf("--force: rc=%d", rc)
	}
	rc, out, _ = runAttach(tokenCreds(srv), "issue", "attachment", "download", "T-1", "501", "-o", "-")
	if rc != 0 || out != "DATA" {
		t.Errorf("-o -: rc=%d out=%q", rc, out)
	}
	sub := filepath.Join(dir, "sub")
	_ = os.Mkdir(sub, 0o700)
	rc, _, _ = runAttach(tokenCreds(srv), "issue", "attachment", "download", "T-1", "501", "-o", sub)
	if _, err := os.Stat(filepath.Join(sub, "shot.png")); rc != 0 || err != nil {
		t.Errorf("-o DIR: rc=%d %v", rc, err)
	}
}

// An ID that is not on the issue is refused before any content or delete call.
func TestJiraAttachmentIDMustBelongToIssue(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { io.WriteString(w, issueWithAttachments) })
	t.Chdir(t.TempDir())
	for _, verb := range []string{"download", "delete"} {
		rc, _, errs := runAttach(tokenCreds(srv), "issue", "attachment", verb, "T-1", "999")
		if rc != 1 || !strings.Contains(errs, "has no attachment 999") {
			t.Errorf("%s: rc=%d err=%q", verb, rc, errs)
		}
	}
	for _, c := range st.seen() {
		if c.method != "GET" || c.path != "/rest/api/3/issue/T-1" {
			t.Errorf("unexpected call %s %s", c.method, c.path)
		}
	}
}

func TestJiraAttachmentDelete(t *testing.T) {
	srv, st := newAttachStub(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, issueWithAttachments)
	})
	rc, out, errs := runAttach(tokenCreds(srv), "issue", "attachment", "delete", "T-1", "501")
	if rc != 0 || !strings.Contains(out, `Attachment 501 ("shot.png") on T-1 deleted`) {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
	calls := st.seen()
	if len(calls) != 2 || calls[1].method != "DELETE" || calls[1].path != "/rest/api/3/attachment/501" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestParseJiraAttachmentArgsRejects(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "attachment", "add", "T-1"},
		{"issue", "attachment", "add", "T-1", "a", "b", "--name", "x"},
		{"issue", "attachment", "add", "T-1", "a", "--name", "../x"},
		{"issue", "attachment", "add", "T-1", "a", "--nmae", "x"},
		{"issue", "attachment", "add", "not-a-key", "a"},
		{"issue", "attachment", "list", "T-1", "extra"},
		{"issue", "attachment", "download", "T-1", "abc"},
		{"issue", "attachment", "delete", "T-1"},
		{"issue", "attachment", "delete", "T-1", "5", "-o", "x"},
		{"issue", "attachment", "list", "T-1", "--name", "x"},
		{"issue", "attachment", "rename", "T-1", "5"},
		{"issue", "attachment", "list", "T-1", "--debug"},
		{"issue", "attachment", "list", "T-1", "--force=maybe"},
	} {
		if _, err := parseJiraAttachmentArgs(args); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
	a, err := parseJiraAttachmentArgs([]string{"-c", "cfg.yml", "issue", "attachment", "add", "T-1", "--", "-dash.txt"})
	if err != nil || len(a.files) != 1 || a.files[0] != "-dash.txt" {
		t.Errorf("-- file: %+v %v", a, err)
	}
	if rc, out, _ := runAttach(nil, "issue", "attachment", "--help"); rc != 0 || !strings.Contains(out, "attachment add KEY FILE") {
		t.Errorf("help: rc=%d out=%q", rc, out)
	}
}

func TestJiraAttachmentNoConnection(t *testing.T) {
	rc, _, errs := runAttach(nil, "issue", "attachment", "list", "T-1")
	if rc != 1 || !strings.Contains(errs, "no Jira connection") {
		t.Errorf("rc=%d err=%q", rc, errs)
	}
}

// On OAuth the upload goes through api.atlassian.com/ex/jira/<cloudId>, and a 401 is answered by a
// renewal and one more attempt that sends the whole file again.
func TestJiraAttachmentAddOAuthRenewsOn401(t *testing.T) {
	b := newBridgeStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { okGrant(w) })
	c := setupJiraRefresh(t, b)
	srv, st := newAttachStub(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `[{"id":"9","filename":"a.txt","size":5}]`)
	})
	old := CloudAPIBase
	CloudAPIBase = srv.URL
	t.Cleanup(func() { CloudAPIBase = old })
	f := writeFile(t, t.TempDir(), "a.txt", "hello")
	rc, _, errs := runAttach(c, "issue", "attachment", "add", "T-1", f)
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	calls := st.seen()
	if len(calls) != 2 || calls[1].path != "/ex/jira/cid/rest/api/3/issue/T-1/attachments" ||
		string(calls[1].content) != "hello" || calls[1].auth != "Bearer at1" {
		t.Errorf("calls = %+v", calls)
	}
}

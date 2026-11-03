package jirax

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var v2Scopes = []string{"read:jira-work", "search:confluence", "read:page:confluence", "write:page:confluence", "read:space:confluence"}

type v2Call struct{ method, path, query, auth, body string }

type fakeConfluence struct {
	mu    sync.Mutex
	calls []v2Call
	// handler answers; status 0 means 500.
	h func(c v2Call) (int, string)
}

func (f *fakeConfluence) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *fakeConfluence) last() v2Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// seedConfluenceV2 stores an OAuth connection with the given scopes and points the Cloud API at
// a fake whose answers come from h.
func seedConfluenceV2(t *testing.T, scopes []string, h func(c v2Call) (int, string)) *fakeConfluence {
	t.Helper()
	f := &fakeConfluence{h: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := v2Call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		st, body := f.h(c)
		if st == 0 {
			st = 500
		}
		w.WriteHeader(st)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := CloudAPIBase
	CloudAPIBase = srv.URL
	t.Cleanup(func() { CloudAPIBase = old })
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", RefreshToken: "fake-rt", Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net", Scopes: scopes}}}
	})
	return f
}

func runPage(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := confluencePageMain(args, strings.NewReader(""), &out, &errb)
	return rc, out.String(), errb.String()
}

const (
	pathPage101  = "/ex/confluence/cid-1/wiki/api/v2/pages/101"
	pathPages    = "/ex/confluence/cid-1/wiki/api/v2/pages"
	pathSpaces   = "/ex/confluence/cid-1/wiki/api/v2/spaces"
	pathSpace7   = "/ex/confluence/cid-1/wiki/api/v2/spaces/7"
	spaceJSON    = `{"id":"7","key":"ENG","name":"開発 Space","type":"global"}`
	spacesAnswer = `{"results":[` + spaceJSON + `]}`
)

func pageJSON(version int, storage string) string {
	b, _ := json.Marshal(map[string]any{"id": "101", "status": "current", "title": "旧題", "spaceId": "7",
		"version": map[string]any{"number": version},
		"body":    map[string]any{"storage": map[string]any{"value": storage, "representation": "storage"}},
		"_links":  map[string]any{"webui": "/spaces/ENG/pages/101/x"}})
	return string(b)
}

func standardV2(storage string, version int) func(c v2Call) (int, string) {
	return func(c v2Call) (int, string) {
		switch {
		case c.method == "GET" && c.path == pathPage101:
			return 200, pageJSON(version, storage)
		case c.method == "GET" && c.path == pathSpaces:
			return 200, spacesAnswer
		case c.method == "GET" && c.path == pathSpace7:
			return 200, spaceJSON
		case c.method == "POST" && c.path == pathPages:
			return 200, pageJSON(1, "")
		case c.method == "PUT" && c.path == pathPage101:
			return 200, pageJSON(version+1, "")
		}
		return 404, `{}`
	}
}

func TestConfluencePageReadFormats(t *testing.T) {
	storage := `<h1>題</h1><p>It&#39;s <strong>太字</strong>です &amp; 😀</p>`
	f := seedConfluenceV2(t, v2Scopes, standardV2(storage, 3))
	for fmtName, want := range map[string]string{
		"storage":  storage + "\n",
		"markdown": "# 題\n\nIt's **太字**です & 😀\n",
		"text":     "題\n\nIt's 太字です & 😀\n",
	} {
		rc, out, errs := runPage("read", "101", "--format", fmtName)
		if rc != 0 || out != want {
			t.Errorf("%s: rc=%d out=%q err=%q, want %q", fmtName, rc, out, errs, want)
		}
	}
	if c := f.last(); c.query != "body-format=storage" || c.auth != "Bearer fake-at" {
		t.Errorf("read request = %+v", c)
	}
	// A page URL is accepted as the CLI does.
	if rc, _, errs := runPage("read", "https://acme.atlassian.net/wiki/spaces/ENG/pages/101/Title"); rc != 0 {
		t.Errorf("url: %s", errs)
	}
}

func TestConfluencePageInfo(t *testing.T) {
	seedConfluenceV2(t, v2Scopes, standardV2("<p>x</p>", 3))
	rc, out, errs := runPage("info", "101")
	want := "Page Information:\nTitle: 旧題\nID: 101\nType: page\nStatus: current\nSpace: 開発 Space (ENG)\n"
	if rc != 0 || out != want {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
}

// The body that reaches Confluence is exactly the converted Markdown, Japanese and emoji intact,
// the space key is resolved to its id, and a create is sent once.
func TestConfluencePageCreateSendsConvertedBodyOnce(t *testing.T) {
	md, _ := os.ReadFile("testdata/confluence/japanese.md")
	f := seedConfluenceV2(t, v2Scopes, standardV2("", 1))
	rc, out, errs := runPage("create", "題名：日本語😀", "ENG", "--format", "markdown", "-f", "testdata/confluence/japanese.md")
	if rc != 0 {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
	if !strings.Contains(out, "Space: 開発 Space (ENG)") || !strings.Contains(out, "URL: https://acme.atlassian.net/wiki/spaces/ENG/pages/101/x") {
		t.Errorf("out = %q", out)
	}
	if f.count("POST") != 1 {
		t.Fatalf("POST count = %d", f.count("POST"))
	}
	var got struct {
		SpaceID, Status, Title string
		Body                   struct{ Representation, Value string }
	}
	post := f.last()
	if err := json.Unmarshal([]byte(post.body), &got); err != nil {
		t.Fatal(err)
	}
	want, _ := MarkdownToStorage(strings.TrimRight(string(md), "\n"))
	if got.SpaceID != "7" || got.Status != "current" || got.Title != "題名：日本語😀" ||
		got.Body.Representation != "storage" || got.Body.Value != want {
		t.Errorf("payload = %+v", got)
	}
	if strings.Contains(post.body, "�") {
		t.Error("replacement character in payload")
	}
}

func TestConfluencePageCreateIsNeverRetried(t *testing.T) {
	f := seedConfluenceV2(t, v2Scopes, func(c v2Call) (int, string) {
		if c.method == "POST" {
			return 503, `{"message":"try later"}`
		}
		return standardV2("", 1)(c)
	})
	rc, _, errs := runPage("create", "T", "ENG", "-c", "x", "--format", "markdown")
	if rc != 1 || f.count("POST") != 1 || !strings.Contains(errs, "503") {
		t.Fatalf("rc=%d posts=%d err=%q", rc, f.count("POST"), errs)
	}
}

func TestConfluencePageUpdateBumpsVersionAndKeepsBodyForTitleOnly(t *testing.T) {
	f := seedConfluenceV2(t, v2Scopes, standardV2("<p>保持</p>", 4))
	rc, out, errs := runPage("update", "101", "-t", "新題")
	if rc != 0 || !strings.Contains(out, "Version: 5") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
	var got struct {
		Title   string
		Body    struct{ Value string }
		Version struct{ Number int }
	}
	_ = json.Unmarshal([]byte(f.last().body), &got)
	if got.Title != "新題" || got.Body.Value != "<p>保持</p>" || got.Version.Number != 5 {
		t.Errorf("payload = %+v", got)
	}
	rc, _, errs = runPage("update", "101", "-c", "**新**です", "--format", "markdown")
	_ = json.Unmarshal([]byte(f.last().body), &got)
	if rc != 0 || got.Title != "旧題" || got.Body.Value != "<p><strong>新</strong>です</p>" {
		t.Errorf("rc=%d err=%q payload=%+v", rc, errs, got)
	}
}

func TestConfluencePageUpdateConflictIsReportedNotRetried(t *testing.T) {
	f := seedConfluenceV2(t, v2Scopes, func(c v2Call) (int, string) {
		if c.method == "PUT" {
			return 409, `{"errors":[{"title":"Version conflict"}]}`
		}
		return standardV2("<p>x</p>", 4)(c)
	})
	rc, _, errs := runPage("update", "101", "-c", "y", "--format", "markdown")
	if rc != 1 || f.count("PUT") != 1 || !strings.Contains(errs, "version conflict") || !strings.Contains(errs, "nothing was overwritten") {
		t.Fatalf("rc=%d puts=%d err=%q", rc, f.count("PUT"), errs)
	}
}

func TestConfluencePageSpaceLookup(t *testing.T) {
	seedConfluenceV2(t, v2Scopes, standardV2("", 1))
	rc, out, _ := runPage("space-lookup", "ENG")
	var s v2Space
	if rc != 0 || json.Unmarshal([]byte(out), &s) != nil || s.Key != "ENG" || s.Name != "開発 Space" {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	seedConfluenceV2(t, v2Scopes, func(c v2Call) (int, string) { return 200, `{"results":[]}` })
	if _, out, _ := runPage("space-lookup", "NOPE"); !strings.Contains(out, `"found": false`) {
		t.Errorf("missing space: %q", out)
	}
}

// A grant from before the v2 scopes reaches nothing: the answer names the fix and no request is
// made (v2 would only say 401 "scope does not match").
func TestConfluencePageClassicOnlyGrantAsksForReconnect(t *testing.T) {
	f := seedConfluenceV2(t, []string{"read:jira-work", "read:confluence-content.all", "search:confluence"}, standardV2("", 1))
	rc, out, errs := runPage("read", "101")
	if rc != 1 || out != "" || len(f.calls) != 0 {
		t.Fatalf("rc=%d out=%q calls=%d", rc, out, len(f.calls))
	}
	for _, w := range []string{"read:page:confluence", "reconnect Jira", "administrator"} {
		if !strings.Contains(errs, w) {
			t.Errorf("message lacks %q: %s", w, errs)
		}
	}
	if strings.Contains(errs, "fake-at") {
		t.Error("token in message")
	}
}

// Unknown scopes are tried; a 401 about scopes becomes the same actionable message.
func TestConfluencePageScopeUnauthorizedBecomesActionable(t *testing.T) {
	stubAccessibleResources(t, `nope`, 500)
	seedConfluenceV2(t, nil, func(c v2Call) (int, string) {
		return 401, `{"code":401,"message":"Unauthorized; scope does not match"}`
	})
	rc, _, errs := runPage("read", "101")
	if rc != 1 || !strings.Contains(errs, "read:page:confluence") {
		t.Fatalf("rc=%d err=%q", rc, errs)
	}
}

func TestConfluencePageNoConnectionDefersToTheCLI(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {})
	if rc, _, _ := runPage("read", "101"); rc != confluencePageNoConnection {
		t.Fatalf("rc = %d", rc)
	}
}

func TestConfluencePageRefusesInvalidUTF8File(t *testing.T) {
	f := seedConfluenceV2(t, v2Scopes, standardV2("", 1))
	p := filepath.Join(t.TempDir(), "sjis.md")
	_ = os.WriteFile(p, []byte{0x93, 0xfa, 0x96, 0x7b}, 0o644) // Shift_JIS 日本
	rc, _, errs := runPage("create", "T", "ENG", "-f", p, "--format", "markdown")
	if rc != 1 || !strings.Contains(errs, "UTF-8") || f.count("POST") != 0 {
		t.Fatalf("rc=%d err=%q posts=%d", rc, errs, f.count("POST"))
	}
}

func TestConfluencePageUsageErrors(t *testing.T) {
	seedConfluenceV2(t, v2Scopes, standardV2("", 1))
	for _, args := range [][]string{
		{"update", "101"},
		{"create", "T", "ENG"},
		{"create", "T", "ENG", "-c", "x", "--type", "folder"},
		{"read", "101", "--front-matter", "a"},
		{"read"},
		{"read", "nonsense"},
	} {
		if rc, _, errs := runPage(args...); rc == 0 || errs == "" {
			t.Errorf("%v: rc=%d err=%q", args, rc, errs)
		}
	}
}

func TestConfluenceScopesStatus(t *testing.T) {
	site := func(sc ...string) *secrets.JiraCreds {
		return &secrets.JiraCreds{AuthKind: "oauth", CloudID: "c", Sites: []secrets.JiraSite{{CloudID: "c", Scopes: sc}}}
	}
	for name, tc := range map[string]struct {
		c    *secrets.JiraCreds
		want string
	}{
		"v2":       {site(v2Scopes...), "granted"},
		"classic":  {site("read:jira-work", "search:confluence"), "outdated"},
		"partial":  {site("read:page:confluence", "search:confluence"), "outdated"},
		"none":     {site("read:jira-work"), "missing"},
		"unknown":  {site(), ""},
		"apitoken": {&secrets.JiraCreds{Token: "t"}, ""},
	} {
		if got := ConfluenceScopes(tc.c); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// `info -f json` is the CLI's deprecated spelling of --json: a pipeline into jq must keep working.
func TestConfluencePageInfoLegacyJSONFormat(t *testing.T) {
	seedConfluenceV2(t, v2Scopes, standardV2("<p>x</p>", 3))
	for _, args := range [][]string{{"info", "101", "-f", "json"}, {"info", "101", "--format=json"}, {"info", "101", "--json"}} {
		rc, out, errs := runPage(args...)
		var got struct {
			ID    string
			Title string
			Space v2Space
		}
		if rc != 0 || json.Unmarshal([]byte(out), &got) != nil || got.ID != "101" || got.Space.Key != "ENG" {
			t.Errorf("%v: rc=%d out=%q err=%q", args, rc, out, errs)
		}
	}
	if rc, _, errs := runPage("info", "101", "-f", "yaml"); rc != 2 || !strings.Contains(errs, "yaml") {
		t.Errorf("bad format: rc=%d err=%q", rc, errs)
	}
}

// create-child takes the space from the parent, posts parentId, and prints the CLI's shape.
func TestConfluencePageCreateChild(t *testing.T) {
	f := seedConfluenceV2(t, v2Scopes, standardV2("", 1))
	rc, out, errs := runPage("create-child", "子ページ", "101", "-c", "**本文**です", "--format", "markdown")
	want := "✅ Child page created successfully!\nTitle: 旧題\nID: 101\nParent: 旧題 (101)\nSpace: 開発 Space (ENG)\nURL: https://acme.atlassian.net/wiki/spaces/ENG/pages/101/x\n"
	if rc != 0 || out != want {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errs)
	}
	var got struct {
		SpaceID, ParentID, Title string
		Body                     struct{ Value string }
	}
	if f.count("POST") != 1 {
		t.Fatalf("POST count %d", f.count("POST"))
	}
	_ = json.Unmarshal([]byte(f.last().body), &got)
	if got.SpaceID != "7" || got.ParentID != "101" || got.Title != "子ページ" || got.Body.Value != "<p><strong>本文</strong>です</p>" {
		t.Errorf("payload = %+v", got)
	}
	rc, out, _ = runPage("create-child", "T", "101", "-c", "x", "--format", "markdown", "--json")
	var j map[string]any
	if rc != 0 || json.Unmarshal([]byte(out), &j) != nil || j["parentId"] != "101" || j["spaceKey"] != "ENG" {
		t.Errorf("json: rc=%d out=%q", rc, out)
	}
}

// seedConfluenceV2Resets drops the connection (what ECONNRESET looks like to the client) for the first n
// requests of method m and answers normally after that.
func seedConfluenceV2Resets(t *testing.T, m string, n int) (*fakeConfluence, func() int) {
	t.Helper()
	var mu sync.Mutex
	resets := 0
	inner := standardV2("<p>x</p>", 3)
	f := seedConfluenceV2(t, v2Scopes, inner)
	// Wrap the fake's server: re-point CloudAPIBase at a proxy that resets first.
	target := CloudAPIBase
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		drop := r.Method == m && resets < n && strings.Contains(r.URL.Path, "/wiki/api/v2/")
		if drop {
			resets++
		}
		mu.Unlock()
		if drop {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		req, _ := http.NewRequest(r.Method, target+r.URL.RequestURI(), r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	CloudAPIBase = proxy.URL
	return f, func() int { mu.Lock(); defer mu.Unlock(); return resets }
}

// Reads survive a connection reset (the Agent's GETs are re-sent); writes are not.
func TestConfluencePageReadRetriesAfterConnectionReset(t *testing.T) {
	old := jiraRetryBase
	jiraRetryBase = time.Millisecond
	t.Cleanup(func() { jiraRetryBase = old })
	for _, args := range [][]string{{"read", "101"}, {"info", "101"}, {"edit", "101"}, {"space-lookup", "ENG"}} {
		_, resets := seedConfluenceV2Resets(t, "GET", 2)
		rc, out, errs := runPage(args...)
		if rc != 0 || out == "" || resets() != 2 {
			t.Errorf("%v: rc=%d out=%q err=%q resets=%d", args, rc, out, errs, resets())
		}
	}
}

func TestConfluencePageWritesAreNotRetriedAfterConnectionReset(t *testing.T) {
	old := jiraRetryBase
	jiraRetryBase = time.Millisecond
	t.Cleanup(func() { jiraRetryBase = old })
	for name, args := range map[string][]string{
		"POST": {"create", "T", "ENG", "-c", "x", "--format", "markdown"},
		"PUT":  {"update", "101", "-c", "x", "--format", "markdown"},
	} {
		_, resets := seedConfluenceV2Resets(t, name, 1)
		rc, _, errs := runPage(args...)
		if rc != 1 || resets() != 1 || !strings.Contains(errs, "may or may not") {
			t.Errorf("%s: rc=%d resets=%d err=%q", name, rc, resets(), errs)
		}
	}
}

package afmemory

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func lintAgent(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/agents/memory/entries/projects":
			io.WriteString(w, `{"projects":[{"project":{"id":"demo-0123456789ab","display":"demo"},"count":2},{"project":{"id":"empty-0123456789ab","display":"empty"},"count":0}]}`)
		case "/agents/memory/entries/lint":
			if r.URL.Query().Get("scope") == "user" {
				io.WriteString(w, `{"scope":"user","terms":1,"report":{"checked":1,"findings":[{"memory":"u1","check":"oversize","subject":"5000"}],"counts":{"oversize":1},"skipped":["repository checks (paths, symbols, ADRs): user scope: no repository"]}}`)
				return
			}
			io.WriteString(w, `{"scope":"project","project":{"id":"demo-0123456789ab","display":"demo"},"ref":"origin/main","commit":"abc123","terms":1,"report":{"checked":2,"findings":[{"memory":"a","check":"broken-link","subject":"ghost","line":3}],"counts":{"broken-link":1}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, HTTP: srv.Client()}, &reqs
}

// The command prints findings with their line and what was skipped, asks only GETs, and with no
// flag lints the user scope and every project that holds memory.
func TestAFMemoryLint(t *testing.T) {
	c, reqs := lintAgent(t)
	code, out, errw := run(c, "lint")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	for _, want := range []string{"== demo: 2 memories, 1 finding(s)  [origin/main @ abc123]", "broken-link    a:3  ghost", "== user", "oversize       u1  5000 bytes", "skipped: repository checks"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "empty") {
		t.Errorf("a project without memories was linted:\n%s", out)
	}
	for _, r := range *reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("lint sent %s: it must only read", r)
		}
	}

	*reqs = nil
	if code, out, _ := run(c, "lint", "--project", "demo", "--json"); code != 0 || !strings.Contains(out, `"broken-link"`) || len(*reqs) != 2 {
		t.Errorf("--project: %d reqs=%v\n%s", code, *reqs, out)
	}
	if code, _, e := run(c, "lint", "--project", "nope"); code != 1 || !strings.Contains(e, `no project "nope"`) {
		t.Errorf("unknown project: %d %q", code, e)
	}
	if code, _, _ := run(c, "lint", "--user", "--project", "demo"); code != 2 {
		t.Errorf("--user with --project: %d, want 2", code)
	}
}

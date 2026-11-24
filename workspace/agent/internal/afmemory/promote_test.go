package afmemory

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func promoteAgent(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/agents/memory/entries/projects":
			io.WriteString(w, `{"projects":[{"project":{"id":"demo-0123456789ab","display":"demo"},"count":2},{"project":{"id":"empty-0123456789ab","display":"empty"},"count":0},`+
				`{"project":{"id":"held-0123456789ab","display":"held"},"count":0,"withheld":1},`+
				`{"project":{"id":"twin-0123456789ab","display":"twin"},"count":1},{"project":{"id":"twin-ba9876543210","display":"twin"},"count":1}]}`)
		case "/agents/memory/entries/promotion-candidates":
			io.WriteString(w, `{"project":{"id":"demo-0123456789ab","display":"demo"},"ref":"origin/main","commit":"abc123","minSessions":2,"quietDays":7,`+
				`"candidates":[{"name":"release-steps","revision":3,"kind":"procedure","sessions":2,"anchors":["scripts/release.sh"],"updated":"2026-11-01T00:00:00Z"}],`+
				`"held":[{"name":"one-session","revision":1,"kind":"lesson","missing":[{"rule":"confirmations","detail":"1 of 2 sessions on revision 1"},{"rule":"no-anchors"}]}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, HTTP: srv.Client()}, &reqs
}

// Without --project every project that holds memory is asked, only with GETs; a candidate prints
// its anchors and a held memory everything it misses.
func TestAFMemoryPromoteCandidates(t *testing.T) {
	c, reqs := promoteAgent(t)
	code, out, errw := run(c, "promote-candidates")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	for _, want := range []string{
		"== demo: 1 candidate(s), 1 held (confirmed by 2 sessions, unchanged for 7 days)  [origin/main @ abc123]",
		"candidate  release-steps r3  procedure, 2 sessions, since 2026-11-01T00:00:00Z",
		"anchors: scripts/release.sh",
		"held       one-session r1  lesson: confirmations (1 of 2 sessions on revision 1); no-anchors",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	var asked []string
	for _, r := range *reqs {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("sent %s: the command must only read", r)
		}
		if strings.Contains(r, "promotion-candidates") {
			asked = append(asked, r)
		}
	}
	// A project whose only files are withheld is still asked: the answer says how many.
	if strings.Join(asked, " ") != "GET /agents/memory/entries/promotion-candidates?project=demo-0123456789ab "+
		"GET /agents/memory/entries/promotion-candidates?project=held-0123456789ab "+
		"GET /agents/memory/entries/promotion-candidates?project=twin-0123456789ab "+
		"GET /agents/memory/entries/promotion-candidates?project=twin-ba9876543210" {
		t.Errorf("asked %v: only a project without any file must be left out", asked)
	}

	*reqs = nil
	if code, out, _ := run(c, "promote-candidates", "--project", "demo", "--days", "14", "--json"); code != 0 || !strings.Contains(out, `"release-steps"`) || !strings.Contains(out, `"missing"`) {
		t.Errorf("--json: %d\n%s", code, out)
	}
	if want := "GET /agents/memory/entries/promotion-candidates?project=demo-0123456789ab&days=14"; len(*reqs) != 2 || (*reqs)[1] != want {
		t.Errorf("reqs = %v, want %s", *reqs, want)
	}
	if code, _, e := run(c, "promote-candidates", "--project", "nope"); code != 1 || !strings.Contains(e, `no project "nope"`) {
		t.Errorf("unknown project: %d %q", code, e)
	}
	if code, _, _ := run(c, "promote-candidates", "--project="); code != 2 {
		t.Errorf("empty --project: %d, want 2", code)
	}
	if code, _, _ := run(c, "promote-candidates", "extra"); code != 2 {
		t.Errorf("stray argument: %d, want 2", code)
	}
	// A name two projects share is refused; the id of either still works.
	if code, _, e := run(c, "promote-candidates", "--project", "twin"); code != 1 || !strings.Contains(e, "several projects") {
		t.Errorf("ambiguous name: %d %q", code, e)
	}
	if code, _, e := run(c, "promote-candidates", "--project", "twin-ba9876543210"); code != 0 {
		t.Errorf("id of a twin: %d %q", code, e)
	}
	// --days is checked before anything is asked, with the Agent's rule: decimal 1 to 90.
	for _, d := range []string{"0", "91", "", "x", "-1", "+5", "0x10"} {
		*reqs = nil
		if code, _, _ := run(c, "promote-candidates", "--days="+d); code != 2 || len(*reqs) != 0 {
			t.Errorf("--days=%q: exit %d after %v, want 2 and no request", d, code, *reqs)
		}
	}
	for d, sent := range map[string]string{"1": "days=1", "90": "days=90", "08": "days=8"} {
		*reqs = nil
		if code, _, _ := run(c, "promote-candidates", "--project", "demo", "--days", d); code != 0 || len(*reqs) != 2 || !strings.HasSuffix((*reqs)[1], "&"+sent) {
			t.Errorf("--days %s: exit %d, %v", d, code, *reqs)
		}
	}
}

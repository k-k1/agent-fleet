package afmemory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeAgent(t *testing.T, applied *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-xyz" {
			w.WriteHeader(401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /agents/memory/claude-import":
			io.WriteString(w, `{"sources":[{"slug":"-a","count":2,"project":{"id":"a-0123456789ab","display":"a"}},{"slug":"-b","count":1,"reason":"no_project"}]}`)
		case "GET /agents/memory/claude-import/preview":
			if r.URL.Query().Get("slug") == "-b" {
				io.WriteString(w, `{"reason":"no_project","items":[],"counts":{}}`)
				return
			}
			var items []string
			for i := 0; i < 120; i++ {
				items = append(items, `{"name":"n`+strings.Repeat("x", i%3)+string(rune('a'+i%26))+`","status":"new","sourceHash":"h"}`)
			}
			items = append(items, `{"name":"bad","status":"secret","findings":[{"path":"body","rule":"private-key-block","line":3,"hint":"-----BEG…"}]}`)
			io.WriteString(w, `{"project":{"id":"a-0123456789ab","display":"a"},"counts":{"new":120,"secret":1},"items":[`+strings.Join(items, ",")+`]}`)
		case "POST /agents/memory/claude-import":
			var req struct {
				Project string
				Items   []struct{ Name string }
			}
			json.NewDecoder(r.Body).Decode(&req)
			if len(req.Items) > 50 || req.Project != "a-0123456789ab" || r.URL.Query().Get("project") != req.Project {
				w.WriteHeader(400)
				return
			}
			*applied = append(*applied, r.URL.RawQuery)
			var rs []string
			for _, it := range req.Items {
				rs = append(rs, `{"name":"`+it.Name+`","result":"imported"}`)
			}
			io.WriteString(w, `{"results":[`+strings.Join(rs, ",")+`]}`)
		case "GET /agents/memory/entries/changes":
			io.WriteString(w, `{"changes":[{"at":"2026-10-05T00:00:00Z","op":"import","name":"x","authorKind":"member","authorSession":"console","project":{"display":"a"}}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: "tok-xyz", HTTP: srv.Client()}
}

func run(c *Client, args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := Main(c, args, &o, &e)
	return code, o.String(), e.String()
}

func TestAFMemoryCommands(t *testing.T) {
	var applied []string
	c := fakeAgent(t, &applied)

	if code, out, _ := run(c, "import-sources"); code != 0 || !strings.Contains(out, "-> a") || !strings.Contains(out, "no_project") {
		t.Errorf("sources: %d %q", code, out)
	}
	if code, out, _ := run(c, "changes", "--limit", "5"); code != 0 || !strings.Contains(out, "import") {
		t.Errorf("changes: %d %q", code, out)
	}
	code, out, _ := run(c, "import", "--project", "-a", "--dry-run")
	if code != 0 || len(applied) != 0 || !strings.Contains(out, "private-key-block in body, line 3") || !strings.Contains(out, "nothing was written") {
		t.Errorf("dry run: %d applied=%v\n%s", code, applied, out)
	}
	// 120 items go in batches of 50.
	code, out, _ = run(c, "import", "--project", "-a")
	if code != 0 || len(applied) != 3 || !strings.Contains(out, "done: 120 imported, 0 skipped") {
		t.Errorf("import: %d batches=%d\n%s", code, len(applied), out)
	}
	if code, _, e := run(c, "import", "--project", "-b"); code != 1 || !strings.Contains(e, "no_project") {
		t.Errorf("no_project: %d %q", code, e)
	}
}

func TestAFMemoryUsageAndTokenNeverPrinted(t *testing.T) {
	c := fakeAgent(t, new([]string))
	for _, args := range [][]string{nil, {"bogus"}, {"import"}, {"changes", "x"}} {
		if code, _, e := run(c, args...); code != 2 || !strings.Contains(e, "Usage") {
			t.Errorf("%v: code %d", args, code)
		}
	}
	if code, out, _ := run(c, "--help"); code != 0 || !strings.Contains(out, "import-sources") {
		t.Errorf("help: %d", code)
	}
	bad := &Client{Base: c.Base, Token: "wrong-token-value", HTTP: c.HTTP}
	code, o, e := run(bad, "import-sources")
	if code != 1 || strings.Contains(o+e, "wrong-token-value") {
		t.Errorf("bad token: %d %q %q", code, o, e)
	}
}

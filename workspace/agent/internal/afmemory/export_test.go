package afmemory

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeExportAgent(t *testing.T, posted *[]map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /agents/memory/claude-export":
			io.WriteString(w, `{"projects":[{"project":{"id":"demo-0123456789ab","display":"demo"},"count":3}]}`)
		case "GET /agents/memory/claude-export/preview":
			if r.URL.Query().Get("project") != "demo-0123456789ab" {
				w.WriteHeader(404)
				return
			}
			io.WriteString(w, `{"project":{"id":"demo-0123456789ab","display":"demo"},"slug":"-home-demo","token":"tok1",
"counts":{"new":1,"conflict":1,"unchanged":1},"userScope":2,"index":"rewrite","indexListed":3,
"items":[{"name":"fresh","status":"new"},{"name":"clash","status":"conflict","reason":"not_written_by_af"},{"name":"same","status":"unchanged"}]}`)
		case "POST /agents/memory/claude-export":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			*posted = append(*posted, req)
			io.WriteString(w, `{"results":[{"name":"fresh","result":"written"},{"name":"clash","result":"skipped","reason":"conflict"}],"snapshot":"0123456789abcdef","index":"written"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, HTTP: srv.Client()}
}

func TestExportDryRunWritesNothing(t *testing.T) {
	var posted []map[string]any
	c := fakeExportAgent(t, &posted)
	code, out, errs := run(c, "export", "--project", "demo", "--dry-run")
	if code != 0 || len(posted) != 0 {
		t.Fatalf("code %d posted %v stderr %s", code, posted, errs)
	}
	for _, want := range []string{"claude directory -home-demo", "fresh", "clash: not_written_by_af", "user scope  2", "dry run: 1 file(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestExportApplyCarriesTokenAndOverwrite(t *testing.T) {
	var posted []map[string]any
	c := fakeExportAgent(t, &posted)
	code, out, errs := run(c, "export", "--project", "demo-0123456789ab", "--overwrite", "clash", "--overwrite", "other")
	if code != 0 || len(posted) != 1 {
		t.Fatalf("code %d posted %v stderr %s", code, posted, errs)
	}
	req := posted[0]
	if req["token"] != "tok1" || req["project"] != "demo-0123456789ab" {
		t.Errorf("request = %v", req)
	}
	if ow, _ := req["overwrite"].([]any); len(ow) != 2 || ow[0] != "clash" {
		t.Errorf("overwrite = %v", req["overwrite"])
	}
	if !strings.Contains(out, "snapshot of claude's memory taken first") || !strings.Contains(out, "done: 1 written or removed, 1 skipped") {
		t.Errorf("output:\n%s", out)
	}
}

func TestExportUsageAndUnknownProject(t *testing.T) {
	var posted []map[string]any
	c := fakeExportAgent(t, &posted)
	if code, _, _ := run(c, "export"); code != 2 {
		t.Errorf("no project: code %d", code)
	}
	if code, _, errs := run(c, "export", "--project", "nope"); code != 1 || !strings.Contains(errs, "no AF project") {
		t.Errorf("unknown project: code %d %s", code, errs)
	}
	if code, out, _ := run(c, "export-sources"); code != 0 || !strings.Contains(out, "demo-0123456789ab") {
		t.Errorf("export-sources: %d %s", code, out)
	}
}

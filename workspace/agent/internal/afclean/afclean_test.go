package afclean

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fake is an Agent whose answers mirror what the Console's modal reads. calls records every
// request as "METHOD path?query" so a test can assert what was (not) sent.
type fake struct {
	mu    sync.Mutex
	calls []string
	// dirs per worktree for the dry run; changeAfter makes a second dry run list one more dir.
	changeAfter map[string]bool
	dryRuns     map[string]int
	busyDelete  bool
}

func (f *fake) did(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) client(t *testing.T) *Client {
	f.dryRuns = map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /cleanup/worktree-output":
			w.Write([]byte(`{"dormant_days":30,"measured_at":"x","worktrees":[
				{"name":"small","status":"candidate","bytes":10},
				{"name":"big","status":"candidate","bytes":500},
				{"name":"live","status":"refused","reason":"live_session","bytes":900},
				{"name":"clean","status":"empty","bytes":0},
				{"name":"zero","status":"candidate","bytes":0}]}`))
		case "POST /cleanup/worktree-output":
			name := r.URL.Query().Get("worktree")
			dry := r.URL.Query().Get("dry_run") == "1"
			if dry {
				f.dryRuns[name]++
			}
			dirs := `[{"rel":"node_modules","bytes":400}]`
			if f.changeAfter[name] && f.dryRuns[name] > 1 {
				dirs = `[{"rel":"node_modules","bytes":400},{"rel":".next","bytes":100}]`
			}
			bytes := "500"
			if name == "small" {
				dirs, bytes = `[{"rel":"target","bytes":10}]`, "10"
			}
			if name == "zero" {
				dirs, bytes = `[]`, "0"
			}
			w.Write([]byte(`{"name":"` + name + `","status":"candidate","bytes":` + bytes + `,"dirs":` + dirs + `}`))
		case "GET /cleanup/tool-caches":
			w.Write([]byte(`{"caches":[
				{"name":"npm","bytes":1000},
				{"name":"uv","bytes":2000,"busy":[42]},
				{"name":"gradle-caches","bytes":3000,"skipped":"pins unknown"},
				{"name":"pip","bytes":0},
				{"name":"go-build","bytes":4000}]}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/cleanup/tool-caches/") && r.Method == "DELETE" {
				name := strings.TrimPrefix(r.URL.Path, "/cleanup/tool-caches/")
				if f.busyDelete && name == "go-build" {
					w.WriteHeader(409)
					w.Write([]byte(`{"error":{"code":"cache_in_use","message":"go-build is in use"}}`))
					return
				}
				w.Write([]byte(`{"name":"` + name + `","bytes":123,"files":1}`))
				return
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: "tok", HTTP: srv.Client()}
}

func run(c *Client, args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := Main(c, args, &o, &e)
	return code, o.String(), e.String()
}

func ids(r *Result) string {
	var s []string
	for _, i := range r.Items {
		s = append(s, i.ID)
	}
	return strings.Join(s, ",")
}

// The proposal is the one ReclaimModal builds: candidate worktrees (largest first) with a
// non-empty dry run, then cache rows that are neither busy, unoffered nor empty.
func TestProposalMatchesConsoleRules(t *testing.T) {
	f := &fake{}
	code, out, _ := run(f.client(t), "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var r Result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if got, want := ids(&r), "wt:big,wt:small,cache:npm,cache:go-build"; got != want {
		t.Errorf("items %s, want %s", got, want)
	}
	if r.Mode != "dry-run" || r.Total != 500+10+1000+4000 {
		t.Errorf("mode/total: %s %d", r.Mode, r.Total)
	}
}

// The default and --dry-run send no real delete: every worktree POST carries dry_run=1 and no
// DELETE goes out.
func TestDryRunDeletesNothing(t *testing.T) {
	for _, args := range [][]string{nil, {"--dry-run"}} {
		f := &fake{}
		code, out, _ := run(f.client(t), args...)
		if code != 0 || !strings.Contains(out, "nothing was deleted") {
			t.Fatalf("%v: %d %q", args, code, out)
		}
		if f.did("DELETE") != 0 {
			t.Errorf("%v: a DELETE was sent: %v", args, f.calls)
		}
		for _, c := range f.calls {
			if strings.HasPrefix(c, "POST") && !strings.Contains(c, "dry_run=1") {
				t.Errorf("%v: real POST in a dry run: %s", args, c)
			}
		}
	}
}

// --apply removes the listed items and only those: not the refused / empty worktrees, not the
// busy, unoffered or empty caches.
func TestApplyRemovesExactlyTheList(t *testing.T) {
	f := &fake{}
	code, out, _ := run(f.client(t), "--apply")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var real, del []string
	for _, c := range f.calls {
		switch {
		case strings.HasPrefix(c, "POST") && !strings.Contains(c, "dry_run=1"):
			real = append(real, c)
		case strings.HasPrefix(c, "DELETE"):
			del = append(del, c)
		}
	}
	if strings.Join(real, " ") != "POST /cleanup/worktree-output?worktree=big POST /cleanup/worktree-output?worktree=small" {
		t.Errorf("real POSTs: %v", real)
	}
	if strings.Join(del, " ") != "DELETE /cleanup/tool-caches/npm? DELETE /cleanup/tool-caches/go-build?" {
		t.Errorf("DELETEs: %v", del)
	}
	if !strings.Contains(out, "freed") {
		t.Errorf("summary missing: %q", out)
	}
}

// A directory that appeared after the list was drawn must not be deleted unseen: the item is
// skipped, the others still run.
func TestApplySkipsWorktreeThatChanged(t *testing.T) {
	f := &fake{changeAfter: map[string]bool{"big": true}}
	code, out, _ := run(f.client(t), "--apply", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if f.did("POST /cleanup/worktree-output?worktree=big") != 2 { // one listing dry run + one re-check, no real POST
		t.Errorf("big calls: %v", f.calls)
	}
	var r Result
	_ = json.Unmarshal([]byte(out), &r)
	for _, it := range r.Items {
		if it.ID == "wt:big" && it.Outcome != "skipped" {
			t.Errorf("big outcome %q", it.Outcome)
		}
		if it.ID == "wt:small" && it.Outcome != "removed" {
			t.Errorf("small outcome %q", it.Outcome)
		}
	}
	if r.Skipped != 1 {
		t.Errorf("skipped %d", r.Skipped)
	}
}

// The Agent's own refusal of a cache in use is "skipped", not a failure.
func TestApplyCacheInUseIsSkipped(t *testing.T) {
	f := &fake{busyDelete: true}
	code, out, _ := run(f.client(t), "--apply")
	if code != 0 || !strings.Contains(out, "1 skipped, 0 failed") {
		t.Errorf("%d %q", code, out)
	}
}

func TestUsage(t *testing.T) {
	f := &fake{}
	c := f.client(t)
	for _, args := range [][]string{{"--apply", "--dry-run"}, {"bogus"}, {"--nope"}} {
		if code, _, _ := run(c, args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("usage error reached the Agent: %v", f.calls)
	}
	if code, out, _ := run(c, "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: %d", code)
	}
}

func TestAgentUnreachable(t *testing.T) {
	c := &Client{Base: "http://127.0.0.1:1", HTTP: http.DefaultClient}
	code, _, errw := run(c)
	if code != 1 || !strings.Contains(errw, "cannot reach the Agent") {
		t.Errorf("%d %q", code, errw)
	}
}

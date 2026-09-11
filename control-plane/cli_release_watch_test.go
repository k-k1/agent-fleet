// Tests for the upstream release-watcher block (cli_release_watch.go).
//
// The hard requirement here is that NOTHING in this package ever dials api.github.com:
// GET /api/env/ws-settings starts a refresh, several handler tests call it, and a suite
// that quietly spends the anonymous hourly budget would also make a green run depend on
// the public internet. TestMain below redirects the package's own instance once, before
// any test runs; every test that wants an answer builds its own cache against httptest.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Port 1 refuses instantly, so the background refresh a handler test kicks off dies
	// without waiting and without leaving the host.
	cliRelease = newCLIReleaseCache("http://127.0.0.1:1")
	os.Exit(m.Run())
}

// TestCLIReleaseProductionBaseIsGitHub is the positive control for the line above: if the
// constant ever stopped pointing at GitHub, every "we read the real state" claim in this
// file would be about nothing, and the redirect in TestMain would be guarding a no-op.
func TestCLIReleaseProductionBaseIsGitHub(t *testing.T) {
	if cliReleaseGitHubAPI != "https://api.github.com" {
		t.Fatalf("production base is %q", cliReleaseGitHubAPI)
	}
	if cliRelease.base == cliReleaseGitHubAPI {
		t.Fatal("the package instance was not redirected; this suite would call the real GitHub")
	}
}

// --- a fake GitHub ------------------------------------------------------------------

// fakeGitHub serves the two endpoints the watcher block reads and counts every hit, so a
// test can prove the cache did NOT go out a second time.
type fakeGitHub struct {
	srv      *httptest.Server
	hits     atomic.Int64
	body     string     // the tracking issue's body
	comments [][]string // comment bodies, one slice per page
	status   int        // when non-zero, every request answers with this instead
	titled   string     // the issue title to serve (defaults to the real one)
}

func newFakeGitHub(t *testing.T, body string, comments ...[]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{body: body, comments: comments, titled: cliReleaseIssueTitle}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+cliReleaseRepo+"/issues", func(w http.ResponseWriter, r *http.Request) {
		if g.deny(w) {
			return
		}
		total := 0
		for _, page := range g.comments {
			total += len(page)
		}
		// A pull request in the same list, to prove it is skipped rather than matched on
		// title: /issues returns PRs too, which `gh issue list` never shows.
		writeTestJSON(t, w, []map[string]any{
			{"number": 3, "title": g.titled, "body": "not me", "comments": 0, "pull_request": map[string]any{"url": "x"}},
			{"number": 4, "title": g.titled, "body": g.body, "comments": total},
		})
	})
	mux.HandleFunc("/repos/"+cliReleaseRepo+"/issues/4/comments", func(w http.ResponseWriter, r *http.Request) {
		if g.deny(w) {
			return
		}
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		items := []map[string]any{}
		if page >= 1 && page <= len(g.comments) {
			for _, b := range g.comments[page-1] {
				items = append(items, map[string]any{"body": b})
			}
		}
		writeTestJSON(t, w, items)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) deny(w http.ResponseWriter) bool {
	g.hits.Add(1)
	if g.status != 0 {
		http.Error(w, `{"message":"nope"}`, g.status)
		return true
	}
	return false
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode fake response: %v", err)
	}
}

// cacheFor builds a cache against the fake whose refresh runs inline, so snapshot()
// returns the fetched value instead of racing a goroutine.
func cacheFor(g *fakeGitHub) *cliReleaseCache {
	c := newCLIReleaseCache(g.srv.URL)
	c.spawn = func(f func()) { f() }
	return c
}

// healthyBody is what cli-release-state.sh leaves after a clean run.
const healthyBody = "Machine-readable state.\n\n### Watcher\n\nRewritten by `cli-release-watch.yml` on every run.\n" +
	"- `cli-release-state watcher ok=2026-09-11T05:30:12Z`\n" +
	"- `cli-release-state watcher failed=none`\n" +
	"- `cli-release-state watcher at=2026-09-11T05:30:12Z`\n"

func tested(cli, ver string) string {
	return fmt.Sprintf("<!-- cli-release-state tested %s=%s -->", cli, ver)
}

// --- the four upstream answers -------------------------------------------------------

func TestCLIReleaseFetchReadsWatcherAndTested(t *testing.T) {
	g := newFakeGitHub(t, healthyBody, []string{
		tested("claude", "2.1.265"),
		tested("codex", "0.152.1"),
		// Last one wins, the same `tail -1` rule the shell reads these back with.
		tested("claude", "2.1.267"),
		"a human comment that merely mentions `cli-release-state tested claude=9.9.9`",
	})
	got := mustSnapshot(t, cacheFor(g))
	if got.WatcherOkAt != "2026-09-11T05:30:12Z" || got.WatcherAt != "2026-09-11T05:30:12Z" {
		t.Fatalf("watcher timestamps: %+v", got)
	}
	if len(got.WatcherFailed) != 0 {
		t.Errorf("failed=none must read as an empty list, got %v", got.WatcherFailed)
	}
	if got.Tested["claude"] != "2.1.267" || got.Tested["codex"] != "0.152.1" {
		t.Errorf("tested: %v", got.Tested)
	}
	if got.FetchedAt == "" {
		t.Error("fetchedAt is empty")
	}
}

func TestCLIReleaseFetchReadsFailedRows(t *testing.T) {
	body := strings.Replace(healthyBody, "failed=none", "failed=rtk,kiro", 1)
	// `ok` does not move on a run that could not read every row — that is the whole
	// signal. Leave it at the older run and check it survives.
	body = strings.Replace(body, "ok=2026-09-11", "ok=2026-09-08", 1)
	g := newFakeGitHub(t, body, []string{tested("claude", "2.1.265")})
	got := mustSnapshot(t, cacheFor(g))
	if len(got.WatcherFailed) != 2 || got.WatcherFailed[0] != "rtk" || got.WatcherFailed[1] != "kiro" {
		t.Fatalf("failed rows: %v", got.WatcherFailed)
	}
	if got.WatcherOkAt != "2026-09-08T05:30:12Z" {
		t.Errorf("watcherOkAt: %q", got.WatcherOkAt)
	}
}

func TestCLIReleaseFetchOnMissingIssueAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		title  string
	}{
		{name: "404", status: http.StatusNotFound},
		{name: "rate limit 403", status: http.StatusForbidden},
		{name: "issue gone", title: "something else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t, healthyBody, []string{tested("claude", "2.1.265")})
			g.status, g.titled = tc.status, tc.title
			if g.titled == "" {
				g.titled = cliReleaseIssueTitle
			}
			if _, err := cacheFor(g).fetch(context.Background()); err == nil {
				t.Fatal("want an error so the caller keeps its previous answer")
			}
		})
	}
}

// A body the watcher never wrote to (or whose format drifted) must not be guessed at: the
// fields stay empty, which the Console draws as nothing rather than as a healthy run.
func TestCLIReleaseFetchOnUnparseableBody(t *testing.T) {
	g := newFakeGitHub(t, "### Watcher\n\nsomebody rewrote this by hand\nwatcher ok = yesterday\n",
		[]string{tested("claude", "2.1.265")})
	got := mustSnapshot(t, cacheFor(g))
	if got.WatcherOkAt != "" || got.WatcherAt != "" || len(got.WatcherFailed) != 0 {
		t.Fatalf("an unreadable body must leave the watcher fields empty: %+v", got)
	}
	if got.Tested["claude"] != "2.1.265" {
		t.Errorf("the tested markers are still readable: %v", got.Tested)
	}
}

// --- the cache -----------------------------------------------------------------------

func TestCLIReleaseCacheDoesNotRefetchWithinTTL(t *testing.T) {
	g := newFakeGitHub(t, healthyBody, []string{tested("claude", "2.1.265")})
	c := cacheFor(g)
	mustSnapshot(t, c)
	first := g.hits.Load()
	if first == 0 {
		t.Fatal("the first read never went out — the rest of this test would prove nothing")
	}
	for i := 0; i < 5; i++ {
		mustSnapshot(t, c)
	}
	if got := g.hits.Load(); got != first {
		t.Fatalf("cache went out again: %d hits after the first %d", got, first)
	}
	// Positive control: age the entry past the TTL and the next read does go out.
	c.mu.Lock()
	c.at = time.Now().Add(-cliReleaseTTL - time.Minute)
	c.mu.Unlock()
	mustSnapshot(t, c)
	if g.hits.Load() <= first {
		t.Fatal("an expired entry was not refetched")
	}
}

func TestCLIReleaseCacheKeepsPreviousValueWhenGitHubFails(t *testing.T) {
	g := newFakeGitHub(t, healthyBody, []string{tested("claude", "2.1.265")})
	c := cacheFor(g)
	mustSnapshot(t, c)

	g.status = http.StatusForbidden
	c.mu.Lock()
	c.at = time.Now().Add(-cliReleaseTTL - time.Minute)
	c.mu.Unlock()
	got := mustSnapshot(t, c)
	if got.Tested["claude"] != "2.1.265" || got.WatcherOkAt != "2026-09-11T05:30:12Z" {
		t.Fatalf("a failed refresh threw the previous answer away: %+v", got)
	}
	// And it must not hammer GitHub on every settings read while the budget is spent.
	hits := g.hits.Load()
	mustSnapshot(t, c)
	if g.hits.Load() != hits {
		t.Fatal("a failed refresh did not reset the TTL; the next read went straight out again")
	}
}

// Never read at all is not "fine" and not "broken": the block is absent entirely, so the
// Console has nothing to draw.
func TestCLIReleaseUnknownUntilFirstAnswer(t *testing.T) {
	c := newCLIReleaseCache("http://127.0.0.1:1")
	c.spawn = func(func()) {} // nothing has run yet
	if got := c.snapshot(); got != nil {
		t.Fatalf("want nil before the first answer, got %+v", got)
	}
}

// --- pagination ----------------------------------------------------------------------

// Only the last cliReleaseCommentPages pages are read, and they are read oldest-first so
// last-wins lands on the newest marker.
func TestCLIReleaseReadsOnlyTheLastCommentPages(t *testing.T) {
	pages := make([][]string, cliReleaseCommentPages+2)
	for i := range pages {
		page := make([]string, cliReleasePerPage)
		for j := range page {
			page[j] = tested("claude", fmt.Sprintf("1.%d.%d", i, j))
		}
		pages[i] = page
	}
	g := newFakeGitHub(t, healthyBody, pages...)
	got := mustSnapshot(t, cacheFor(g))
	want := fmt.Sprintf("1.%d.%d", len(pages)-1, cliReleasePerPage-1)
	if got.Tested["claude"] != want {
		t.Fatalf("tested claude=%q, want the newest marker %q", got.Tested["claude"], want)
	}
	// 1 issue list + cliReleaseCommentPages comment pages, not one request per page.
	if want := int64(1 + cliReleaseCommentPages); g.hits.Load() != want {
		t.Fatalf("%d requests, want %d", g.hits.Load(), want)
	}
}

// --- the wire ------------------------------------------------------------------------

// The four places a field has to exist for the Console to see it are the Go type, its
// json tags, the response that carries it and the Console's reader. This covers the
// middle two end to end, through the real route table: a key that is not written here is
// dropped silently, and neither the Console's tests nor its typecheck would notice.
func TestWSSettingsCarriesCLIRelease(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")

	// Unknown (a deployment with no route out): no key at all, not an empty object.
	if _, ok := getWSSettings(t, e)["cliRelease"]; ok {
		t.Fatal("cliRelease is present before anything was read; the Console would draw a broken watcher")
	}

	seedCLIRelease(t, &cliReleaseWire{
		WatcherOkAt:   "2026-09-11T05:30:12Z",
		WatcherFailed: []string{"rtk"},
		WatcherAt:     "2026-09-11T05:30:12Z",
		Tested:        map[string]string{"claude": "2.1.267"},
		FetchedAt:     "2026-09-11T06:00:00Z",
	})
	block, ok := getWSSettings(t, e)["cliRelease"].(map[string]any)
	if !ok {
		t.Fatal("cliRelease is missing from GET /api/env/ws-settings")
	}
	for _, k := range []string{"watcherOkAt", "watcherFailed", "watcherAt", "tested", "fetchedAt"} {
		if _, ok := block[k]; !ok {
			t.Errorf("cliRelease.%s is missing from the wire", k)
		}
	}
	if block["watcherOkAt"] != "2026-09-11T05:30:12Z" {
		t.Errorf("watcherOkAt: %v", block["watcherOkAt"])
	}
	rows, _ := block["watcherFailed"].([]any)
	if len(rows) != 1 || rows[0] != "rtk" {
		t.Errorf("watcherFailed: %v", block["watcherFailed"])
	}
	if m, _ := block["tested"].(map[string]any); m["claude"] != "2.1.267" {
		t.Errorf("tested: %v", block["tested"])
	}
}

// seedCLIRelease puts an answer in the package-wide cache and keeps the TTL from firing a
// refresh, restoring both afterwards so the next test starts from "never read".
func seedCLIRelease(t *testing.T, out *cliReleaseWire) {
	t.Helper()
	cliRelease.mu.Lock()
	prev, prevAt := cliRelease.out, cliRelease.at
	cliRelease.out, cliRelease.at = out, time.Now()
	cliRelease.mu.Unlock()
	t.Cleanup(func() {
		cliRelease.mu.Lock()
		cliRelease.out, cliRelease.at = prev, prevAt
		cliRelease.mu.Unlock()
	})
}

func getWSSettings(t *testing.T, e *previewHostEnv) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/env/ws-settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ws-settings: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ws-settings: %v (%s)", err, rec.Body.String())
	}
	return body
}

func mustSnapshot(t *testing.T, c *cliReleaseCache) *cliReleaseWire {
	t.Helper()
	got := c.snapshot()
	if got == nil {
		t.Fatal("snapshot is nil")
	}
	return got
}

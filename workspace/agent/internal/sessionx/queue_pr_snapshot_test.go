package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// snapshotFixture fakes the provider caches and the origin of every working copy.
type snapshotFixture struct {
	prs    map[branchpr.Key]*branchpr.PR // answered keys; a nil value answers "no PR"
	err    error
	asked  []branchpr.Key
	since  []time.Time
	origin originRepo
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &snapshotFixture{prs: map[branchpr.Key]*branchpr.PR{}, origin: originRepo{"github", "o/r"}}
	of, oo := prSnapshotFetch, originOf
	prSnapshotFetch = func(_ context.Context, _ string, keys []branchpr.Key, since time.Time) (map[branchpr.Key]*branchpr.PR, error) {
		f.asked = append(f.asked, keys...)
		f.since = append(f.since, since)
		if f.err != nil {
			return nil, f.err
		}
		out := map[branchpr.Key]*branchpr.PR{}
		for _, k := range keys {
			if pr, ok := f.prs[k]; ok {
				out[k] = pr
			}
		}
		return out, nil
	}
	originOf = func(string, time.Time) originRepo { return f.origin }
	t.Cleanup(func() { prSnapshotFetch, originOf = of, oo })
	return f
}

func ghPR(n int, state string) *branchpr.PR {
	return &branchpr.PR{Number: n, State: state, URL: "https://github.com/o/r/pull/" + itoaTest(n)}
}

func itoaTest(n int) string { b, _ := json.Marshal(n); return string(b) }

func postSnapshot(t *testing.T, body any) (*httptest.ResponseRecorder, PRSnapshotResponse, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	HandleQueuePRSnapshot(rec, httptest.NewRequest("POST", "/memos/pr-snapshot", strings.NewReader(string(b))))
	if rec.Header().Get(prSnapshotHeader) != "1" {
		t.Fatalf("answer without the route's mark: %d %s", rec.Code, rec.Body.String())
	}
	var out PRSnapshotResponse
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
	}
	return rec, out, e.Error.Code
}

const snapshotSince = "2026-11-19T10:00:00.000000Z"

// A first read is the branch's PR and the PR links held as started, created or working: never a
// reviewing or watching one, never an issue. Every answer is asked with the close as lower bound.
func TestPRSnapshotFirstReadIsTheSessionsPRs(t *testing.T) {
	f := newSnapshotFixture(t)
	session.WriteMeta(session.Meta{Name: "s", Kind: session.KindClaude, Dir: t.TempDir(), Branch: "feat", Links: []session.WorkItemLink{
		{Provider: "github", Key: "o/r#5", Role: session.LinkCreated, URL: "https://github.com/o/r/pull/5"},
		{Provider: "github", Key: "o/r#9", Role: session.LinkReviewing, URL: "https://github.com/o/r/pull/9"},
		{Provider: "github", Key: "o/r#3", Role: session.LinkWorking, URL: "https://github.com/o/r/issues/3"},
		{Provider: "github", Key: "o/r#4", Role: session.LinkWorking, URL: "https://github.com/o/r/pull/4", State: "open"},
	}})
	f.prs[branchpr.Key{Repo: "o/r", Branch: "feat"}] = ghPR(4, "open")
	f.prs[branchpr.Key{Repo: "o/r", Number: 5}] = ghPR(5, "merged")
	f.prs[branchpr.Key{Repo: "o/r", Number: 4}] = ghPR(4, "open")
	rec, out, _ := postSnapshot(t, PRSnapshotRequest{Session: "s", Since: snapshotSince})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := []string{}
	for _, p := range out.PRs {
		got = append(got, p.Key+"="+p.State)
	}
	if strings.Join(got, ",") != "o/r#4=open,o/r#5=merged" {
		t.Fatalf("prs %v", got)
	}
	want, _ := time.Parse(time.RFC3339Nano, snapshotSince)
	for _, s := range f.since {
		if !s.Equal(want) {
			t.Fatalf("asked with since %v, want the close", s)
		}
	}
}

// A branch the provider answered with no PR, and no link, is "no PR": an empty list, never null.
func TestPRSnapshotNoPRIsAnEmptyList(t *testing.T) {
	f := newSnapshotFixture(t)
	session.WriteMeta(session.Meta{Name: "s", Kind: session.KindClaude, Dir: t.TempDir(), Branch: "feat"})
	f.prs[branchpr.Key{Repo: "o/r", Branch: "feat"}] = nil
	rec, _, _ := postSnapshot(t, PRSnapshotRequest{Session: "s", Since: snapshotSince})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"prs":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Anything not answered for certain is an error, never "no PR".
func TestPRSnapshotUnknownIsNeverNoPR(t *testing.T) {
	f := newSnapshotFixture(t)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "s", Kind: session.KindClaude, Dir: dir, Branch: "feat"})
	// The provider left the branch unanswered.
	if rec, _, code := postSnapshot(t, PRSnapshotRequest{Session: "s", Since: snapshotSince}); rec.Code != http.StatusServiceUnavailable || code != prSnapshotUnanswered {
		t.Fatalf("unanswered: %d %s", rec.Code, rec.Body.String())
	}
	// Not connected, held off, or failed.
	f.err = errors.New("not connected")
	if rec, _, code := postSnapshot(t, PRSnapshotRequest{Session: "s", Since: snapshotSince}); rec.Code != http.StatusServiceUnavailable || code != prSnapshotUnanswered {
		t.Fatalf("provider error: %d %s", rec.Code, rec.Body.String())
	}
	// The working copy is gone: its branch PR is unknown.
	session.WriteMeta(session.Meta{Name: "s2", Kind: session.KindClaude, Dir: filepath.Join(dir, "gone"), Branch: "feat"})
	if rec, _, code := postSnapshot(t, PRSnapshotRequest{Session: "s2", Since: snapshotSince}); rec.Code != http.StatusServiceUnavailable || code != prSnapshotUnreadable {
		t.Fatalf("missing working copy: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _, code := postSnapshot(t, map[string]any{"session": "s"}); rec.Code != http.StatusBadRequest || code != prSnapshotBadRequest {
		t.Fatalf("no since: %d %s", rec.Code, rec.Body.String())
	}
}

// A session the Agent does not have answers no_session, the one answer that ends the wait. A read
// by PR number needs no session, and a PR the provider cannot show is unknown, not merged.
func TestPRSnapshotLaterReadsAskByNumber(t *testing.T) {
	f := newSnapshotFixture(t)
	if rec, _, code := postSnapshot(t, PRSnapshotRequest{Session: "nobody", Since: snapshotSince}); rec.Code != http.StatusNotFound || code != prSnapshotNoSession {
		t.Fatalf("absent session: %d %s", rec.Code, rec.Body.String())
	}
	f.prs[branchpr.Key{Repo: "o/r", Number: 4}] = ghPR(4, "merged")
	f.prs[branchpr.Key{Repo: "o/r", Number: 6}] = nil
	rec, out, _ := postSnapshot(t, PRSnapshotRequest{Session: "nobody", Since: snapshotSince,
		PRs: []PRSnapshotKey{{"github", "o/r#4"}, {"github", "o/r#6"}}})
	if rec.Code != http.StatusOK || len(out.PRs) != 2 || out.PRs[0].State != "merged" || out.PRs[1].State != "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec, _, code := postSnapshot(t, PRSnapshotRequest{Since: snapshotSince, PRs: []PRSnapshotKey{{"gitlab", "o/r#4"}}}); rec.Code != http.StatusBadRequest || code != prSnapshotBadRequest {
		t.Fatalf("another provider: %d %s", rec.Code, rec.Body.String())
	}
}

package sessionx

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

func pollWatches(t *testing.T, handler func(*httptest.ResponseRecorder), rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	handler(rec)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return body
}

// The transcript poll carries the session's live watches as a slim row, and nothing at all when
// there are none. A notify-parent watch shows on the parent, not the child.
func TestMessagesCarryLiveWatches(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	dir := t.TempDir()
	for _, m := range []session.Meta{
		{Name: "p", Dir: dir, Kind: session.KindClaude, Title: "t"},
		{Name: "c", Dir: dir, Kind: session.KindClaude, Title: "t", OriginSession: "p", Origin: session.OriginSession},
	} {
		n := m.Name
		session.WriteMeta(m)
		writeFile(t, filepath.Join(home, ".claude", "projects", "p", session.UUID(dir, n)+".jsonl"), promptRow("p1")+"\n")
	}
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}))
	poll := func(name string) map[string]json.RawMessage {
		req := httptest.NewRequest("GET", "/sessions/"+name+"/messages?since=1", nil)
		req.SetPathValue("name", name)
		return pollWatches(t, func(rec *httptest.ResponseRecorder) { HandleSessionMessages(rec, req) }, httptest.NewRecorder())
	}

	if _, has := poll("p")["watches"]; has {
		t.Fatal("a session without watches sent a watches key")
	}
	sub, err := watchEngine().Subscribe(t.Context(), watch.Request{Owner: "p", Requester: "c", Source: "github", Ref: "o/r#1",
		Types: []string{"pr.merged"}, Until: watch.UntilDeadline})
	if err != nil {
		t.Fatal(err)
	}
	raw, has := poll("p")["watches"]
	if !has {
		t.Fatal("the owner's poll has no watches")
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows = %s (%v)", raw, err)
	}
	r := rows[0]
	if r["id"] != sub.ID || r["source"] != "github" || r["ref"] != "o/r#1" || r["by"] != "c" {
		t.Fatalf("row = %v", r)
	}
	for _, internal := range []string{"owner", "subject", "told", "pending", "wake_id", "wake", "tag", "builtin", "notify"} {
		if _, leaked := r[internal]; leaked {
			t.Fatalf("row leaks internal field %q: %v", internal, r)
		}
	}
	if _, has := poll("c")["watches"]; has {
		t.Fatal("the child's poll shows its parent's watch")
	}
}

// The generic (non-claude) path answers with the same key.
func TestGenericMessagesCarryLiveWatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Cleanup(agentreg.SwapForTest(session.KindMuse, fakeApprovalAgent{}))
	m := session.Meta{Name: "slot-watches-wire", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged}
	session.WriteMeta(m)
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}))
	if _, err := watchEngine().Subscribe(t.Context(), watch.Request{Owner: m.Name, Source: "github", Ref: "o/r#1",
		Types: []string{"pr.merged"}, Until: watch.UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleGenericMessages(rec, httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil), m, true, "idle")
	if !strings.Contains(rec.Body.String(), `"watches":[{`) {
		t.Fatalf("no watches in %s", rec.Body.String())
	}
}

func TestWatchRowOfFormatsTimesAndNeverNullsTypes(t *testing.T) {
	due := time.Date(2026, 11, 12, 3, 4, 5, 0, time.FixedZone("x", 9*3600))
	row := watchRowOf(watch.Listed{Subscription: watch.Subscription{ID: "w", Due: due, Expires: due.Add(time.Hour)}, LastErr: "boom"})
	if row.Due != "2026-11-11T18:04:05Z" || row.Expires != "2026-11-11T19:04:05Z" || row.Error != "boom" || row.Types == nil {
		t.Fatalf("row = %+v", row)
	}
	if r := watchRowOf(watch.Listed{}); r.Due != "" || r.Expires != "" {
		t.Fatalf("zero times formatted: %+v", r)
	}
}

// A provider error can be arbitrarily long; the poll carries a bounded, valid-UTF-8 copy.
func TestWatchRowOfBoundsALongError(t *testing.T) {
	long := strings.Repeat("あ", 5000) + "\n" + strings.Repeat("x", 5000)
	row := watchRowOf(watch.Listed{Subscription: watch.Subscription{ID: "w"}, LastErr: long})
	if n := utf8.RuneCountInString(row.Error); n != watchErrorRunes || !utf8.ValidString(row.Error) || !strings.HasSuffix(row.Error, "…") {
		t.Fatalf("error runes = %d valid=%v: %q", n, utf8.ValidString(row.Error), row.Error)
	}
	if got := watchRowOf(watch.Listed{LastErr: "HTTP 502\n  bad gateway"}).Error; got != "HTTP 502 bad gateway" {
		t.Fatalf("short error = %q", got)
	}
}

// browser_handoff_ledger_test.go exercises ledger record/resolve/deliver from package main.
//
// The implementation lives in internal/browserx (ADR 0067 WP-A3 / AG-BROWSER); only the tests
// stay here, because the delivery stage runs through mcp_stdio.go's agentSendToSession (check
// the state, POST /input, and resume a stopped session before re-sending). browserx borrows
// that through a function variable, and the wiring is done by package main's
// alias_browser.go init, so this package's test binary is the only place the REAL wiring can
// be driven. Move these to browserx and swap in a double, and a broken agentSendToSession
// would still leave them green.
//
// The two tests that depend on browserx-internal fakes (newFakeBrowserCDP /
// fakeAttachmentManager) cannot move, so they stay in
// internal/browserx/browser_handoff_ledger_test.go.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
)

// stubAgentInputServer fakes the loopback Agent REST that agentSendToSession
// talks to: alive+ready status, and an /input endpoint that records the prompt
// it received. Safe for concurrent hits (browserx.DeliverBrowserHandoff runs on its own
// goroutine).
func stubAgentInputServer(t *testing.T) (prompts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"alive":true,"ready":true}`))
		case strings.HasSuffix(r.URL.Path, "/input"):
			var body struct {
				Prompt string `json:"prompt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			got = append(got, body.Prompt)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"sent":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func waitForBrowserHandoff(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestBrowserHandoffLedgerRecordResolveDeliverRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sess = "test-sess-1"

	browserx.RecordBrowserHandoffRequested(sess, "ba_1", "確認してください")
	row, ok := browserx.ResolveBrowserHandoff(sess, "ba_1", "completed")
	if !ok {
		t.Fatal("browserx.ResolveBrowserHandoff did not find the pending row")
	}
	if row.Result != "completed" || row.Message != "確認してください" {
		t.Fatalf("resolved row = %+v", row)
	}

	// A second resolve for the same attachment finds nothing — the row is no
	// longer open (Result != "").
	if _, ok := browserx.ResolveBrowserHandoff(sess, "ba_1", "cancelled"); ok {
		t.Fatal("resolving an already-resolved row must fail, not silently overwrite it")
	}

	l, ok := browserx.BrowserHandoffLedgers.Read(sess)
	if !ok || len(l.Rows) != 1 {
		t.Fatalf("ledger after resolve = %+v ok=%v, want exactly one row still on disk", l, ok)
	}

	browserx.MarkBrowserHandoffDelivered(sess, row.ID)
	if _, ok := browserx.BrowserHandoffLedgers.Read(sess); ok {
		t.Fatal("the ledger file should be removed once its only row is delivered")
	}
}

func TestRecordBrowserHandoffRequestedIgnoresMissingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No session context (a handoff started without request_browser_action's
	// session, or a bare API call) — must not create a file, and must not panic.
	browserx.RecordBrowserHandoffRequested("", "ba_1", "msg")
	if _, ok := browserx.ResolveBrowserHandoff("", "ba_1", "completed"); ok {
		t.Fatal("resolving with no session should never succeed")
	}
}

func TestDeliverBrowserHandoffInjectsResultAsSessionInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prompts := stubAgentInputServer(t)

	row, _ := func() (browserx.BrowserHandoffRow, bool) {
		browserx.RecordBrowserHandoffRequested("test-sess-2", "ba_9", "予約投稿を確認して")
		return browserx.ResolveBrowserHandoff("test-sess-2", "ba_9", "completed")
	}()

	browserx.DeliverBrowserHandoff("test-sess-2", row)

	got := prompts()
	if len(got) != 1 {
		t.Fatalf("prompts delivered = %v, want exactly one", got)
	}
	if !strings.Contains(got[0], "予約投稿を確認して") || !strings.Contains(got[0], "完了") || !strings.Contains(got[0], "ba_9") {
		t.Fatalf("delivered prompt = %q, want it to carry the message, the verdict, and the attachment id", got[0])
	}
	if _, ok := browserx.BrowserHandoffLedgers.Read("test-sess-2"); ok {
		t.Fatal("a successfully delivered row must be removed from the ledger")
	}
}

func TestSweepUndeliveredBrowserHandoffsRetriesAfterARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prompts := stubAgentInputServer(t)

	// Simulate what a crash between browserx.ResolveBrowserHandoff and browserx.DeliverBrowserHandoff
	// leaves behind: a resolved row already on disk, nothing delivered yet.
	browserx.RecordBrowserHandoffRequested("test-sess-3", "ba_7", "レビューして")
	if _, ok := browserx.ResolveBrowserHandoff("test-sess-3", "ba_7", "cancelled"); !ok {
		t.Fatal("setup: resolve should have found the row")
	}

	browserx.SweepUndeliveredBrowserHandoffs()

	waitForBrowserHandoff(t, func() bool { return len(prompts()) == 1 })
	got := prompts()[0]
	if !strings.Contains(got, "キャンセル") || !strings.Contains(got, "ba_7") {
		t.Fatalf("swept delivery prompt = %q", got)
	}
	waitForBrowserHandoff(t, func() bool {
		_, ok := browserx.BrowserHandoffLedgers.Read("test-sess-3")
		return !ok
	})
}

func TestSweepUndeliveredBrowserHandoffsSkipsStillPendingRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prompts := stubAgentInputServer(t)

	// No human has responded yet (Result == "") — nothing to deliver, and the
	// row must survive the sweep untouched.
	browserx.RecordBrowserHandoffRequested("test-sess-4", "ba_5", "確認して")

	browserx.SweepUndeliveredBrowserHandoffs()
	time.Sleep(50 * time.Millisecond) // let any (wrongly) spawned goroutine run

	if got := prompts(); len(got) != 0 {
		t.Fatalf("a still-pending row must not be delivered, got %v", got)
	}
	l, ok := browserx.BrowserHandoffLedgers.Read("test-sess-4")
	if !ok || len(l.Rows) != 1 || l.Rows[0].Result != "" {
		t.Fatalf("pending row was disturbed by the sweep: %+v ok=%v", l, ok)
	}
}

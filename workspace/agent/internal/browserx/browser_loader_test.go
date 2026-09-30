package browserx

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// browserStateRecord is one state the page was seen in, with how many
// held documents the test server had released when it arrived.
type browserStateRecord struct {
	state  string
	served int64
}

// TestBrowserReadyWaitsForTheNavigationsOwnLoader holds every document response
// for 1.5 s and records each state transition against the number of documents
// released so far. A "ready" recorded before the navigation's document was
// served came from another loader: the target's initial about:blank (its
// networkIdle lands ~0.5 s after creation) or the previous document, whose
// network goes idle while the next navigation is still pending.
func TestBrowserReadyWaitsForTheNavigationsOwnLoader(t *testing.T) {
	factory := browserTestCDPFactory(t)
	const hold = 1500 * time.Millisecond
	var served atomic.Int64
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/slow":
			time.Sleep(hold)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			served.Add(1)
			_, _ = w.Write([]byte(`<!doctype html><title>held</title><a href="/slow">next</a>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer app.Close()
	u, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	m := NewBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Minute, ChromiumIdle: time.Minute,
		CommandTimeout: 10 * time.Second, FrameInterval: time.Second / 12, JPEGQuality: 70,
		CDPFactory: factory,
	})
	defer m.Close()
	// Page.navigate returns only once the document commits, so the early
	// "ready" happens inside Create: record by polling the page from the moment
	// Create registers it, not through a viewer attached afterwards.
	var recMu sync.Mutex
	var records []browserStateRecord
	stop := make(chan struct{})
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		last := ""
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			m.mu.Lock()
			var p *browserPage
			for _, page := range m.pages {
				p = page
			}
			m.mu.Unlock()
			if p == nil {
				continue
			}
			if state := p.response().State; state != last {
				last = state
				recMu.Lock()
				records = append(records, browserStateRecord{state: state, served: served.Load()})
				recMu.Unlock()
			}
		}
	}()
	defer func() { close(stop); <-polled }()
	created, err := m.Create(browserCreateRequest{
		Port: port, Path: "/", Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	cdp := m.cdp
	m.mu.Unlock()
	snapshot := func() []browserStateRecord {
		recMu.Lock()
		defer recMu.Unlock()
		return append([]browserStateRecord(nil), records...)
	}
	evaluate := func(expr string) {
		t.Helper()
		if err := m.call(cdp, p.sessionID, "Runtime.evaluate", map[string]any{"expression": expr}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// expectReady waits for the step's navigation to reach "ready" with its own
	// document served, and fails on any "ready" recorded before that document.
	expectReady := func(step string, from int, want int64) int {
		t.Helper()
		if !waitFor(10*time.Second, func() bool {
			return served.Load() >= want && p.response().State == "ready"
		}) {
			t.Fatalf("%s: never reached ready with document %d served: %+v (page %+v)", step, want, snapshot()[from:], p.response())
		}
		time.Sleep(20 * time.Millisecond) // let the poller record the last transition
		recs := snapshot()
		for _, r := range recs[from:] {
			if r.state == "ready" && r.served < want {
				t.Errorf("%s: ready before its document was served (served %d of %d): %+v", step, r.served, want, recs[from:])
			}
		}
		return len(recs)
	}

	next := expectReady("first navigation", 0, 1)

	evaluate(`document.querySelector('a').click()`)
	next = expectReady("link click", next, 2)

	if err := m.call(cdp, p.sessionID, "Page.reload", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	next = expectReady("reload", next, 3)

	// A same-document navigation commits no new loader and must not leave the
	// page loading.
	evaluate(`history.pushState({}, '', '/pushed'); location.hash = 'h'`)
	time.Sleep(300 * time.Millisecond)
	if got := p.response().State; got != "ready" {
		t.Fatalf("same-document navigation left state %q: %+v", got, snapshot()[next:])
	}
	next = len(snapshot())

	// A navigation that fails must end, not stay loading.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	evaluate(`location.href = 'http://127.0.0.1:` + strconv.Itoa(dead) + `/'`)
	if !waitFor(10*time.Second, func() bool {
		for _, r := range snapshot()[next:] {
			if r.state == "target-unreachable" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("failed navigation never reported target-unreachable: %+v (page %+v)", snapshot()[next:], p.response())
	}
	if !waitFor(10*time.Second, func() bool { return p.response().State != "loading" }) {
		t.Fatalf("failed navigation left the page loading: %+v", snapshot()[next:])
	}
}

// TestBrowserLoadedStateFollowsTheTrackedLoader drives the same rules with
// synthetic events, including the ones Chromium did not produce on demand: an
// old-loader loadEventFired, a same-document start, and a navigation that ends
// with only the main frame stopping.
func TestBrowserLoadedStateFollowsTheTrackedLoader(t *testing.T) {
	cdp := newFakeBrowserCDP()
	m := fakeBrowserManager(cdp)
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()
	event := func(method, params string) {
		m.handleEvent(cdp, browserCDPEvent{Method: method, SessionID: p.sessionID, Params: json.RawMessage(params)})
	}
	expect := func(step, want string) {
		t.Helper()
		if got := p.response().State; got != want {
			t.Fatalf("%s: state %q, want %q", step, got, want)
		}
	}
	startDocument := func(networkID string) {
		m.handleRequestPaused(cdp, p, json.RawMessage(`{"requestId":"r-`+networkID+`","networkId":"`+networkID+`","frameId":"frame-1","resourceType":"Document","request":{"url":"http://127.0.0.1:3000/"}}`))
	}

	startDocument("L1")
	expect("document request", "loading")
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"blank","name":"networkIdle"}`)
	expect("about:blank networkIdle before any loader is known", "loading")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L1","url":"http://127.0.0.1:3000/","navigationType":"differentDocument"}`)
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"blank","name":"networkIdle"}`)
	event("Page.loadEventFired", `{"timestamp":1}`)
	expect("old loader's networkIdle and loadEventFired", "loading")
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"L1","name":"load"}`)
	expect("tracked loader's load", "ready")

	startDocument("L2")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L2","url":"http://127.0.0.1:3000/next","navigationType":"differentDocument"}`)
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"L1","name":"networkIdle"}`)
	expect("previous document's networkIdle", "loading")
	event("Page.frameNavigated", `{"frame":{"id":"frame-1","loaderId":"L2","url":"http://127.0.0.1:3000/next"}}`)
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"S1","url":"http://127.0.0.1:3000/next#h","navigationType":"sameDocument"}`)
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"L2","name":"networkIdle"}`)
	expect("committed loader's networkIdle after a same-document start", "ready")

	startDocument("L3")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L3","url":"http://127.0.0.1:3000/gone","navigationType":"differentDocument"}`)
	event("Page.frameStoppedLoading", `{"frameId":"sub"}`)
	expect("a subframe stopping", "loading")
	event("Page.frameStoppedLoading", `{"frameId":"frame-1"}`)
	expect("main frame stopped without a commit", "ready")
}

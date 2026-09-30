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
		case "/no-content":
			w.WriteHeader(http.StatusNoContent)
		case "/download":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="x.bin"`)
			_, _ = w.Write([]byte("x"))
		case "/stalled":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
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

	// A top-level navigation that is aborted without committing leaves the
	// previous document live: it must end "ready", never "target-unreachable".
	aborted := func(step, expr string) {
		t.Helper()
		from := len(snapshot())
		evaluate(expr)
		if !waitFor(10*time.Second, func() bool {
			for _, r := range snapshot()[from:] {
				if r.state == "loading" {
					return p.response().State != "loading"
				}
			}
			return false
		}) {
			t.Fatalf("%s: never left loading: %+v (page %+v)", step, snapshot()[from:], p.response())
		}
		time.Sleep(300 * time.Millisecond) // a late event must not flip it either
		for _, r := range snapshot()[from:] {
			if r.state == "target-unreachable" {
				t.Errorf("%s: reported target-unreachable although the document is still live: %+v", step, snapshot()[from:])
				break
			}
		}
		if got := p.response().State; got != "ready" {
			t.Errorf("%s: state %q, want ready", step, got)
		}
	}
	aborted("204 No Content", `location.href = '/no-content'`)
	aborted("denied download", `location.href = '/download'`)
	aborted("window.stop()", `location.href = '/stalled'; setTimeout(() => window.stop(), 300)`)
	from := len(snapshot())
	evaluate(`document.querySelector('a').click()`)
	next = expectReady("link click after the aborted navigations", from, served.Load()+1)

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
	// The error page's commit, its load and the aborted re-navigation that its
	// chrome-error:// URL provokes (measured: ~0.3 s) must all keep it.
	time.Sleep(time.Second)
	if got := p.response().State; got != "target-unreachable" {
		t.Fatalf("error page settled in state %q, want target-unreachable: %+v", got, snapshot()[next:])
	}

	// Going back leaves the error page for a live document; an aborted
	// navigation from there returns to that document, not to the error page.
	// Runtime.evaluate does not answer on the error page; go back through CDP.
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := m.call(cdp, p.sessionID, "Page.getNavigationHistory", map[string]any{}, &history); err != nil {
		t.Fatal(err)
	}
	if history.CurrentIndex < 1 {
		t.Fatalf("no entry to go back to: %+v", history)
	}
	if err := m.call(cdp, p.sessionID, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[history.CurrentIndex-1].ID}, nil); err != nil {
		t.Fatal(err)
	}
	if !waitFor(10*time.Second, func() bool { return p.response().State == "ready" }) {
		t.Fatalf("going back from the error page never reached ready: %+v (page %+v)", snapshot()[next:], p.response())
	}
	aborted("204 No Content after going back", `location.href = '/no-content'`)
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

	loader := func() string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.loaderID
	}
	abort := func(networkID string) {
		event("Network.loadingFailed", `{"requestId":"`+networkID+`","type":"Document","errorText":"net::ERR_ABORTED","canceled":true}`)
	}
	startDocument("L4")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L4","url":"http://127.0.0.1:3000/204","navigationType":"differentDocument"}`)
	abort("L4")
	expect("aborted navigation", "ready")
	if got := loader(); got != "L2" {
		t.Fatalf("aborted navigation left loader %q tracked, want the committed L2", got)
	}

	startDocument("L5")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L5","url":"http://127.0.0.1:3000/a","navigationType":"differentDocument"}`)
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L6","url":"http://127.0.0.1:3000/b","navigationType":"differentDocument"}`)
	abort("L5")
	expect("navigation aborted by a newer one", "loading")
	if got := loader(); got != "L6" {
		t.Fatalf("superseded navigation's abort moved the loader to %q, want L6", got)
	}
	startDocument("L6")
	event("Page.frameNavigated", `{"frame":{"id":"frame-1","loaderId":"L6","url":"http://127.0.0.1:3000/b"}}`)
	event("Page.lifecycleEvent", `{"frameId":"frame-1","loaderId":"L6","name":"load"}`)
	expect("newer navigation's load", "ready")

	startDocument("L7")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L7","url":"http://127.0.0.1:3000/down","navigationType":"differentDocument"}`)
	event("Network.loadingFailed", `{"requestId":"L7","type":"Document","errorText":"net::ERR_CONNECTION_REFUSED"}`)
	expect("connection refused", "target-unreachable")
	event("Page.frameNavigated", `{"frame":{"id":"frame-1","loaderId":"L7","url":"chrome-error://chromewebdata/","unreachableUrl":"http://127.0.0.1:3000/down"}}`)
	startDocument("L8")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L8","url":"http://127.0.0.1:3000/204","navigationType":"differentDocument"}`)
	abort("L8")
	expect("aborted navigation over an error page", "target-unreachable")

	// Going back restores the healthy L6 document from the back/forward cache:
	// it commits with no document request and no response.
	event("Page.frameNavigated", `{"frame":{"id":"frame-1","loaderId":"L6","url":"http://127.0.0.1:3000/b"},"type":"BackForwardCacheRestore"}`)
	expect("document restored from the cache", "ready")
	startDocument("L9")
	event("Page.frameStartedNavigating", `{"frameId":"frame-1","loaderId":"L9","url":"http://127.0.0.1:3000/204","navigationType":"differentDocument"}`)
	abort("L9")
	expect("aborted navigation over a document restored from the cache", "ready")
}

// TestBrowserAbortedInitialNavigationIsReady opens pages on URLs whose
// navigation Chromium aborts without committing (a 204, a download). The tab
// stays on its about:blank, a live document, so the page must read ready as an
// aborted navigation later on does, never target-unreachable; navigating it
// afterwards must still work.
func TestBrowserAbortedInitialNavigationIsReady(t *testing.T) {
	factory := browserTestCDPFactory(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/no-content":
			w.WriteHeader(http.StatusNoContent)
		case "/download":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="x.bin"`)
			_, _ = w.Write([]byte("x"))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><title>live</title>`))
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
	for _, path := range []string{"/no-content", "/download"} {
		// Record every state from the moment Create registers the page: the
		// navigation's outcome lands inside Create.
		var recMu sync.Mutex
		var states []string
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
					states = append(states, state)
					recMu.Unlock()
				}
			}
		}()
		created, err := m.Create(browserCreateRequest{
			Port: port, Path: path, Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
		})
		if err != nil {
			close(stop)
			t.Fatal(err)
		}
		m.mu.Lock()
		p := m.pages[created.ID]
		cdp := m.cdp
		m.mu.Unlock()
		ok := waitFor(10*time.Second, func() bool { return p.response().State == "ready" })
		time.Sleep(300 * time.Millisecond) // a late event must not flip it either
		close(stop)
		<-polled
		recMu.Lock()
		seen := append([]string(nil), states...)
		recMu.Unlock()
		if !ok {
			t.Fatalf("%s: never reached ready: %v (page %+v)", path, seen, p.response())
		}
		for _, s := range seen {
			if s == "target-unreachable" {
				t.Errorf("%s: reported target-unreachable although about:blank is live: %v", path, seen)
				break
			}
		}
		if got := p.response(); got.State != "ready" || got.URL != app.URL+path {
			t.Errorf("%s: %+v, want ready at the requested URL", path, got)
		}

		if err := m.call(cdp, p.sessionID, "Page.navigate", map[string]any{"url": app.URL + "/after"}, nil); err != nil {
			t.Fatal(err)
		}
		if !waitFor(10*time.Second, func() bool {
			got := p.response()
			return got.State == "ready" && got.URL == app.URL+"/after"
		}) {
			t.Fatalf("%s: a navigation after the aborted one never reached ready: %+v", path, p.response())
		}
		m.Delete(created.ID)
	}
}

// TestBrowserInitialNavigationErrorText decides the initial state from
// Page.navigate's errorText alone: an abort leaves the live about:blank (ready),
// any other failure is target-unreachable.
func TestBrowserInitialNavigationErrorText(t *testing.T) {
	for _, tc := range []struct{ errorText, want string }{
		{"net::ERR_ABORTED", "ready"},
		{"net::ERR_CONNECTION_REFUSED", "target-unreachable"},
	} {
		cdp := newFakeBrowserCDP()
		cdp.navigateErrorText = tc.errorText
		m := fakeBrowserManager(cdp)
		created, err := m.Create(browserCreateRequest{Port: 3000, Path: "/no-content", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}})
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		p := m.pages[created.ID]
		m.mu.Unlock()
		if got := p.response(); got.State != tc.want || got.URL != "http://127.0.0.1:3000/no-content" {
			t.Errorf("%s: %+v, want %s at the requested URL", tc.errorText, got, tc.want)
		}
		m.Close()
	}
}

// TestBrowserAbortedInitialNavigationLeavesANewerOneAlone has the initial
// navigation L1 aborted by a newer one, L2, whose events the loop handles
// before Page.navigate answers. Whether L2 is still pending or already
// committed, the late ERR_ABORTED must not end it: the page stays loading on
// L2's request until L2's own load.
func TestBrowserAbortedInitialNavigationLeavesANewerOneAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []browserCDPEvent
	}{
		{"pending", []browserCDPEvent{
			{Method: "Page.frameStartedNavigating", Params: json.RawMessage(`{"frameId":"frame-1","loaderId":"L2","url":"http://127.0.0.1:3000/next","navigationType":"differentDocument"}`)},
			{Method: "Fetch.requestPaused", Params: json.RawMessage(`{"requestId":"r-L2","networkId":"L2","frameId":"frame-1","resourceType":"Document","request":{"url":"http://127.0.0.1:3000/next"}}`)},
		}},
		{"committed, not loaded", []browserCDPEvent{
			{Method: "Page.frameStartedNavigating", Params: json.RawMessage(`{"frameId":"frame-1","loaderId":"L2","url":"http://127.0.0.1:3000/next","navigationType":"differentDocument"}`)},
			{Method: "Fetch.requestPaused", Params: json.RawMessage(`{"requestId":"r-L2","networkId":"L2","frameId":"frame-1","resourceType":"Document","request":{"url":"http://127.0.0.1:3000/next"}}`)},
			{Method: "Page.frameNavigated", Params: json.RawMessage(`{"frame":{"id":"frame-1","loaderId":"L2","url":"http://127.0.0.1:3000/next"}}`)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cdp := newFakeBrowserCDP()
			cdp.navigateErrorText = "net::ERR_ABORTED"
			m := fakeBrowserManager(cdp)
			t.Cleanup(m.Close)
			var p *browserPage
			cdp.setOnCall("Page.navigate", func() {
				m.mu.Lock()
				for _, page := range m.pages {
					p = page
				}
				m.mu.Unlock()
				// The initial navigation's own start, then the newer one's.
				m.handleEvent(cdp, browserCDPEvent{Method: "Page.frameStartedNavigating", SessionID: p.sessionID, Params: json.RawMessage(`{"frameId":"frame-1","loaderId":"L1","url":"http://127.0.0.1:3000/","navigationType":"differentDocument"}`)})
				for _, ev := range tc.events {
					ev.SessionID = p.sessionID
					m.handleEvent(cdp, ev)
				}
			})
			if _, err := m.Create(browserCreateRequest{Port: 3000, Path: "/", Viewport: browserViewportRequest{Width: 900, Height: 600, DeviceScaleFactor: 1}}); err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			state, loader, top := p.state, p.loaderID, p.topRequestID
			p.mu.Unlock()
			if state != "loading" || loader != "L2" || top != "L2" {
				t.Fatalf("after the initial abort: state=%q loader=%q topRequest=%q, want loading on L2", state, loader, top)
			}
			m.handleEvent(cdp, browserCDPEvent{Method: "Page.lifecycleEvent", SessionID: p.sessionID, Params: json.RawMessage(`{"frameId":"frame-1","loaderId":"L2","name":"load"}`)})
			if got := p.response().State; got != "ready" {
				t.Fatalf("L2's load: state %q, want ready", got)
			}
		})
	}
}

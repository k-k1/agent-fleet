package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
)

// captureObservationLog redirects the standard logger into a buffer and clears
// the duplicate-suppression state so each test observes from a clean slate.
func captureObservationLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	codexObservedMu.Lock()
	codexObservedLast = map[string]string{}
	codexObservedMu.Unlock()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestConnectCodexAppServerIntegration(t *testing.T) {
	addr := os.Getenv("AF_TEST_CODEX_APP_SERVER_ADDR")
	if addr == "" {
		t.Skip("set AF_TEST_CODEX_APP_SERVER_ADDR to run against a real Codex app-server")
	}
	conn, err := connectCodexAppServer(addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// End-to-end against a real app-server: the AF observer must attach to a thread
// another connection (standing in for the TUI) is driving, and see its
// contextCompaction begin. Needs a server whose CODEX_HOME contains a rollout for
// the given thread; the compact turn itself may fail (no auth) — item/started
// still fires first.
func TestCodexObserverLiveIntegration(t *testing.T) {
	addr := os.Getenv("AF_TEST_CODEX_APP_SERVER_ADDR")
	tid := os.Getenv("AF_TEST_CODEX_THREAD_ID")
	if addr == "" || tid == "" {
		t.Skip("set AF_TEST_CODEX_APP_SERVER_ADDR and AF_TEST_CODEX_THREAD_ID to run against a real Codex app-server")
	}
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)

	conn, err := connectCodexAppServer(addr)
	if err != nil {
		t.Fatal(err)
	}
	go observeCodexAppServer(conn)

	// Second connection plays the TUI: resume the thread and start a compaction.
	tui, err := connectCodexAppServer(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tui.Close()
	rpc := func(id int, method string, params map[string]any) {
		t.Helper()
		if err := tui.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			_ = tui.SetReadDeadline(deadline)
			var m map[string]any
			if err := tui.ReadJSON(&m); err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			if got, ok := m["id"].(float64); ok && int(got) == id {
				if m["error"] != nil {
					t.Fatalf("%s: %v", method, m["error"])
				}
				return
			}
		}
	}
	rpc(2, "thread/resume", map[string]any{"threadId": tid})
	// Give the observer a beat to attach (its sweep runs immediately on start).
	time.Sleep(time.Second)
	rpc(3, "thread/compact/start", map[string]any{"threadId": tid})

	deadline := time.Now().Add(10 * time.Second)
	for !codex.IsCompactingThread(tid) {
		if time.Now().After(deadline) {
			t.Fatal("observer did not see the TUI-driven compaction start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartCodexAppServerDisabledClearsRemoteAddress(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	t.Setenv(codexAppServerEnv, "ws://127.0.0.1:1")
	startCodexAppServer()
	if got := os.Getenv(codexAppServerEnv); got != "" {
		t.Fatalf("app-server address = %q; want empty in disabled mode", got)
	}
}

func TestHandleCodexAppServerCompactionLifecycle(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)

	handleCodexAppServerEvent([]byte(`{
      "method":"item/started",
      "params":{"threadId":"thr-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"item-1"}}
    }`))
	if !codex.IsCompactingThread("thr-1") {
		t.Fatal("contextCompaction item/started did not set compacting")
	}

	handleCodexAppServerEvent([]byte(`{
      "method":"item/completed",
      "params":{"threadId":"thr-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"item-1"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("contextCompaction item/completed did not clear compacting")
	}
}

func TestHandleCodexAppServerTurnCompletedClearsStuckCompaction(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)
	codex.SetCompacting("thr-1", true)

	handleCodexAppServerEvent([]byte(`{
      "method":"turn/completed",
      "params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"failed"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("turn/completed did not clear compacting")
	}
}

// Payload shape mirrors a live account/rateLimits/read response (CLI 0.144.4):
// weekly window in primary, secondary null, epoch-seconds resetsAt.
func TestHandleCodexAppServerRateLimitsUpdated(t *testing.T) {
	buf := captureObservationLog(t)
	ev := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":93,"windowDurationMins":10080,"resetsAt":1784669818},"secondary":null,"planType":"plus","rateLimitReachedType":null}}}`
	handleCodexAppServerEvent([]byte(ev))
	want := "account/rateLimits/updated primary=93%/10080m resets=2026-07-21T21:36:58Z secondary=- plan=plus reached=-"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}

	// An identical reading must not repeat the line; a changed one must.
	handleCodexAppServerEvent([]byte(ev))
	if got := strings.Count(buf.String(), "account/rateLimits/updated"); got != 1 {
		t.Fatalf("duplicate reading logged %d times, want 1", got)
	}
	handleCodexAppServerEvent([]byte(strings.Replace(ev, `"usedPercent":93`, `"usedPercent":94`, 1)))
	if got := strings.Count(buf.String(), "account/rateLimits/updated"); got != 2 {
		t.Fatalf("changed reading logged %d times, want 2", got)
	}
}

func TestHandleCodexAppServerModelReroutedAlwaysLogs(t *testing.T) {
	buf := captureObservationLog(t)
	ev := `{"method":"model/rerouted","params":{"threadId":"thr-r","turnId":"turn-9","fromModel":"gpt-5.6-sol","toModel":"gpt-5.4-mini","reason":"highRiskCyberActivity"}}`
	handleCodexAppServerEvent([]byte(ev))
	handleCodexAppServerEvent([]byte(ev))
	want := "model/rerouted thread=thr-r turn=turn-9 from=gpt-5.6-sol to=gpt-5.4-mini reason=highRiskCyberActivity"
	if got := strings.Count(buf.String(), want); got != 2 {
		t.Fatalf("model/rerouted logged %d times, want 2 (never deduplicated); log = %q", got, buf.String())
	}
}

func TestHandleCodexAppServerThreadSettingsUpdated(t *testing.T) {
	buf := captureObservationLog(t)
	ev := func(model string) string {
		return `{"method":"thread/settings/updated","params":{"threadId":"thr-s","threadSettings":{"model":"` + model + `","modelProvider":"openai","effort":"high","collaborationMode":{"mode":"default","settings":{}},"cwd":"/w","approvalPolicy":"never","approvalsReviewer":"user","sandboxPolicy":{"mode":"danger-full-access"}}}}`
	}
	handleCodexAppServerEvent([]byte(ev("gpt-5.6-sol")))
	if want := "thread/settings/updated thread=thr-s model=gpt-5.6-sol effort=high mode=default"; !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "(prev") {
		t.Fatalf("first observation must not carry a prev clause: %q", buf.String())
	}

	handleCodexAppServerEvent([]byte(ev("gpt-5.6-sol")))
	if got := strings.Count(buf.String(), "thread/settings/updated"); got != 1 {
		t.Fatalf("unchanged settings logged %d times, want 1", got)
	}

	// The nudge-acceptance signature: a model change with no model/rerouted around it.
	handleCodexAppServerEvent([]byte(ev("gpt-5.4-mini")))
	want := "model=gpt-5.4-mini effort=high mode=default (prev model=gpt-5.6-sol effort=high mode=default)"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}
}

func TestHandleCodexAppServerWarning(t *testing.T) {
	buf := captureObservationLog(t)
	handleCodexAppServerEvent([]byte(`{"method":"warning","params":{"message":"model unavailable","threadId":null}}`))
	if want := `warning thread=- message="model unavailable"`; !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}
}

func TestHandleCodexAppServerThreadStatusChanged(t *testing.T) {
	buf := captureObservationLog(t)
	active := `{"method":"thread/status/changed","params":{"threadId":"thr-st","status":{"type":"active","activeFlags":["waitingOnUserInput"]}}}`
	handleCodexAppServerEvent([]byte(active))
	if want := "thread/status/changed thread=thr-st status=active[waitingOnUserInput]"; !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}
	handleCodexAppServerEvent([]byte(active))
	if got := strings.Count(buf.String(), "thread/status/changed"); got != 1 {
		t.Fatalf("unchanged status logged %d times, want 1", got)
	}
	handleCodexAppServerEvent([]byte(`{"method":"thread/status/changed","params":{"threadId":"thr-st","status":{"type":"idle"}}}`))
	if want := "thread/status/changed thread=thr-st status=idle"; !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want contains %q", buf.String(), want)
	}
}

func TestHandleCodexAppServerIgnoresOtherItems(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)
	handleCodexAppServerEvent([]byte(`{
      "method":"item/started",
      "params":{"threadId":"thr-1","item":{"type":"commandExecution","id":"item-1"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("non-compaction item changed compacting state")
	}
}

// The app-server delivers thread-scoped events only to connections that have the
// thread loaded, so the monitor must attach with thread/resume — from both the
// thread/started broadcast and the thread/loaded/list sweep — before compaction
// (or any other thread event) can be observed. This drives monitorCodexAppServer
// against a scripted app-server and checks that full loop.
func TestCodexObserverAttachesBeforeObservingThreadEvents(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)
	captureObservationLog(t) // silence + isolate the observation log

	resumed := make(chan string, 8)
	wsConns := make(chan *websocket.Conn, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wsConns <- c
		defer c.Close()
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			switch m["method"] {
			case "initialize":
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
			case "initialized":
				// A thread is already running when the observer arrives; it is
				// announced but its events are withheld until the observer resumes.
				_ = c.WriteJSON(map[string]any{"method": "thread/started",
					"params": map[string]any{"thread": map[string]any{"id": "thr-live"}}})
			case "thread/loaded/list":
				_ = c.WriteJSON(map[string]any{"id": m["id"],
					"result": map[string]any{"data": []string{"thr-live"}, "nextCursor": nil}})
			case "thread/resume":
				tid, _ := m["params"].(map[string]any)["threadId"].(string)
				resumed <- tid
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
				// Attachment unlocks thread-scoped delivery.
				_ = c.WriteJSON(map[string]any{"method": "item/started",
					"params": map[string]any{"threadId": tid, "item": map[string]any{"type": "contextCompaction", "id": "i1"}}})
			}
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	conn, err := connectCodexAppServer(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	go observeCodexAppServer(conn)

	select {
	case tid := <-resumed:
		if tid != "thr-live" {
			t.Fatalf("observer resumed thread %q, want thr-live", tid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observer never sent thread/resume")
	}
	// thread/started broadcast and loaded/list sweep race for the same thread;
	// the attach set must collapse them into a single resume.
	select {
	case tid := <-resumed:
		t.Fatalf("duplicate thread/resume for %q", tid)
	case <-time.After(300 * time.Millisecond):
	}

	deadline := time.Now().Add(3 * time.Second)
	for !codex.IsCompactingThread("thr-live") {
		if time.Now().After(deadline) {
			t.Fatal("compaction event after attach was not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Tear down inside the test so the monitor's disconnect handling
	// (ClearCompacting + log line) cannot bleed into a later test. Close the
	// listener first (reconnects fail), then the hijacked websocket conns, which
	// httptest's Close does not touch.
	srv.Close()
	for {
		select {
		case c := <-wsConns:
			_ = c.Close()
			continue
		default:
		}
		break
	}
	deadline = time.Now().Add(3 * time.Second)
	for codex.IsCompactingThread("thr-live") {
		if time.Now().After(deadline) {
			t.Fatal("disconnect did not clear compacting state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An unload — a notLoaded broadcast with no thread/closed, from a TUI disconnect or an idle
// eviction — must make requested forget the thread. If the entry survives, attach keeps
// returning early once the thread is reloaded, and observation of it stays dead until the
// observing socket reconnects.
func TestCodexObserverForgetsUnloadedThread(t *testing.T) {
	obs := newCodexObserver(nil) // the forget path never touches conn
	obs.requested["thr-unload"] = true
	obs.observeThreadLifecycle(codexAppServerMessage{
		Method: "thread/status/changed",
		Params: []byte(`{"threadId":"thr-unload","status":{"type":"notLoaded"}}`),
	})
	if obs.requested["thr-unload"] {
		t.Fatal("notLoaded must forget the thread so a later reload can re-attach")
	}
}

// A Terminal launch resuming a thread the shared app-server holds needs the server to unload
// it, and the observer is the subscriber that otherwise keeps it loaded forever. Released, the
// observer must unsubscribe, stay off the thread through sweeps and status broadcasts, and
// attach again only once the unload (notLoaded) has been seen.
func TestCodexObserverReleasesThreadUntilUnloaded(t *testing.T) {
	captureObservationLog(t)
	releasedFile := filepath.Join(t.TempDir(), "released.json")
	prevFile := codexReleasedFile
	codexReleasedFile = func() string { return releasedFile }
	t.Cleanup(func() { clearCodexReleased("thr-held"); codexReleasedFile = prevFile })
	var loadedMu sync.Mutex
	loaded := true // whether the scripted server still lists thr-held

	calls := make(chan string, 16) // "<method> <threadId>" for resume/unsubscribe
	push := make(chan map[string]any, 4)
	wsConns := make(chan *websocket.Conn, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wsConns <- c
		defer c.Close()
		var wmu sync.Mutex // the push goroutine and the reply loop share the socket
		write := func(v any) {
			wmu.Lock()
			defer wmu.Unlock()
			_ = c.WriteJSON(v)
		}
		go func() {
			for m := range push {
				write(m)
			}
		}()
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			switch m["method"] {
			case "initialize":
				write(map[string]any{"id": m["id"], "result": map[string]any{}})
			case "thread/loaded/list":
				loadedMu.Lock()
				data := []string{}
				if loaded {
					data = []string{"thr-held"}
				}
				loadedMu.Unlock()
				write(map[string]any{"id": m["id"],
					"result": map[string]any{"data": data, "nextCursor": nil}})
			case "thread/resume", "thread/unsubscribe":
				tid, _ := m["params"].(map[string]any)["threadId"].(string)
				calls <- m["method"].(string) + " " + tid
				write(map[string]any{"id": m["id"], "result": map[string]any{}})
			}
		}
	}))
	t.Cleanup(func() {
		srv.Close()
		for {
			select {
			case c := <-wsConns:
				_ = c.Close()
				continue
			default:
			}
			return
		}
	})
	conn, err := connectCodexAppServer("ws" + strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	go observeCodexAppServer(conn)

	expect := func(want string) {
		t.Helper()
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("app-server got %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("app-server never got %q", want)
		}
	}
	quiet := func(why string) {
		t.Helper()
		select {
		case got := <-calls:
			t.Fatalf("%s: app-server got %q", why, got)
		case <-time.After(300 * time.Millisecond):
		}
	}
	expect("thread/resume thr-held") // the first sweep attaches

	releaseCodexObservedThread("thr-held")
	expect("thread/unsubscribe thr-held")

	// Still loaded during the server's grace period: neither a sweep nor a status broadcast
	// may put the observer back on it.
	codexObsMu.Lock()
	obs := codexObsCur
	codexObsMu.Unlock()
	obs.sweep()
	push <- map[string]any{"method": "thread/status/changed",
		"params": map[string]any{"threadId": "thr-held", "status": map[string]any{"type": "idle"}}}
	quiet("released thread re-attached while still loaded")

	// The hold is on disk, so an Agent restarted during the pane's wait keeps off the thread.
	codexReleasedMu.Lock()
	codexReleased = map[string]bool{}
	codexReleasedMu.Unlock()
	loadCodexReleased()
	if !codexThreadReleased("thr-held") {
		t.Fatal("the release was not restored from disk")
	}

	// Unloaded, seen through a sweep (no notLoaded broadcast arrives): the hold ends, and a
	// later load is observed again.
	loadedMu.Lock()
	loaded = false
	loadedMu.Unlock()
	obs.sweep()
	deadline := time.Now().Add(3 * time.Second)
	for codexThreadReleased("thr-held") {
		if time.Now().After(deadline) {
			t.Fatal("a sweep without the thread did not end the release")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, _ := os.ReadFile(releasedFile); strings.Contains(string(b), "thr-held") {
		t.Fatalf("the ended hold is still on disk: %s", b)
	}
	loadedMu.Lock()
	loaded = true
	loadedMu.Unlock()
	obs.sweep()
	expect("thread/resume thr-held")
	close(push)
}

// notLoaded is the other unload signal, and a managed Resume's restore is the explicit one.
func TestCodexReleaseEndsOnNotLoadedOrRestore(t *testing.T) {
	prevFile := codexReleasedFile
	codexReleasedFile = func() string { return filepath.Join(t.TempDir(), "released.json") }
	t.Cleanup(func() { codexReleasedFile = prevFile })

	releaseCodexObservedThread("thr-a")
	obs := newCodexObserver(nil) // neither path touches conn
	obs.observeThreadLifecycle(codexAppServerMessage{
		Method: "thread/status/changed",
		Params: []byte(`{"threadId":"thr-a","status":{"type":"notLoaded"}}`),
	})
	if codexThreadReleased("thr-a") {
		t.Fatal("notLoaded did not end the release")
	}
	releaseCodexObservedThread("thr-b")
	clearCodexReleased("thr-b") // what codex.RestoreObservedThread does
	if codexThreadReleased("thr-b") {
		t.Fatal("restore did not end the release")
	}
}

package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

package main

// Codex app-server lifecycle and event monitor.
//
// The interactive Codex TUI connects to this local app-server over loopback.
// A second, read-only AF connection observes the TUI's threads, which gives us
// first-class signals instead of scraping version-dependent terminal text:
//   - contextCompaction item lifecycle → live compacting state (codex.SetCompacting)
//   - account/rateLimits/updated → usage reading fresher than the rollout snapshot
//     (codex.SetObservedRateLimits)
//   - model/rerouted, thread/settings/updated, warning, thread/status/changed →
//     structured observation log (docs/log/27 P1). The log separates the two possible
//     causes of an unrequested model switch: a server-side reroute emits
//     model/rerouted, while a TUI-level nudge acceptance emits only a
//     thread/settings/updated with a changed model.
//
// The app-server delivers thread-scoped notifications (item/*, turn/*,
// thread/settings/updated, thread-targeted warning) only to connections that have
// the thread loaded; a passive socket sees little more than thread/started
// (verified live on CLI 0.144.3 and 0.144.4). The observer therefore attaches to
// every loaded thread with `thread/resume` — on a running thread that joins the
// in-memory instance without touching its rollout — via codexObserver below.
//
// Event names and payload fields verified against the CLI's own protocol schema
// (`codex app-server generate-json-schema`, CLI 0.144.4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const codexAppServerEnv = "AF_CODEX_APP_SERVER_ADDR"
const defaultCodexAppServerAddr = "ws://127.0.0.1:7798"

type codexAppServerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type codexAppServerItemNotification struct {
	ThreadID string `json:"threadId"`
	Item     struct {
		Type string `json:"type"`
	} `json:"item"`
}

type codexAppServerThreadNotification struct {
	ThreadID string `json:"threadId"`
}

type codexAppServerRateLimitsNotification struct {
	RateLimits struct {
		Primary              *codex.RateLimitWindow `json:"primary"`
		Secondary            *codex.RateLimitWindow `json:"secondary"`
		PlanType             string                 `json:"planType"`
		RateLimitReachedType string                 `json:"rateLimitReachedType"`
	} `json:"rateLimits"`
}

type codexAppServerModelReroutedNotification struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	FromModel string `json:"fromModel"`
	ToModel   string `json:"toModel"`
	Reason    string `json:"reason"`
}

type codexAppServerThreadSettingsNotification struct {
	ThreadID       string `json:"threadId"`
	ThreadSettings struct {
		Model             string `json:"model"`
		Effort            string `json:"effort"`
		CollaborationMode struct {
			Mode string `json:"mode"`
		} `json:"collaborationMode"`
	} `json:"threadSettings"`
}

type codexAppServerWarningNotification struct {
	Message  string `json:"message"`
	ThreadID string `json:"threadId"`
}

type codexAppServerThreadStatusNotification struct {
	ThreadID string `json:"threadId"`
	Status   struct {
		Type        string   `json:"type"`
		ActiveFlags []string `json:"activeFlags"`
	} `json:"status"`
}

// codexObservedLast suppresses consecutive-duplicate observation log lines, keyed
// by "<kind> <threadId>". Bounded by the workspace's live thread count; never
// cleared — a stale entry after a reconnect only suppresses one repeated line.
var codexObservedMu sync.Mutex
var codexObservedLast = map[string]string{}

// codexObservationSwap records the latest formatted observation and returns the
// previous one; changed is false for a consecutive duplicate.
func codexObservationSwap(kind, threadID, line string) (prev string, changed bool) {
	key := kind + " " + threadID
	codexObservedMu.Lock()
	defer codexObservedMu.Unlock()
	prev = codexObservedLast[key]
	if prev == line {
		return prev, false
	}
	codexObservedLast[key] = line
	return prev, true
}

// fmtCodexRateWindow renders one rate-limit window for the observation log.
func fmtCodexRateWindow(w *codex.RateLimitWindow) string {
	if w == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%/%dm resets=%s", w.UsedPercent, w.WindowMinutes,
		time.Unix(w.ResetsAt, 0).UTC().Format(time.RFC3339))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// startCodexAppServer wires the shared local server (owned by the codex
// RuntimeSupervisor since P3 — daemon 起動・generation・exit recording は
// codex.Serve() 側、docs/log/27 §10.2-2) into package main and starts the AF
// read-only observer.
//
// 起動時に daemon を上げるのはやめた: 未ログインでも、codex を一度も使わなくても
// 約 110 MB（native 62 MB＋node シム 48 MB、実測）が常駐していた。冷起動は 217 ms
// （実測）なので、需要——managed の Resume と TUI の BuildLaunch——で起こすほうが
// 素直。ここでやるのは (1) 台帳を持たない codex package へ「TUI ルートで生きている
// セッション数」を渡す継ぎ目の登録、(2) 前の Agent プロセスが残した daemon の
// 引き取り、(3) daemon の生涯をまたぐオブザーバの常設、の 3 つだけ。
//
// オブザーバは writer とは SEPARATE な read-only ソケット: thread スコープの通知は
// 接続ごと（docs/log/27 §12.1-1）で、writer は自分が start/resume した thread しか
// 見ない — TUI（CLI ルート）の thread を覆うのはオブザーバの仕事。
func startCodexAppServer() {
	if codex.Serve().Disabled() {
		_ = os.Unsetenv(codexAppServerEnv)
		return
	}
	// 継ぎ目は AdoptIfRunning より先に張ること: Ensure が需要ゼロ監視を張る際に
	// TUIDependents を読むので、後から差し替えると「TUI セッションが 0 に見える窓」が
	// できて、生きている TUI の backend を畳みかねない。
	codex.TUIDependents = liveCodexTUISessions
	codex.DaemonUp = wakeCodexObserver
	codex.Serve().AdoptIfRunning()
	go superviseCodexObserver()
}

// liveCodexTUISessions counts the codex sessions running on the CLI route whose
// backend is the shared app-server (`codex --remote`). They are dependents of the
// daemon exactly like managed handles: kill it under them and the TUI's
// conversation stops dead. tmux は 1 発（list-sessions）で数える。
func liveCodexTUISessions() int { return countCodexTUISessions(tmuxx.LiveSessionNames()) }

// countCodexTUISessions is the pure half, so the filter can be tested without
// creating a session in the fleet's own tmux namespace (LiveSessionNames only
// reports names carrying session.TmuxPrefix, so a test would have to plant a
// `claude_*` session that the Console would then show as an orphan).
//
// live のキーは **接頭辞を剥いだセッション名**（tmuxx.LiveSessionNames）。実 tmux で
// 確認済み: `claude_skggere` → `skggere`。ここを取り違えると常に 0 になり、
// 生きている TUI セッションの backend を需要ゼロ判定が引き抜く。
func countCodexTUISessions(live map[string]bool) int {
	if len(live) == 0 {
		return 0
	}
	n := 0
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCodex || m.Archived || m.DriverKind() == session.DriverManaged {
			continue
		}
		if live[m.Name] {
			n++
		}
	}
	return n
}

func connectCodexAppServer(addr string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	url := addr
	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if path == "" {
			return nil, errors.New("empty app-server unix socket path")
		}
		dialer.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		url = "ws://localhost/"
	}
	conn, resp, err := dialer.Dial(url, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("app-server websocket: %s: %w", resp.Status, err)
		}
		return nil, err
	}
	init := map[string]any{
		"method": "initialize",
		"id":     1,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name": "agent_fleet", "title": "Agent Fleet", "version": "1",
			},
			// AF needs lifecycle boundaries, not token/terminal deltas. Suppressing
			// high-volume notifications keeps the observer cheap and does not affect
			// the TUI's separate app-server connection.
			"capabilities": map[string]any{"optOutNotificationMethods": []string{
				"item/agentMessage/delta",
				"item/plan/delta",
				"item/reasoning/summaryPartAdded",
				"item/reasoning/summaryTextDelta",
				"item/reasoning/textDelta",
				"item/commandExecution/outputDelta",
				"item/commandExecution/terminalInteraction",
				"item/fileChange/outputDelta",
				"item/fileChange/patchUpdated",
				"item/mcpToolCall/progress",
				"turn/diff/updated",
				"turn/plan/updated",
				"thread/tokenUsage/updated",
			}},
		},
	}
	if err := conn.WriteJSON(init); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return nil, err
		}
		var msg codexAppServerMessage
		if json.Unmarshal(raw, &msg) != nil || string(msg.ID) != "1" {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			conn.Close()
			return nil, fmt.Errorf("app-server initialize: %s", msg.Error)
		}
		break
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

const codexObserverSweepInterval = 30 * time.Second

// codexObserver owns one AF observer connection and keeps it attached to every
// loaded thread (see the file comment: without an attach, thread-scoped
// notifications are never delivered to this connection). Attach sources: the
// thread/started broadcast, plus a periodic thread/loaded/list sweep that covers
// threads loaded before we connected and retries fresh threads whose rollout
// does not exist yet (thread/resume fails until the first turn is recorded).
type codexObserver struct {
	conn *websocket.Conn

	mu        sync.Mutex
	nextID    int
	pending   map[int]string  // in-flight observer request id → thread id ("" = loaded/list)
	requested map[string]bool // threads attached or with an in-flight resume
}

func newCodexObserver(conn *websocket.Conn) *codexObserver {
	// Request ids share the connection's JSON-RPC space with initialize (id 1);
	// start well clear of it.
	return &codexObserver{conn: conn, nextID: 100, pending: map[int]string{}, requested: map[string]bool{}}
}

// attach subscribes this connection to one thread via a read-only thread/resume.
func (o *codexObserver) attach(threadID string) {
	if threadID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.requested[threadID] {
		return
	}
	o.requested[threadID] = true
	o.sendLocked("thread/resume", map[string]any{"threadId": threadID}, threadID)
}

func (o *codexObserver) sweep() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sendLocked("thread/loaded/list", map[string]any{}, "")
}

func (o *codexObserver) forget(threadID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.requested, threadID)
}

func (o *codexObserver) sendLocked(method string, params map[string]any, threadID string) {
	o.nextID++
	id := o.nextID
	o.pending[id] = threadID
	if err := o.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		delete(o.pending, id)
		if threadID != "" {
			delete(o.requested, threadID) // let the next sweep retry
		}
	}
}

// handleResponse resolves an observer-sent request. A resume failure releases
// the thread so the next sweep retries it.
func (o *codexObserver) handleResponse(msg codexAppServerMessage) {
	var id int
	if json.Unmarshal(msg.ID, &id) != nil {
		return
	}
	o.mu.Lock()
	threadID, ok := o.pending[id]
	delete(o.pending, id)
	o.mu.Unlock()
	if !ok {
		return
	}
	failed := len(msg.Error) > 0 && string(msg.Error) != "null"
	if threadID == "" { // thread/loaded/list
		var res struct {
			Data []string `json:"data"`
		}
		if !failed && json.Unmarshal(msg.Result, &res) == nil {
			for _, tid := range res.Data {
				o.attach(tid)
			}
		}
		return
	}
	if failed {
		o.forget(threadID)
		// Expected once per fresh thread ("no rollout found" until its first
		// turn lands); dedupe so the sweep retries don't repeat the line.
		if _, changed := codexObservationSwap("attachErr", threadID, string(msg.Error)); changed {
			log.Printf("codex app-server: observer attach failed thread=%s: %s", threadID, msg.Error)
		}
		return
	}
	log.Printf("codex app-server: observing thread %s", threadID)
}

// observeThreadLifecycle maintains the attach set from broadcast notifications.
// thread/started announces new threads only; a thread loaded by another
// connection's resume (the TUI resuming an AF session) is announced by a
// broadcast thread/status/changed instead, so both trigger an attach.
func (o *codexObserver) observeThreadLifecycle(msg codexAppServerMessage) {
	switch msg.Method {
	case "thread/started":
		var p struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			o.attach(p.Thread.ID)
		}
	case "thread/status/changed":
		var p codexAppServerThreadStatusNotification
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		// Never attach on notLoaded: resuming would make the observer LOAD the
		// thread from disk, keeping it in server memory for no reader. Also forget
		// it — アンロードは thread/closed を伴わない（TUI 切断・アイドル退避）ため、
		// requested を残すと再ロード時に attach が早期 return し続け、そのスレッドの
		// 観測（圧縮検知等）が観測ソケットの再接続まで復活しない。
		if p.Status.Type == "notLoaded" {
			o.forget(p.ThreadID)
			return
		}
		if p.Status.Type != "" {
			o.attach(p.ThreadID)
		}
	case "thread/closed", "thread/deleted":
		var p codexAppServerThreadNotification
		if json.Unmarshal(msg.Params, &p) == nil {
			o.forget(p.ThreadID)
		}
	}
}

// codexObserverWake carries "the daemon is up again" from the supervisor
// (codex.DaemonUp) to the observer's reconnect wait. Buffered 1 + non-blocking
// send: a wake that nobody is waiting for is dropped, never queued.
var codexObserverWake = make(chan struct{}, 1)

func wakeCodexObserver() {
	select {
	case codexObserverWake <- struct{}{}:
	default:
	}
}

// superviseCodexObserver keeps ONE read-only observer alive across the daemon's
// whole life, including the stretches where there IS no daemon: the shared
// app-server is now started on demand and stopped again once nothing needs it
// (docs/log/27 §7 補遺), so "not listening" is a normal resting state, not a
// failure. While it is down we wait — woken immediately by DaemonUp when a
// session brings it back, and otherwise polling on a capped backoff so an
// adopted/externally-started daemon is still picked up.
func superviseCodexObserver() {
	backoff := time.Second
	for {
		if codex.Serve().Disabled() {
			log.Printf("codex app-server: observer stopped (server disabled)")
			return
		}
		conn, err := connectCodexAppServer(codex.Serve().Addr())
		if err != nil {
			select {
			case <-codexObserverWake:
				backoff = time.Second
			case <-time.After(backoff):
				if backoff *= 2; backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}
			continue
		}
		backoff = time.Second
		// Attachments are per connection, so each fresh observer's first sweep
		// re-attaches every loaded thread.
		observeCodexAppServer(conn)
	}
}

// observeCodexAppServer runs one observer connection until its socket drops.
func observeCodexAppServer(conn *websocket.Conn) {
	obs := newCodexObserver(conn)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		obs.sweep() // threads loaded before this connection existed
		t := time.NewTicker(codexObserverSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				obs.sweep()
			}
		}
	}()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			codex.ClearCompacting()
			log.Printf("codex app-server monitor disconnected: %v", err)
			return
		}
		var msg codexAppServerMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if msg.Method == "" {
			obs.handleResponse(msg)
			continue
		}
		// Server-initiated requests (method + id, e.g. approvals aimed at
		// the driving TUI) fall through harmlessly: no case matches, and we
		// must not answer on the TUI's behalf.
		obs.observeThreadLifecycle(msg)
		handleCodexAppServerEvent(raw)
	}
}

func handleCodexAppServerEvent(raw []byte) {
	var msg codexAppServerMessage
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	switch msg.Method {
	case "item/started", "item/completed":
		var p codexAppServerItemNotification
		if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID == "" || p.Item.Type != "contextCompaction" {
			return
		}
		codex.SetCompacting(p.ThreadID, msg.Method == "item/started")
	case "turn/completed":
		// A failed/aborted compaction may end its turn without item/completed.
		var p codexAppServerThreadNotification
		if json.Unmarshal(msg.Params, &p) == nil && p.ThreadID != "" {
			codex.SetCompacting(p.ThreadID, false)
		}
	case "account/rateLimits/updated":
		var p codexAppServerRateLimitsNotification
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		rl := p.RateLimits
		codex.SetObservedRateLimits(rl.Primary, rl.Secondary, rl.PlanType)
		line := fmt.Sprintf("primary=%s secondary=%s plan=%s reached=%s",
			fmtCodexRateWindow(rl.Primary), fmtCodexRateWindow(rl.Secondary),
			orDash(rl.PlanType), orDash(rl.RateLimitReachedType))
		if _, changed := codexObservationSwap("rateLimits", "", line); changed {
			log.Printf("codex app-server: account/rateLimits/updated %s", line)
		}
	case "model/rerouted":
		// Rare and high-signal (its absence around a model switch is what convicts
		// the TUI nudge), so always logged, never deduplicated.
		var p codexAppServerModelReroutedNotification
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		log.Printf("codex app-server: model/rerouted thread=%s turn=%s from=%s to=%s reason=%s",
			orDash(p.ThreadID), orDash(p.TurnID), orDash(p.FromModel), orDash(p.ToModel), orDash(p.Reason))
	case "thread/settings/updated":
		var p codexAppServerThreadSettingsNotification
		if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID == "" {
			return
		}
		s := p.ThreadSettings
		line := fmt.Sprintf("model=%s effort=%s mode=%s", orDash(s.Model), orDash(s.Effort), orDash(s.CollaborationMode.Mode))
		if prev, changed := codexObservationSwap("settings", p.ThreadID, line); changed {
			if prev != "" {
				log.Printf("codex app-server: thread/settings/updated thread=%s %s (prev %s)", p.ThreadID, line, prev)
			} else {
				log.Printf("codex app-server: thread/settings/updated thread=%s %s", p.ThreadID, line)
			}
		}
	case "warning":
		var p codexAppServerWarningNotification
		if json.Unmarshal(msg.Params, &p) != nil || p.Message == "" {
			return
		}
		log.Printf("codex app-server: warning thread=%s message=%q", orDash(p.ThreadID), p.Message)
	case "thread/status/changed":
		var p codexAppServerThreadStatusNotification
		if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID == "" || p.Status.Type == "" {
			return
		}
		st := p.Status.Type
		if len(p.Status.ActiveFlags) > 0 {
			st += "[" + strings.Join(p.Status.ActiveFlags, ",") + "]"
		}
		if _, changed := codexObservationSwap("status", p.ThreadID, st); changed {
			log.Printf("codex app-server: thread/status/changed thread=%s status=%s", p.ThreadID, st)
		}
	}
}

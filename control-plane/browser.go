package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	browserControlQueueSize      = 32
	browserMaxAgentMessageBytes  = 8 << 20
	browserMaxAgentControlBytes  = 256 << 10
	browserMaxClientMessageBytes = 64 << 10
	browserAgentErrorBodyBytes   = 64 << 10
	browserHandshakeTimeout      = 10 * time.Second
	browserWriteTimeout          = 5 * time.Second
)

var (
	errBrowserControlBackpressure = errors.New("browser control queue is full")
	errBrowserControlTooLarge     = errors.New("browser control message is too large")
)

// browserAPI relays the restricted browser REST/WebSocket protocol to the
// membership's Workspace Agent. It intentionally does not share the terminal
// relay: browser frames need latest-only delivery while terminal bytes must be
// lossless and ordered.
type browserAPI struct{ memberAuth }

func newBrowserAPI(m *manager) browserAPI { return browserAPI{memberAuth{m}} }

// rest forwards /api/browser/pages* verbatim to /browser/pages* on the Agent.
// Browser pages are ephemeral Agent-owned resources; the CP neither decodes nor
// persists their target, URL, title, console output, or response body.
func (a browserAPI) rest(w http.ResponseWriter, r *http.Request, res *resolved) {
	if !browserRuntimeReady(w, r, res.rt) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if err := a.mgr.touchWorkspace(r.Context(), res.ws.ID); err != nil {
			writeAPIErr(w, workspaceActivityAPIError(err))
			return
		}
	}

	if unsafeRelayPath(r.URL.Path) {
		http.Error(w, "bad browser proxy path", http.StatusBadRequest)
		return
	}
	target, err := browserAgentHTTPURL(res.rt.Endpoint(), r)
	if err != nil {
		http.Error(w, "bad agent endpoint", http.StatusBadGateway)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad browser proxy request", http.StatusBadGateway)
		return
	}
	// Do not forward Console cookies, gateway identity, tenant selection, or an
	// inbound Authorization header across the CP -> Agent trust boundary.
	copyHeader(req.Header, r.Header, "Content-Type")
	copyHeader(req.Header, r.Header, "Accept")
	if token := res.rt.Token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := agentRelayClient.Do(req)
	if err != nil {
		http.Error(w, "workspace agent browser unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header, "Content-Type")
	copyHeader(w.Header(), resp.Header, "Cache-Control")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyHeader(dst, src http.Header, name string) {
	for _, value := range src.Values(name) {
		dst.Add(name, value)
	}
}

func browserAgentHTTPURL(endpoint string, r *http.Request) (string, error) {
	target, err := url.Parse(strings.TrimRight(endpoint, "/") + strings.TrimPrefix(r.URL.Path, "/api"))
	if err != nil {
		return "", err
	}
	q := r.URL.Query()
	q.Del("tenant") // CP-only membership selector; never disclose it to the Agent.
	target.RawQuery = q.Encode()
	return target.String(), nil
}

func browserRuntimeReady(w http.ResponseWriter, r *http.Request, rt Runtime) bool {
	switch rt.State(r.Context()) {
	case "running":
		return true
	case "starting":
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_starting",
			"workspace is starting — wait for it to come up"})
	default:
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_stopped",
			"workspace is stopped — start it first"})
	}
	return false
}

// socket bridges /ws/browser to the Agent while keeping browser-specific
// backpressure local to this relay. Agent text events remain ordered and binary
// JPEG frames use a capacity-one latest slot, so a slow Console cannot build an
// unbounded frame backlog or stall unrelated Agent browser work.
func (a browserAPI) socket(w http.ResponseWriter, r *http.Request, res *resolved) {
	a.socketToAgent(w, r, res, "/ws/browser")
}

func (a browserAPI) attachmentSocket(w http.ResponseWriter, r *http.Request, res *resolved) {
	a.socketToAgent(w, r, res, "/ws/browser-attachments")
}

func (a browserAPI) socketToAgent(w http.ResponseWriter, r *http.Request, res *resolved, agentPath string) {
	if !browserRuntimeReady(w, r, res.rt) {
		return
	}
	agentURL, err := browserAgentWebSocketURLForPath(res.rt.Endpoint(), agentPath, r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, "bad agent endpoint", http.StatusBadGateway)
		return
	}

	var headers http.Header
	if token := res.rt.Token(); token != "" {
		headers = http.Header{"Authorization": []string{"Bearer " + token}}
	}
	dialer := websocket.Dialer{HandshakeTimeout: browserHandshakeTimeout, EnableCompression: true, NetDialContext: dialAgent}
	up, agentResp, err := dialer.Dial(agentURL, headers)
	if err != nil {
		writeAgentHandshakeError(w, agentResp, "cannot reach workspace agent browser")
		return
	}
	defer up.Close()
	up.SetReadLimit(browserMaxAgentMessageBytes)

	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer down.Close()
	down.SetReadLimit(browserMaxClientMessageBytes)

	viewer := newBrowserViewer(a.mgr.conns, res.ws.ID)
	viewer.setVisible(true)
	defer viewer.close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := newBrowserOutbound()
	errc := make(chan error, 3)
	go func() { errc <- readBrowserAgent(ctx, up, out) }()
	go func() { errc <- writeBrowserClient(ctx, down, out) }()
	go func() { errc <- relayBrowserControls(down, up, viewer) }()
	<-errc
}

func browserAgentWebSocketURL(endpoint, browserID string) (string, error) {
	return browserAgentWebSocketURLForPath(endpoint, "/ws/browser", browserID)
}

func browserAgentAttachmentWebSocketURL(endpoint, attachmentID string) (string, error) {
	return browserAgentWebSocketURLForPath(endpoint, "/ws/browser-attachments", attachmentID)
}

func browserAgentWebSocketURLForPath(endpoint, socketPath, browserID string) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	switch target.Scheme {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("unsupported agent endpoint scheme")
	}
	target.Path = socketPath
	target.RawQuery = url.Values{"id": []string{browserID}}.Encode()
	target.Fragment = ""
	return target.String(), nil
}

// Preserve the Agent's structured bad-id/already-attached handshake response.
// Only the status, content type, and bounded body cross the boundary; headers
// such as Agent bearer challenges are not exposed to the Console.
// writeAgentHandshakeError answers a failed CP→Agent WebSocket dial. gorilla returns a
// non-nil response exactly when the Agent ANSWERED and refused with an ordinary HTTP
// status (ErrBadHandshake) — relaying it is the difference between "the agent said no,
// and why" and a blanket 502 that claims the agent is unreachable when it plainly was.
// resp == nil is the genuinely-unreachable case, and only that gets the fallback 502.
func writeAgentHandshakeError(w http.ResponseWriter, resp *http.Response, fallback string) {
	if resp == nil {
		http.Error(w, fallback, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header, "Content-Type")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, browserAgentErrorBodyBytes))
}

type browserMessage struct {
	typ  int
	data []byte
}

type browserOutbound struct {
	controls chan browserMessage
	frame    chan browserMessage
}

func newBrowserOutbound() *browserOutbound {
	return &browserOutbound{
		controls: make(chan browserMessage, browserControlQueueSize),
		frame:    make(chan browserMessage, 1),
	}
}

func (q *browserOutbound) enqueue(typ int, data []byte) error {
	msg := browserMessage{typ: typ, data: data}
	if typ != websocket.BinaryMessage {
		if len(data) > browserMaxAgentControlBytes {
			return errBrowserControlTooLarge
		}
		select {
		case q.controls <- msg:
			return nil
		default:
			return errBrowserControlBackpressure
		}
	}

	// The sole producer may race the writer consuming the old frame. Drain if it
	// is still present, then publish the new frame; either outcome leaves at most
	// one unsent JPEG and never blocks the Agent reader.
	select {
	case q.frame <- msg:
		return nil
	default:
	}
	select {
	case <-q.frame:
	default:
	}
	select {
	case q.frame <- msg:
	default:
	}
	return nil
}

func readBrowserAgent(ctx context.Context, src *websocket.Conn, out *browserOutbound) error {
	for {
		typ, data, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if typ != websocket.TextMessage && typ != websocket.BinaryMessage {
			return errors.New("unsupported browser message type from Agent")
		}
		if err := out.enqueue(typ, data); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

type browserMessageWriter interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
}

func writeBrowserClient(ctx context.Context, dst browserMessageWriter, out *browserOutbound) error {
	for {
		var msg browserMessage
		// Prefer state/navigation/error text over a replaceable frame whenever a
		// control event is already queued.
		select {
		case msg = <-out.controls:
		default:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case msg = <-out.controls:
			case msg = <-out.frame:
			}
		}
		if err := dst.SetWriteDeadline(time.Now().Add(browserWriteTimeout)); err != nil {
			return err
		}
		if err := dst.WriteMessage(msg.typ, msg.data); err != nil {
			return err
		}
	}
}

func relayBrowserControls(src, dst *websocket.Conn, viewer *browserViewer) error {
	for {
		typ, data, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if err := dst.SetWriteDeadline(time.Now().Add(browserWriteTimeout)); err != nil {
			return err
		}
		if err := dst.WriteMessage(typ, data); err != nil {
			return err
		}
		// Visibility remains an end-to-end protocol message. The CP observes only
		// this boolean after forwarding it so hidden retained pages stop pinning
		// the Workspace; screencast stop/restart and CDP ack stay Agent-owned.
		if visible, ok := browserVisibility(data, typ); ok {
			viewer.setVisible(visible)
		}
	}
}

func browserVisibility(data []byte, typ int) (bool, bool) {
	if typ != websocket.TextMessage {
		return false, false
	}
	var msg struct {
		Type    string `json:"type"`
		Visible *bool  `json:"visible"`
	}
	if json.Unmarshal(data, &msg) != nil || msg.Type != "visibility" || msg.Visible == nil {
		return false, false
	}
	return *msg.Visible, true
}

// browserViewer dynamically removes hidden sockets from the reaper's warm
// connection count without closing the socket or attaching it to a Session.
type browserViewer struct {
	mu       sync.Mutex
	registry *connRegistry
	wsID     string
	active   bool
	closed   bool
}

func newBrowserViewer(registry *connRegistry, wsID string) *browserViewer {
	return &browserViewer{registry: registry, wsID: wsID}
}

func (v *browserViewer) setVisible(visible bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.active == visible {
		return
	}
	v.active = visible
	if visible {
		v.registry.addConn(v.wsID, "")
	} else {
		v.registry.doneConn(v.wsID, "")
	}
}

func (v *browserViewer) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	v.closed = true
	if v.active {
		v.active = false
		v.registry.doneConn(v.wsID, "")
	}
}

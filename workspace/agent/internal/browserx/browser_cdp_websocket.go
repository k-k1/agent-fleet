package browserx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const browserCDPHandshakeTimeout = 3 * time.Second

type webSocketCDPTransport struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func dialWebSocketCDP(ctx context.Context, port int, rawDebuggerURL string) (browserCDP, error) {
	target, err := reconstructCDPWebSocketURL(port, rawDebuggerURL)
	if err != nil {
		return nil, attachmentError(422, "cdp_endpoint_invalid", "endpoint advertised an invalid debugger socket", err)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: browserCDPHandshakeTimeout,
		NetDialContext: (&net.Dialer{
			Timeout: browserCDPHandshakeTimeout,
		}).DialContext,
	}
	conn, resp, err := dialer.DialContext(ctx, target, nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return nil, attachmentError(422, "cdp_endpoint_invalid", "CDP WebSocket handshake was rejected",
				fmt.Errorf("HTTP %d: %w", resp.StatusCode, err))
		}
		return nil, attachmentError(502, "cdp_unreachable", "CDP endpoint is unreachable", err)
	}
	conn.SetReadLimit(browserCDPMaxMessageBytes)
	return newBrowserCDPCore(&webSocketCDPTransport{conn: conn}), nil
}

// reconstructCDPWebSocketURL trusts only the path/query advertised by Chromium.
// The scheme and authority are rebuilt from the already validated loopback port.
func reconstructCDPWebSocketURL(port int, raw string) (string, error) {
	if err := validateCDPPort(port); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Path == "" ||
		!strings.HasPrefix(u.Path, "/") || u.User != nil || u.Fragment != "" {
		return "", errors.New("invalid Chromium WebSocket debugger URL")
	}
	return (&url.URL{
		Scheme:   "ws",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
	}).String(), nil
}

func (t *webSocketCDPTransport) ReadMessage() ([]byte, error) {
	typ, data, err := t.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if typ != websocket.TextMessage && typ != websocket.BinaryMessage {
		return nil, errors.New("unsupported CDP WebSocket message type")
	}
	return data, nil
}

func (t *webSocketCDPTransport) WriteMessage(data []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return t.conn.WriteMessage(websocket.TextMessage, data)
}

func (t *webSocketCDPTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "detached"),
			time.Now().Add(time.Second))
		err = t.conn.Close()
	})
	return err
}

package kiro

// acpClient は `kiro-cli acp` 子プロセスとの JSON-RPC 2.0（newline-delimited、stdio）
// クライアント（docs/log/43 Track A2・2.14.1 実測）。cursor/copilot の acp.go と同じプロトコル
// 汎用の骨格で、kiro 固有の差分は driver.go 側（onNotify の session/update 判別・
// `_kiro.dev/metadata` 通知の扱い）が担う。
//   - call: id 採番 → 書き込み → 応答待ち（timeout 0 = 無期限、session/prompt 用）
//   - notifyPeer: 通知（session/cancel）
//   - respond: サーバー発リクエスト（session/request_permission）への応答
// readLoop が応答/通知/サーバー発リクエストを振り分ける。onRequest / onNotify は
// readLoop goroutine 上で同期に呼ばれるため、実装はブロックしてはならない
// （permission はハンドラが Interaction を記録して即 return し、応答は後から
// respond() で返す。onNotify は転写バッファへ追記するだけで軽い）。
//
// kiro は session/update（ACP 標準）に加え、`_kiro.dev/*` 名前空間の独自通知
// （metadata / subagent/list_update / commands/available / session/update=retry_warning）を
// 流す。これらはすべて id 無しの通知なので onNotify が受け、driver 側で必要なものだけ
// 拾って残りは黙って捨てる（応答不要）。

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

type rpcResult struct {
	result json.RawMessage
	err    error
}

// rpcError is the JSON-RPC error object. kiro packs the human-useful detail into the
// `data` field（例: 「Session is active in another process (PID …)」）——lock 競合の判定
// （isLockBusy）に使うので Error() に畳んで露出する。
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return e.Message + ": " + string(e.Data)
	}
	return e.Message
}

type acpClient struct {
	mu      sync.Mutex
	stdin   io.Writer
	pending map[int64]chan rpcResult
	nextID  int64

	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	closeOnce sync.Once
	closed    chan struct{} // closed when the readLoop exits (child gone)
}

func newACPClient(stdin io.Writer, stdout io.Reader) *acpClient {
	c := &acpClient{
		stdin:   stdin,
		pending: map[int64]chan rpcResult{},
		closed:  make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c
}

var errClientClosed = errors.New("kiro runtime との接続が切れました")

// readLoop dispatches incoming lines until the child's stdout closes. kiro streams a
// large `_kiro.dev/commands/available`（skill/command 一覧）を 1 行で流すため、バッファは
// 広く取る（cursor と同じ）。
func (c *acpClient) readLoop(stdout io.Reader) {
	defer c.markClosed()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// server-initiated request (session/request_permission など)
			if c.onRequest != nil {
				c.onRequest(msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "":
			// notification (session/update, _kiro.dev/*, …)
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
			}
		default:
			var id int64
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				var err error
				if msg.Error != nil {
					err = msg.Error
				}
				ch <- rpcResult{result: msg.Result, err: err}
			}
		}
	}
}

// markClosed fails every in-flight call and marks the client dead.
func (c *acpClient) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResult{err: errClientClosed}
	}
}

func (c *acpClient) dead() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *acpClient) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead() {
		return errClientClosed
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// call issues a request and waits for its response. timeout 0 = wait forever
// (a turn legitimately runs for minutes/hours — interrupt or child death unblocks it).
func (c *acpClient) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.write(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-c.closed:
		// markClosed が pending へ配送済みの場合もあるので回収を試みる。
		select {
		case r := <-ch:
			return r.result, r.err
		default:
			return nil, errClientClosed
		}
	case <-timer:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errors.New("kiro runtime が応答しません: " + method)
	}
}

func (c *acpClient) notifyPeer(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// respond answers a server-initiated request by its raw id.
func (c *acpClient) respond(id json.RawMessage, result any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

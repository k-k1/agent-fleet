package browserx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type channelCDPTransport struct {
	reads     chan []byte
	writes    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newChannelCDPTransport() *channelCDPTransport {
	return &channelCDPTransport{reads: make(chan []byte, 4), writes: make(chan []byte, 4), closed: make(chan struct{})}
}

func (t *channelCDPTransport) ReadMessage() ([]byte, error) {
	select {
	case data := <-t.reads:
		return data, nil
	case <-t.closed:
		return nil, io.EOF
	}
}

func (t *channelCDPTransport) WriteMessage(data []byte) error {
	select {
	case t.writes <- append([]byte(nil), data...):
		return nil
	case <-t.closed:
		return io.ErrClosedPipe
	}
}

func (t *channelCDPTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func TestBrowserCDPCoreUsesFramingAgnosticMessageTransport(t *testing.T) {
	transport := newChannelCDPTransport()
	core := newBrowserCDPCore(transport)
	defer core.Close()
	type result struct {
		Value string `json:"value"`
	}
	got := result{}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- core.Call(ctx, "Runtime.evaluate", map[string]any{"expression": "1"}, "session-1", &got)
	}()
	request := <-transport.writes
	if len(request) == 0 || request[len(request)-1] == 0 {
		t.Fatalf("common core wrote transport framing bytes: %q", request)
	}
	var envelope struct {
		ID        int64  `json:"id"`
		Method    string `json:"method"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(request, &envelope); err != nil || envelope.Method != "Runtime.evaluate" || envelope.SessionID != "session-1" {
		t.Fatalf("core request = %+v err=%v", envelope, err)
	}
	transport.reads <- []byte(fmt.Sprintf(`{"id":%d,"result":{"value":"ok"}}`, envelope.ID))
	if err := <-done; err != nil || got.Value != "ok" {
		t.Fatalf("core response = %+v err=%v", got, err)
	}
}

func TestChromiumLaunchArgsKeepSandboxAndAvoidDevShm(t *testing.T) {
	args := chromiumLaunchArgs("/tmp/profile", true)
	if !hasBrowserArg(args, "--disable-dev-shm-usage") {
		t.Fatal("production Chromium args do not define the /dev/shm policy")
	}
	if hasBrowserArg(args, "--no-sandbox") {
		t.Fatal("production Chromium args unexpectedly disable the sandbox")
	}

	if args := chromiumLaunchArgs("/tmp/profile", false); !hasBrowserArg(args, "--no-sandbox") {
		t.Fatal("explicit local-only sandbox override was ignored")
	}
}

func TestBrowserCDPCoreEventQueueFailsClosedWithoutWaiterGoroutines(t *testing.T) {
	p := &browserCDPCore{events: make(chan browserCDPEvent, 1)}
	if err := p.dispatch([]byte(`{"method":"Runtime.consoleAPICalled","sessionId":"s","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.dispatch([]byte(`{"method":"Network.dataReceived","sessionId":"s","params":{}}`)); err != nil {
		t.Fatalf("droppable event overflow = %v", err)
	}
	if got := len(p.events); got != 1 {
		t.Fatalf("event queue length = %d, want fixed capacity 1", got)
	}
	// Screencast frames are lossy: a saturated queue drops them rather than killing
	// Chromium, so overflow must be a silent drop, not errBrowserCDPEventOverflow.
	if err := p.dispatch([]byte(`{"method":"Page.screencastFrame","sessionId":"s","params":{}}`)); err != nil {
		t.Fatalf("screencast frame overflow = %v, want silent drop", err)
	}
	if got := len(p.events); got != 1 {
		t.Fatalf("screencast frame grew queue to %d", got)
	}
	for _, method := range []string{"Fetch.requestPaused", "Target.targetCrashed"} {
		err := p.dispatch([]byte(`{"method":"` + method + `","sessionId":"s","params":{}}`))
		if !errors.Is(err, errBrowserCDPEventOverflow) {
			t.Fatalf("%s overflow = %v, want errBrowserCDPEventOverflow", method, err)
		}
		if got := len(p.events); got != 1 {
			t.Fatalf("%s grew queue to %d", method, got)
		}
	}
	ev := <-p.events
	ev.releaseQueueBytes()
	if got := p.eventBytes.Load(); got != 0 {
		t.Fatalf("queued event bytes after release = %d", got)
	}
}

func TestBrowserCDPCoreEventQueueHasFixedByteBudget(t *testing.T) {
	p := &browserCDPCore{events: make(chan browserCDPEvent, browserCDPEventQueueSize)}
	p.eventBytes.Store(browserCDPEventQueueBytes - 1)
	if err := p.dispatch([]byte(`{"method":"Runtime.consoleAPICalled","params":{}}`)); err != nil {
		t.Fatalf("droppable event over byte budget = %v", err)
	}
	if got := len(p.events); got != 0 {
		t.Fatalf("droppable event over byte budget was queued: %d", got)
	}
	err := p.dispatch([]byte(`{"method":"Fetch.requestPaused","params":{}}`))
	if !errors.Is(err, errBrowserCDPEventOverflow) {
		t.Fatalf("must-deliver event over byte budget = %v, want overflow", err)
	}
	if got := p.eventBytes.Load(); got != browserCDPEventQueueBytes-1 {
		t.Fatalf("failed reservations changed byte budget to %d", got)
	}
}

func hasBrowserArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

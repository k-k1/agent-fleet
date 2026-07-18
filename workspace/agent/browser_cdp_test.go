package main

import (
	"errors"
	"testing"
)

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

func TestPipeCDPEventQueueFailsClosedWithoutWaiterGoroutines(t *testing.T) {
	p := &pipeCDP{events: make(chan browserCDPEvent, 1)}
	if err := p.dispatch([]byte(`{"method":"Runtime.consoleAPICalled","sessionId":"s","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.dispatch([]byte(`{"method":"Network.dataReceived","sessionId":"s","params":{}}`)); err != nil {
		t.Fatalf("droppable event overflow = %v", err)
	}
	if got := len(p.events); got != 1 {
		t.Fatalf("event queue length = %d, want fixed capacity 1", got)
	}
	for _, method := range []string{"Fetch.requestPaused", "Page.screencastFrame", "Target.targetCrashed"} {
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

func TestPipeCDPEventQueueHasFixedByteBudget(t *testing.T) {
	p := &pipeCDP{events: make(chan browserCDPEvent, browserCDPEventQueueSize)}
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

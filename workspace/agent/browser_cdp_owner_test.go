package main

import (
	"math"
	"net"
	"os"
	"strings"
	"testing"
)

// A port only this process listens on must be attributed to this process — that
// is what tells "one owner" apart from "cannot tell", and the difference decides
// whether a legitimate attach is blocked.
func TestCDPPortListenersAttributesOwnSocket(t *testing.T) {
	if _, err := os.Stat(procNetTCP4); err != nil {
		t.Skip("no procfs on this platform")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	owners, ok := cdpPortListeners(port)
	if !ok {
		t.Fatal("own listening socket must be attributable")
	}
	if len(owners) != 1 || owners[0].PID != os.Getpid() {
		t.Fatalf("owners=%+v want only pid %d", owners, os.Getpid())
	}
	if err := ensureUnambiguousCDPPort(port); err != nil {
		t.Fatalf("single owner must not be rejected: %v", err)
	}
}

// A port nobody listens on is "unknown", not "one owner" — and must never be
// rejected on that basis. Discovery's own HTTP probe reports it as unreachable.
func TestCDPPortListenersUnknownWhenNoListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	if _, ok := cdpPortListeners(port); ok {
		t.Fatal("a closed port must report unknown ownership")
	}
	if err := ensureUnambiguousCDPPort(port); err != nil {
		t.Fatalf("unknown ownership must not block: %v", err)
	}
}

// Two processes on one port is the measured Chromium collision (docs/53 §53.16):
// the second browser binds the other loopback family and keeps running, so
// discovery would hand this session the OTHER session's browser.
func TestEnsureUnambiguousCDPPortRejectsTwoOwners(t *testing.T) {
	original := lookupCDPPortListeners
	t.Cleanup(func() { lookupCDPPortListeners = original })
	lookupCDPPortListeners = func(int) ([]cdpPortListener, bool) {
		return []cdpPortListener{
			{PID: 111, UserDataDir: "/home/dev/repos/a/.profile"},
			{PID: 222, IPv6: true, UserDataDir: "/home/dev/repos/b/.profile"},
		}, true
	}
	err := ensureUnambiguousCDPPort(9222)
	apiErr := asAttachmentAPIError(err)
	if apiErr.Code != "cdp_port_ambiguous" {
		t.Fatalf("code=%q want cdp_port_ambiguous", apiErr.Code)
	}
	if apiErr.Status != 409 {
		t.Fatalf("status=%d want 409", apiErr.Status)
	}
	// The message has to name the rival profile and the fix; "conflict" alone
	// leaves the agent retrying the same wrong port.
	for _, want := range []string{"pid=111", "pid=222", "IPv6", "/home/dev/repos/b/.profile", "--remote-debugging-port=0", "DevToolsActivePort"} {
		if !strings.Contains(apiErr.Message, want) {
			t.Fatalf("message %q must mention %q", apiErr.Message, want)
		}
	}
}

func TestCDPBrowserIDNormalization(t *testing.T) {
	const guid = "c162d83f-b0a3-41d3-9db6-e9f6012c1491"
	if got := cdpBrowserID("ws://127.0.0.1:9222/devtools/browser/" + guid); got != guid {
		t.Fatalf("cdpBrowserID=%q", got)
	}
	if got := cdpBrowserID("ws://127.0.0.1:9222/devtools/page/abc"); got != "" {
		t.Fatalf("a page socket is not a browser id: %q", got)
	}
	// What a caller can actually copy: the bare GUID, DevToolsActivePort line 2,
	// or the full advertised socket URL.
	for _, raw := range []string{guid, "  " + guid + "\n", "/devtools/browser/" + guid, "ws://127.0.0.1:9222/devtools/browser/" + guid} {
		if got := normalizeCDPBrowserID(raw); got != guid {
			t.Fatalf("normalizeCDPBrowserID(%q)=%q", raw, got)
		}
	}
	for _, raw := range []string{"", "  ", "http://evil.invalid/x", "a/b"} {
		if got := normalizeCDPBrowserID(raw); got != "" {
			t.Fatalf("normalizeCDPBrowserID(%q)=%q want empty", raw, got)
		}
	}
}

// fitLayoutViewport decides how far to zoom out. The aspect ratio must stay the
// pane's: the canvas stretches the frame to fill the pane, so any other ratio
// would render the page distorted rather than merely small.
func TestFitLayoutViewport(t *testing.T) {
	pane := browserViewport{Width: 660, Height: 800, DeviceScaleFactor: 1}

	if _, _, ok := fitLayoutViewport(pane, 640); ok {
		t.Fatal("content that already fits must not zoom")
	}
	if _, _, ok := fitLayoutViewport(pane, 664); ok {
		t.Fatal("a few px over the pane is rounding, not overflow")
	}
	vp, scale, ok := fitLayoutViewport(pane, 1240)
	if !ok || vp.Width != 1240 || vp.Height != 1503 {
		t.Fatalf("layout = %+v ok=%v", vp, ok)
	}
	if math.Abs(scale-660.0/1240.0) > 1e-9 {
		t.Fatalf("scale = %v", scale)
	}
	if ratio := float64(vp.Width) / float64(vp.Height); math.Abs(ratio-660.0/800.0) > 0.001 {
		t.Fatalf("aspect drifted: %v", ratio)
	}

	// A pathological page is capped, and whichever bound binds, the ratio and
	// both caps survive it. A portrait pane hits the HEIGHT cap first.
	capped, _, ok := fitLayoutViewport(pane, 99999)
	if !ok || capped.Height != browserMaxLayoutHeight || capped.Width != 3300 {
		t.Fatalf("portrait cap = %+v", capped)
	}
	if ratio := float64(capped.Width) / float64(capped.Height); math.Abs(ratio-660.0/800.0) > 0.001 {
		t.Fatalf("aspect drifted at the cap: %v", ratio)
	}
	// A landscape pane hits the WIDTH cap first.
	landscape := browserViewport{Width: 1200, Height: 400, DeviceScaleFactor: 1}
	wide, _, ok := fitLayoutViewport(landscape, 99999)
	if !ok || wide.Width != browserMaxLayoutWidth || wide.Height > browserMaxLayoutHeight {
		t.Fatalf("landscape cap = %+v", wide)
	}

	// A very tall, narrow pane hits the HEIGHT cap first; width is pulled back.
	tall := browserViewport{Width: 400, Height: 2000, DeviceScaleFactor: 1}
	got, _, ok := fitLayoutViewport(tall, 3000)
	if !ok || got.Height != browserMaxLayoutHeight || got.Width != 800 {
		t.Fatalf("height-capped layout = %+v ok=%v", got, ok)
	}
	if _, _, ok := fitLayoutViewport(browserViewport{}, 1240); ok {
		t.Fatal("a degenerate pane must be a no-op")
	}
}

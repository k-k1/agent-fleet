package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// keyboardTestCDPFactory picks whichever launcher can actually start Chromium
// here — see browserTestCDPFactory.
func keyboardTestCDPFactory(t *testing.T) browserCDPFactory {
	t.Helper()
	return browserTestCDPFactory(t)
}

func newKeyboardTestPage(t *testing.T, factory browserCDPFactory, html string, viewport browserViewportRequest) (*browserManager, *browserPage, browserCDP) {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(app.Close)
	u, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	m := newBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Minute, ChromiumIdle: time.Minute,
		CommandTimeout: 10 * time.Second, FrameInterval: time.Second / 12, JPEGQuality: 70,
		CDPFactory: factory,
	})
	t.Cleanup(m.Close)
	created, err := m.Create(browserCreateRequest{Port: port, Path: "/", Viewport: viewport})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(10*time.Second, func() bool {
		page, ok := m.Get(created.ID)
		return ok && page.State == "ready"
	}) {
		page, _ := m.Get(created.ID)
		t.Fatalf("Chromium page did not become ready: %+v", page)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	cdp := m.cdp
	m.mu.Unlock()
	v := &browserViewer{page: p, control: make(chan browserOutbound, 16), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	p.mu.Unlock()
	return m, p, cdp
}

func evalString(t *testing.T, m *browserManager, cdp browserCDP, session, expr string) string {
	t.Helper()
	var out struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := m.call(cdp, session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &out); err != nil {
		t.Fatal(err)
	}
	return out.Result.Value
}

func evalNumber(t *testing.T, m *browserManager, cdp browserCDP, session, expr string) float64 {
	t.Helper()
	var out struct {
		Result struct {
			Value float64 `json:"value"`
		} `json:"result"`
	}
	if err := m.call(cdp, session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &out); err != nil {
		t.Fatal(err)
	}
	return out.Result.Value
}

// TestBrowserPlainASCIIKeyTypingLandsInForm guards the reported bug at the Agent
// boundary: the production control path for a non-IME keystroke
// (handleControl -> handleKey -> Input.dispatchKeyEvent with text) must actually
// type printable characters into a focused <input>. Earlier integration coverage
// only exercised the IME Input.insertText path, so the plain keyDown path went
// unverified against real Chromium.
func TestBrowserPlainASCIIKeyTypingLandsInForm(t *testing.T) {
	factory := keyboardTestCDPFactory(t)
	m, p, cdp := newKeyboardTestPage(t, factory,
		`<!doctype html><meta charset="utf-8"><title>Login</title>`+
			`<form><input id="uid" type="text"><input id="pw" type="password"></form>`,
		browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1})
	v := p.viewer

	if err := m.call(cdp, p.sessionID, "Runtime.evaluate", map[string]any{"expression": `document.querySelector('#uid').focus()`}, nil); err != nil {
		t.Fatal(err)
	}
	// "aB2" mixes lower/upper (Shift, modifier bit 8) and a digit — the printable
	// ranges handleKey must forward with a text field.
	for _, k := range []struct {
		key, code string
		mods      int
	}{
		{"a", "KeyA", 0}, {"B", "KeyB", 8}, {"2", "Digit2", 0},
	} {
		v.handleControl([]byte(fmt.Sprintf(`{"type":"key","event":"down","key":%q,"code":%q,"modifiers":%d,"repeat":false}`, k.key, k.code, k.mods)))
		v.handleControl([]byte(fmt.Sprintf(`{"type":"key","event":"up","key":%q,"code":%q,"modifiers":%d,"repeat":false}`, k.key, k.code, k.mods)))
	}
	if got := evalString(t, m, cdp, p.sessionID, `document.querySelector('#uid').value`); got != "aB2" {
		t.Fatalf("plain ASCII typing did not land in the form: got %q, want %q", got, "aB2")
	}
}

// fixedFocusPaneHTML mirrors the BrowserPane onPointer("down") wiring in
// console/src/features/browser/BrowserPane.tsx: a canvas overlaid by a 2x2
// near-transparent hidden IME <input>. The pointerdown handler MUST call
// preventDefault() before focusing the hidden input, otherwise the native
// mousedown default action clears focus to <body> (the canvas is not focusable),
// swallowing every subsequent keystroke. Keep this replica in sync with the real
// handler; it is the regression guard for that focus contract.
const fixedFocusPaneHTML = `<!doctype html><meta charset="utf-8"><title>FocusContract</title>
<style>
  html,body{margin:0;height:100%}
  .stage{position:relative;width:400px;height:300px;overflow:hidden}
  .canvas{display:block;width:100%;height:100%;background:#123;touch-action:none}
  .ime{position:absolute;z-index:2;width:2px;height:2px;padding:0;opacity:0.01;
       color:transparent;background:transparent;border:0;caret-color:transparent;pointer-events:none}
</style>
<div class="stage"><canvas class="canvas" id="canvas"></canvas><input class="ime" id="ime"></div>
<script>
  window.__keys = [];
  var canvas = document.getElementById('canvas'), ime = document.getElementById('ime');
  canvas.addEventListener('pointerdown', function (e) {
    e.preventDefault(); // keep the browser from blurring the hidden input we focus next
    try { canvas.setPointerCapture(e.pointerId); } catch (x) {}
    ime.focus({ preventScroll: true });
  });
  ime.addEventListener('keydown', function (e) { window.__keys.push(e.key); if (!e.isComposing) e.preventDefault(); });
  window.__activeId = function () { return document.activeElement ? document.activeElement.id : ''; };
</script>`

// TestBrowserCanvasClickKeepsHiddenInputFocused reproduces the root cause of the
// report ("click focuses, but keys never land") at the browser level and locks
// the fix: after a real mouse click on the canvas, the hidden IME input must stay
// focused so that keystrokes reach its keydown handler.
func TestBrowserCanvasClickKeepsHiddenInputFocused(t *testing.T) {
	factory := keyboardTestCDPFactory(t)
	m, p, cdp := newKeyboardTestPage(t, factory, fixedFocusPaneHTML,
		browserViewportRequest{Width: 400, Height: 300, DeviceScaleFactor: 1})

	for _, ev := range []string{"mousePressed", "mouseReleased"} {
		if err := m.call(cdp, p.sessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": ev, "x": 200, "y": 150, "button": "left", "buttons": 1, "clickCount": 1,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if active := evalString(t, m, cdp, p.sessionID, `window.__activeId()`); active != "ime" {
		t.Fatalf("after canvas click, focus is %q not the hidden IME input; keystrokes cannot be captured", active)
	}
	if err := m.call(cdp, p.sessionID, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyDown", "key": "a", "code": "KeyA", "text": "a", "unmodifiedText": "a",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := evalNumber(t, m, cdp, p.sessionID, `window.__keys.length`); got < 1 {
		t.Fatalf("hidden input received %v keydown events after canvas click, want >= 1", got)
	}
}

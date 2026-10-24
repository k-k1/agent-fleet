package browserx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// browserAnimationFixture serves a page that paints on every animation frame at a
// full 1200x800 canvas. Real Chromium 150 emits several screencast frames before
// it observes an ACK for such a page, which is exactly the in-flight burst that a
// single-slot buffer used to treat as a fatal "screencast backpressure" crash.
const browserAnimationFixture = `<!doctype html><meta charset="utf-8"><title>Animation</title>
<style>html,body{margin:0}#c{width:100vw;height:100vh;display:block}</style>
<canvas id="c" width="1200" height="800"></canvas>
<script>
const cv=document.getElementById('c'),ctx=cv.getContext('2d');let t=0;
function frame(){t++;ctx.fillStyle='hsl('+(t%360)+',80%,50%)';ctx.fillRect(0,0,1200,800);
for(let i=0;i<200;i++){ctx.fillStyle='hsl('+((t+i*7)%360)+',90%,60%)';ctx.fillRect((t*3+i*40)%1200,(i*17)%800,30,30);}
requestAnimationFrame(frame);}
requestAnimationFrame(frame);
</script>`

// TestBrowserScreencastBackpressureIntegration drives real Chromium 150 through the
// production manager the way the Console does: a 1200x800 animation page, a viewer
// attach, the redundant post-attach viewport message, plus mid-stream wheel and
// reload paint bursts. It asserts the Page keeps streaming JPEG frames throughout
// and is never invalidated with reason=screencast backpressure. Before the fix the
// Page crashed after 0-1 frames.
func TestBrowserScreencastBackpressureIntegration(t *testing.T) {
	cdpFactory := browserTestCDPFactory(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(browserAnimationFixture))
	}))
	defer app.Close()
	u, _ := url.Parse(app.URL)
	port, _ := strconv.Atoi(u.Port())

	m := NewBrowserManager(browserManagerConfig{
		MaxPages: 1, DetachedGrace: time.Minute, ChromiumIdle: time.Minute,
		CommandTimeout: 10 * time.Second, FrameInterval: time.Second / 12, JPEGQuality: 70,
		CDPFactory: cdpFactory,
	})
	defer m.Close()
	created, err := m.Create(browserCreateRequest{
		Port: port, Path: "/", Viewport: browserViewportRequest{Width: 1200, Height: 800, DeviceScaleFactor: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(10*time.Second, func() bool {
		page, ok := m.Get(created.ID)
		return ok && page.State == "ready"
	}) {
		page, _ := m.Get(created.ID)
		t.Fatalf("page did not become ready: %+v", page)
	}
	m.mu.Lock()
	p := m.pages[created.ID]
	m.mu.Unlock()

	v := &browserViewer{page: p, control: make(chan browserOutbound, 64), done: make(chan struct{})}
	p.mu.Lock()
	p.viewer, p.visible = v, true
	// Setting the viewer directly skips reserve(), which is what cancels the
	// detached-grace deletion timer; without this the Page is deleted after
	// DetachedGrace even though a viewer is streaming, and a slow run flakes.
	if p.expiry != nil {
		p.expiry.Stop()
		p.expiry = nil
	}
	p.mu.Unlock()
	if err := p.startScreencast(); err != nil {
		t.Fatal(err)
	}
	// Console re-sends its viewport (same size) immediately after attaching.
	v.handleControl([]byte(`{"type":"viewport","width":1200,"height":800}`))

	alive := func() {
		t.Helper()
		st, ok := m.Get(created.ID)
		if !ok {
			t.Fatalf("page was invalidated mid-stream (screencast backpressure crash)")
		}
		if st.State == "crashed" {
			t.Fatalf("page entered crashed state mid-stream")
		}
	}

	// The assertion is progress-based, not rate-based: each phase must deliver
	// browserBackpressurePhaseFrames more frames, but the deadline is generous. A
	// slow or busy runner only takes longer; a stalled ACK loop or an invalidated
	// Page still fails. The deterministic pacing and burst-absorption contracts are
	// covered without Chromium by TestBrowserScreencastAckPacesCaptureBeforeNextFrame and
	// TestBrowserScreencastAbsorbsInFlightBurst.
	frames := 0
	await := func(phase string, want int, limit time.Duration) {
		t.Helper()
		start, got := time.Now(), 0
		for got < want {
			if time.Since(start) > limit {
				t.Fatalf("%s: only %d of %d frames within %s (total %d)", phase, got, want, limit, frames)
			}
			select {
			case f := <-p.latestFrame:
				if len(f) < 2 || f[0] != 0xff || f[1] != 0xd8 {
					t.Fatalf("received a non-JPEG frame: %x", f)
				}
				frames++
				got++
			case <-time.After(40 * time.Millisecond):
			}
			alive()
		}
	}

	// Chromium start-up and the first paint are not part of what is measured.
	await("first frames", 1, browserBackpressurePhaseLimit)
	await("initial stream", browserBackpressurePhaseFrames, browserBackpressurePhaseLimit)
	// Wheel scroll and reload each unleash a fresh burst of continuous paints.
	v.handleControl([]byte(`{"type":"wheel","x":600,"y":400,"deltaX":0,"deltaY":240,"modifiers":0}`))
	await("after wheel", browserBackpressurePhaseFrames, browserBackpressurePhaseLimit)
	v.handleControl([]byte(`{"type":"reload","ignoreCache":false}`))
	await("after reload", browserBackpressurePhaseFrames, browserBackpressurePhaseLimit)

	fmt.Printf("backpressure integration: %d JPEG frames at 1200x800 with viewport/wheel/reload churn\n", frames)
}

const (
	browserBackpressurePhaseFrames = 10
	browserBackpressurePhaseLimit  = 30 * time.Second
)

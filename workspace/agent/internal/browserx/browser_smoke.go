package browserx

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RunBrowserImageSmoke exercises the production pipe-CDP launcher from the
// baked workspace-agent binary. The image smoke invokes it as the non-root dev
// user through launchPipeCDP, whose production path has no no-sandbox switch, so
// a missing/unusable Debian setuid sandbox fails exactly as it does in product.
func RunBrowserImageSmoke() error {
	return runBrowserSmoke(true)
}

func runBrowserSmoke(requireSandbox bool) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen fixture: %w", err)
	}
	fixture := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Browser image smoke `+r.URL.Path+`</title><style>@keyframes pulse{to{transform:translateX(20px)}}main{animation:pulse .05s infinite alternate}</style><main>日本語 ✓</main>`)
	})}
	go func() { _ = fixture.Serve(listener) }()
	defer func() {
		_ = fixture.Close()
		_ = listener.Close()
	}()

	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return fmt.Errorf("fixture address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("fixture port: %w", err)
	}

	config := DefaultBrowserManagerConfig()
	if !requireSandbox {
		config.CDPFactory = LaunchPipeCDPWithoutSandboxForTest
	}
	if config.MaxPages < 2 {
		return fmt.Errorf("browser page limit %d is below the product default 2", config.MaxPages)
	}
	manager := NewBrowserManager(config)
	defer manager.Close()

	pages := make([]*browserPage, 0, 2)
	for _, path := range []string{"/one", "/two"} {
		created, err := manager.Create(browserCreateRequest{
			Port: port, Path: path,
			Viewport: browserViewportRequest{Width: 800, Height: 600, DeviceScaleFactor: 1},
		})
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		manager.mu.Lock()
		page := manager.pages[created.ID]
		manager.mu.Unlock()
		if page == nil {
			return fmt.Errorf("created Page %s is not owned", path)
		}
		pages = append(pages, page)
	}

	deadline := time.Now().Add(15 * time.Second)
	for _, page := range pages {
		for time.Now().Before(deadline) {
			state := page.response()
			if state.State == "ready" && state.Title != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		state := page.response()
		if state.State != "ready" || state.Title == "" {
			return fmt.Errorf("Page did not become ready: %+v", state)
		}
		viewer := &browserViewer{page: page, control: make(chan browserOutbound, 4), done: make(chan struct{})}
		page.mu.Lock()
		page.viewer, page.visible = viewer, true
		page.mu.Unlock()
	}

	// Start both casts before waiting: this verifies simultaneous contexts and
	// rendering, not two sequential single-Page launches.
	for _, page := range pages {
		if err := page.startScreencast(); err != nil {
			return fmt.Errorf("start screencast: %w", err)
		}
	}

	result := make(chan error, len(pages))
	for _, page := range pages {
		go func(page *browserPage) { result <- verifyBrowserFrameRate(page, config.FrameInterval) }(page)
	}
	for range pages {
		if err := <-result; err != nil {
			return err
		}
	}
	fmt.Printf("browser image smoke: sandboxed pipe CDP, %d simultaneous Pages, capture interval >= %s\n", len(pages), config.FrameInterval)
	return nil
}

func verifyBrowserFrameRate(page *browserPage, interval time.Duration) error {
	duration := 1250 * time.Millisecond
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	frames := 0
	for {
		select {
		case frame := <-page.latestFrame:
			if len(frame) < 2 || frame[0] != 0xff || frame[1] != 0xd8 {
				return fmt.Errorf("Page %s emitted a non-JPEG frame", page.id)
			}
			frames++
		case <-deadline.C:
			maxFrames := int(duration/interval) + 2 // initial frame + timer scheduling tolerance
			if frames == 0 || frames > maxFrames {
				return fmt.Errorf("Page %s emitted %d frames in %s, want 1..%d", page.id, frames, duration, maxFrames)
			}
			return nil
		}
	}
}

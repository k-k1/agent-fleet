package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type browserCDPEvent struct {
	Method    string
	SessionID string
	Params    json.RawMessage
	queueSize int64
	queue     *pipeCDP
}

type browserCDP interface {
	Call(context.Context, string, any, string, any) error
	Events() <-chan browserCDPEvent
	Done() <-chan error
	Close() error
}

type browserCDPFactory func(context.Context) (browserCDP, error)

const (
	browserCDPEventQueueSize  = 256
	browserCDPEventQueueBytes = 32 << 20
	browserCDPMaxMessageBytes = 8 << 20
)

var errBrowserCDPEventOverflow = errors.New("CDP event queue overflow")

type pipeCDP struct {
	cmd        *exec.Cmd
	profile    string
	in         *os.File
	out        *os.File
	writeMu    sync.Mutex
	pendingMu  sync.Mutex
	pending    map[int64]chan cdpResponse
	events     chan browserCDPEvent
	eventBytes atomic.Int64
	done       chan error
	nextID     atomic.Int64
	closeOnce  sync.Once
}

type cdpResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func launchPipeCDP(ctx context.Context) (browserCDP, error) {
	// AF_CHROMIUM_NO_SANDBOX=1 is the documented escape hatch for hosts where no
	// Chromium sandbox can work at all — the native rootfs mode runs under an
	// unprivileged userns (no SUID helper) and its namespace-sandbox viability is
	// hardware/kernel dependent (docs/35 §35.7.2-4). Pane-only usage, loopback
	// CDP; the README spells out the tradeoff. Default remains sandboxed.
	return launchPipeCDPWithSandbox(ctx, os.Getenv("AF_CHROMIUM_NO_SANDBOX") != "1")
}

func launchPipeCDPWithoutSandboxForTest(ctx context.Context) (browserCDP, error) {
	return launchPipeCDPWithSandbox(ctx, false)
}

func launchPipeCDPWithSandbox(ctx context.Context, sandbox bool) (browserCDP, error) {
	bin, err := findChromiumBinary()
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp("", "agent-fleet-chromium-")
	if err != nil {
		return nil, fmt.Errorf("create Chromium profile: %w", err)
	}

	toChromeR, toChromeW, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, err
	}
	fromChromeR, fromChromeW, err := os.Pipe()
	if err != nil {
		_ = toChromeR.Close()
		_ = toChromeW.Close()
		_ = os.RemoveAll(profile)
		return nil, err
	}
	args := chromiumLaunchArgs(profile, sandbox)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.ExtraFiles = []*os.File{toChromeR, fromChromeW} // child fd 3 (read), fd 4 (write)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = toChromeR.Close()
		_ = toChromeW.Close()
		_ = fromChromeR.Close()
		_ = fromChromeW.Close()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start Chromium: %w", err)
	}
	_ = toChromeR.Close()
	_ = fromChromeW.Close()
	p := &pipeCDP{
		cmd: cmd, profile: profile, in: toChromeW, out: fromChromeR,
		pending: make(map[int64]chan cdpResponse), events: make(chan browserCDPEvent, browserCDPEventQueueSize), done: make(chan error, 1),
	}
	go p.readLoop()
	go func() {
		err := cmd.Wait()
		p.finish(err)
	}()
	return p, nil
}

func chromiumLaunchArgs(profile string, sandbox bool) []string {
	args := []string{
		"--headless=new", "--remote-debugging-pipe", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--disable-extensions", "--disable-features=Translate,MediaRouter,OptimizationHints",
		"--metrics-recording-only", "--mute-audio", "--disable-dev-shm-usage",
		"--password-store=basic", "--use-mock-keychain", "--user-data-dir=" + profile,
		"about:blank",
	}
	// launchPipeCDP always passes sandbox=true. Only test code can inject the
	// separate no-sandbox factory for Playwright's unprivileged cache binary.
	if !sandbox {
		args = append(args, "--no-sandbox")
	}
	return args
}

func findChromiumBinary() (string, error) {
	for _, key := range []string{"AF_CHROMIUM_BIN", "CHROMIUM_PATH"} {
		if p := os.Getenv(key); p != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
			return "", fmt.Errorf("%s does not point to a Chromium executable", key)
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	// The pinned on-demand build (install-chromium) comes BEFORE the playwright
	// cache: on a lean rootfs nothing is baked, and without this ordering a
	// user-managed playwright download would win over the verified pin
	// (docs/35 §35.7.2-4).
	if p := chromiumPinnedBinary(); p != "" {
		return p, nil
	}
	root := filepath.Join(os.Getenv("HOME"), ".cache", "ms-playwright")
	patterns := []string{
		filepath.Join(root, "chromium-*", "chrome-linux", "chrome"),
		filepath.Join(root, "chromium-*", "chrome-linux64", "chrome"),
		filepath.Join(root, "chromium_headless_shell-*", "chrome-headless-shell-linux64", "chrome-headless-shell"),
	}
	if runtime.GOARCH == "arm64" {
		patterns = append(patterns, filepath.Join(root, "chromium-*", "chrome-linux-arm64", "chrome"))
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for i := len(matches) - 1; i >= 0; i-- {
			if st, err := os.Stat(matches[i]); err == nil && st.Mode()&0111 != 0 {
				return matches[i], nil
			}
		}
	}
	return "", errors.New("Chromium executable not found (set AF_CHROMIUM_BIN or install chromium)")
}

func (p *pipeCDP) Call(ctx context.Context, method string, params any, sessionID string, result any) error {
	id := p.nextID.Add(1)
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ch := make(chan cdpResponse, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()
	p.writeMu.Lock()
	_, err = p.in.Write(append(b, 0))
	p.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("write CDP %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("CDP %s (%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("decode CDP %s: %w", method, err)
			}
		}
		return nil
	case err := <-p.done:
		if err == nil {
			err = io.EOF
		}
		return fmt.Errorf("Chromium stopped during %s: %w", method, err)
	}
}

func (p *pipeCDP) readLoop() {
	// ReadSlice with a fixed 8 MiB buffer bounds a single CDP message as well as
	// queue depth. It matches the CP browser relay's maximum Agent frame size and
	// is ample for a 1600x1200 quality-70 JPEG after base64 expansion.
	r := bufio.NewReaderSize(p.out, browserCDPMaxMessageBytes)
	for {
		b, err := r.ReadSlice(0)
		if errors.Is(err, bufio.ErrBufferFull) {
			overflow := fmt.Errorf("CDP message exceeds %d bytes", browserCDPMaxMessageBytes)
			p.finish(overflow)
			if p.cmd.Process != nil {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
			return
		}
		if len(b) > 1 {
			if dispatchErr := p.dispatch(b[:len(b)-1]); dispatchErr != nil {
				// A saturated queue means the manager can no longer enforce a
				// navigation or lifecycle decision in bounded memory. Fail the single
				// Chromium process and let BrowserManager invalidate every Page instead
				// of spawning an unbounded waiter per non-droppable event. Lossy
				// screencast frames never reach here; they are dropped in dispatch.
				p.finish(dispatchErr)
				if p.cmd.Process != nil {
					_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				}
				return
			}
		}
		if err != nil {
			if p.cmd.Process != nil {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
			p.finish(err)
			return
		}
	}
}

func (p *pipeCDP) dispatch(b []byte) error {
	var envelope struct {
		ID        int64           `json:"id"`
		Method    string          `json:"method"`
		SessionID string          `json:"sessionId"`
		Params    json.RawMessage `json:"params"`
		Result    json.RawMessage `json:"result"`
		Error     *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &envelope) != nil {
		return nil
	}
	if envelope.ID != 0 {
		p.pendingMu.Lock()
		ch := p.pending[envelope.ID]
		p.pendingMu.Unlock()
		if ch != nil {
			ch <- cdpResponse{Result: envelope.Result, Error: envelope.Error}
		}
		return nil
	}
	if envelope.Method != "" {
		eventSize := int64(len(b))
		if !p.reserveEventBytes(eventSize) {
			if browserCDPEventMustDeliver(envelope.Method) {
				return fmt.Errorf("%w: %s exceeds %d-byte queue budget", errBrowserCDPEventOverflow, envelope.Method, browserCDPEventQueueBytes)
			}
			return nil
		}
		ev := browserCDPEvent{
			Method: envelope.Method, SessionID: envelope.SessionID, Params: envelope.Params,
			queueSize: eventSize, queue: p,
		}
		select {
		case p.events <- ev:
		default:
			ev.releaseQueueBytes()
			if browserCDPEventMustDeliver(envelope.Method) {
				return fmt.Errorf("%w: %s", errBrowserCDPEventOverflow, envelope.Method)
			}
		}
	}
	return nil
}

func (p *pipeCDP) reserveEventBytes(size int64) bool {
	for {
		used := p.eventBytes.Load()
		if size < 0 || used > browserCDPEventQueueBytes-size {
			return false
		}
		if p.eventBytes.CompareAndSwap(used, used+size) {
			return true
		}
	}
}

func (ev *browserCDPEvent) releaseQueueBytes() {
	if ev.queue != nil && ev.queueSize > 0 {
		ev.queue.eventBytes.Add(-ev.queueSize)
		ev.queue, ev.queueSize = nil, 0
	}
}

// Events that enforce a security or lifecycle decision may not be silently
// dropped. Queue saturation is exceptional; terminating Chromium bounds memory and
// gives every attached Page a stable crashed transition through
// BrowserManager.handleCrash. Page.screencastFrame is deliberately absent:
// screencast frames are lossy by design, so a saturated queue drops the oldest
// frame instead of killing the whole browser.
func browserCDPEventMustDeliver(method string) bool {
	switch method {
	case "Fetch.requestPaused", "Page.fileChooserOpened", "Page.frameRequestedNavigation",
		"Page.frameStartedNavigating", "Target.targetCreated",
		"Target.targetDestroyed", "Target.targetCrashed", "Inspector.targetCrashed":
		return true
	default:
		return false
	}
}

func (p *pipeCDP) Events() <-chan browserCDPEvent { return p.events }
func (p *pipeCDP) Done() <-chan error             { return p.done }

func (p *pipeCDP) finish(err error) {
	p.closeOnce.Do(func() {
		_ = p.in.Close()
		_ = p.out.Close()
		_ = os.RemoveAll(p.profile)
		select {
		case p.done <- err:
		default:
		}
		close(p.done)
	})
}

func (p *pipeCDP) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.Call(ctx, "Browser.close", nil, "", nil)
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}

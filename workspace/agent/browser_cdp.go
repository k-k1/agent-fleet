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
}

type browserCDP interface {
	Call(context.Context, string, any, string, any) error
	Events() <-chan browserCDPEvent
	Done() <-chan error
	Close() error
}

type browserCDPFactory func(context.Context) (browserCDP, error)

type pipeCDP struct {
	cmd       *exec.Cmd
	profile   string
	in        *os.File
	out       *os.File
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan cdpResponse
	events    chan browserCDPEvent
	done      chan error
	nextID    atomic.Int64
	closeOnce sync.Once
}

type cdpResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func launchPipeCDP(ctx context.Context) (browserCDP, error) {
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
	args := []string{
		"--headless=new", "--remote-debugging-pipe", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--disable-extensions", "--disable-features=Translate,MediaRouter,OptimizationHints",
		"--metrics-recording-only", "--mute-audio",
		"--password-store=basic", "--use-mock-keychain", "--user-data-dir=" + profile,
		"about:blank",
	}
	// Production images should provide a working Chromium sandbox. The explicit
	// opt-out exists only for restricted local containers using Playwright's
	// unprivileged binary; never weaken the default silently.
	if os.Getenv("AF_CHROMIUM_NO_SANDBOX") == "1" {
		args = append(args, "--no-sandbox")
	}
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
		pending: make(map[int64]chan cdpResponse), events: make(chan browserCDPEvent, 256), done: make(chan error, 1),
	}
	go p.readLoop()
	go func() {
		err := cmd.Wait()
		p.finish(err)
	}()
	return p, nil
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
	r := bufio.NewReaderSize(p.out, 64*1024)
	for {
		b, err := r.ReadBytes(0)
		if len(b) > 1 {
			p.dispatch(b[:len(b)-1])
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

func (p *pipeCDP) dispatch(b []byte) {
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
		return
	}
	if envelope.ID != 0 {
		p.pendingMu.Lock()
		ch := p.pending[envelope.ID]
		p.pendingMu.Unlock()
		if ch != nil {
			ch <- cdpResponse{Result: envelope.Result, Error: envelope.Error}
		}
		return
	}
	if envelope.Method != "" {
		ev := browserCDPEvent{Method: envelope.Method, SessionID: envelope.SessionID, Params: envelope.Params}
		select {
		case p.events <- ev:
		default:
			// Interception events cannot be dropped, but their waiter must not block
			// this response reader: the handler sends a CDP command in reply.
			switch envelope.Method {
			case "Fetch.requestPaused", "Page.fileChooserOpened", "Page.frameRequestedNavigation", "Page.frameStartedNavigating", "Target.targetCreated", "Target.targetDestroyed", "Target.targetCrashed":
				go func() {
					select {
					case p.events <- ev:
					case <-p.done:
					}
				}()
			}
		}
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

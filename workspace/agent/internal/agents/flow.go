package agents

// Shared plumbing for PTY login flows (docs/log/23 remaining item 1 Wave F). The WebUI-driven
// logins of claude and codex all follow the same pattern: start the interactive CLI on a PTY,
// scrape its output, and round-trip with the client through a flow_id.

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// AnsiRe matches the CSI/escape sequences and lone control chars Ink emits while
// redrawing, so flow output can be scraped as plain text.
var AnsiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][AB012]|\x1b[<>=]|[\x00-\x08\x0b\x0c\x0e-\x1f]`)

// Flow is one running login flow: the interactive CLI process, the handle its output
// arrives on, and the accumulated output (scraped for URLs/codes/errors).
//
// Ptmx is non-nil for the PTY flows (StartFlow) and nil for the pipe flows
// (StartPipeFlow) — a caller that types keys at the child must use the former.
type Flow struct {
	Ptmx    *os.File
	Cmd     *exec.Cmd
	rd      *os.File // pipe flows: the read end we drain (nil for PTY flows)
	mu      sync.Mutex
	out     strings.Builder
	ended   bool
	Created time.Time
}

// StartFlow launches cmd under a PTY (TERM set by the caller on cmd.Env) and
// starts accumulating its output. A very wide PTY keeps Ink from wrapping URLs,
// so they can be scraped on one line.
func StartFlow(cmd *exec.Cmd) (*Flow, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 4000}) // wide => URL on one line

	f := &Flow{Ptmx: ptmx, Cmd: cmd, Created: time.Now()}
	go f.drain(ptmx)
	return f, nil
}

// StartPipeFlow launches cmd with NO controlling terminal: stdin is /dev/null and stdout
// and stderr are one pipe this Flow drains. Everything else — Clean, WaitFor, the flow
// store and its reaping — behaves as it does for StartFlow.
//
// It exists because a login CLI can branch on isatty, and muse's does: measured on
// 1.3.0-R3401.1, `muse login` on a PTY prints the device URL and then STOPS at
// "Press Enter to open it in your browser:" — it never starts polling for the approval,
// and there is no browser in this container to open. Off a TTY the same binary prints the
// URL and goes straight to "Waiting for approval…", self-polling exactly the way kiro's and
// cursor's device flows do. So for muse a PTY is not the neutral choice the other kinds
// found it, it is the broken one (ADR 0095 decision 9).
func StartPipeFlow(cmd *exec.Cmd) (*Flow, error) {
	rd, wr, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = nil // os/exec opens /dev/null: closed stdin, and not a terminal
	cmd.Stdout = wr
	cmd.Stderr = wr
	if err := cmd.Start(); err != nil {
		_ = rd.Close()
		_ = wr.Close()
		return nil, err
	}
	// Drop the parent's copy of the write end, or the drain below never sees EOF when the
	// child exits — which is what Ended reports on.
	_ = wr.Close()

	f := &Flow{Cmd: cmd, rd: rd, Created: time.Now()}
	go f.drain(rd)
	return f, nil
}

// drain accumulates the child's output until the handle reports EOF or an error, then
// marks the flow ended.
func (f *Flow) drain(src *os.File) {
	buf := make([]byte, 8192)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.out.Write(buf[:n])
			f.mu.Unlock()
		}
		if rerr != nil {
			f.mu.Lock()
			f.ended = true
			f.mu.Unlock()
			return
		}
	}
}

// Ended reports that the child's output handle reached EOF — for a pipe flow that means
// the process is gone (or has closed both streams), so a poll loop waiting on a
// side-channel can stop instead of spinning to its deadline.
func (f *Flow) Ended() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

// Clean returns the accumulated PTY output with ANSI/control noise removed.
func (f *Flow) Clean() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := AnsiRe.ReplaceAllString(f.out.String(), "")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Close kills the flow's process, releases its PTY, and reaps the child. The
// Wait is load-bearing: workspace-agent is not PID 1, so a killed-but-unwaited
// flow child stays a zombie forever (measured: one `[agy] <defunct>` piles up per agy /usage
// scrape — docs/log/32). Wait after SIGKILL cannot block: pty.Start
// wires *os.File fds (no copier goroutines), so it only reaps the exit status — and
// StartPipeFlow keeps that property by handing os/exec *os.File pipe ends for the same reason.
func (f *Flow) Close() {
	_ = f.Cmd.Process.Kill()
	if f.Ptmx != nil {
		_ = f.Ptmx.Close()
	}
	if f.rd != nil {
		_ = f.rd.Close()
	}
	_ = f.Cmd.Wait()
}

// WaitFor polls the flow's cleaned output until re matches or the timeout hits.
func (f *Flow) WaitFor(re *regexp.Regexp, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := re.FindString(f.Clean()); m != "" {
			return m
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// NewFlowID mints an opaque flow id the client uses to address a pending flow.
func NewFlowID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// FlowStore holds the pending flows of one provider, reaping them after ttl so
// orphan PTYs (an abandoned login) don't linger.
type FlowStore struct {
	mu    sync.Mutex
	flows map[string]*Flow
	ttl   time.Duration
}

func NewFlowStore(ttl time.Duration) *FlowStore {
	return &FlowStore{flows: map[string]*Flow{}, ttl: ttl}
}

// Reap closes and drops every flow older than the store's TTL.
func (s *FlowStore) Reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, f := range s.flows {
		if time.Since(f.Created) > s.ttl {
			f.Close()
			delete(s.flows, id)
		}
	}
}

// Put registers f under a fresh flow id and returns the id.
func (s *FlowStore) Put(f *Flow) string {
	id := NewFlowID()
	s.mu.Lock()
	s.flows[id] = f
	s.mu.Unlock()
	return id
}

// Get returns the flow for id WITHOUT removing it (nil when unknown/expired), for a poll that
// only wants to look at a flow it intends to keep waiting on. Take-then-Put is not the same
// thing and is a trap: Put mints a fresh id, orphaning the one the client is polling with.
func (s *FlowStore) Get(id string) *Flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flows[id]
}

// Take removes and returns the flow for id (nil when unknown/expired). The
// caller owns closing it.
func (s *FlowStore) Take(id string) *Flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[id]
	delete(s.flows, id)
	return f
}

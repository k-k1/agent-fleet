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

// Flow is one running PTY login flow: the interactive CLI process, its PTY, and
// the accumulated output (scraped for URLs/codes/errors).
type Flow struct {
	Ptmx    *os.File
	Cmd     *exec.Cmd
	mu      sync.Mutex
	out     strings.Builder
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
	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				f.mu.Lock()
				f.out.Write(buf[:n])
				f.mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()
	return f, nil
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
// wires *os.File fds (no copier goroutines), so it only reaps the exit status.
func (f *Flow) Close() {
	_ = f.Cmd.Process.Kill()
	_ = f.Ptmx.Close()
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

// Take removes and returns the flow for id (nil when unknown/expired). The
// caller owns closing it.
func (s *FlowStore) Take(id string) *Flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[id]
	delete(s.flows, id)
	return f
}

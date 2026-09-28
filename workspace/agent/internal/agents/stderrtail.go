package agents

// Bounded stderr tail for managed CLI children. A child that dies during start-up or the
// handshake usually says why on stderr, and without this the only trace left is a generic
// "failed to start". The tail is kept out of the Agent log: it rides on the start error as
// StartError.Stderr, and only the HTTP response to the Console renders it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
)

const (
	tailMaxLines = 256
	tailMaxBytes = 32 << 10
	// tailMaxLine is the longest line kept; a longer one is replaced by a marker. It is below
	// StartErrStderrBudget so a kept line always fits the error whole.
	tailMaxLine = 1 << 10
	// StartErrStderrBudget and StartErrStderrLines bound what a start error carries: the last
	// lines of a crash are where the cause is, and the whole of it has to fit an error toast
	// without scrolling.
	StartErrStderrBudget = 2 << 10
	StartErrStderrLines  = 12
	// StartErrStderrWait is how long a failed start waits for a dying child's stderr to reach
	// EOF before taking the snapshot.
	StartErrStderrWait = 300 * time.Millisecond
)

// StderrTail keeps the last lines a child wrote to stderr. The reader goroutine only appends
// raw lines under the lock, so a chatty child never blocks on a full pipe; redaction happens
// in Snapshot, on the caller's goroutine.
type StderrTail struct {
	mu      sync.Mutex
	lines   []string
	size    int
	partial []byte
	skipped int // bytes of the current over-long line, 0 when not skipping
	done    chan struct{}
	// Private-key blocks are tracked as the bytes arrive, so no line of one ever enters the
	// ring: matching a PEM pattern at snapshot time fails once a delimiter is gone (its line
	// was over-long, or it was evicted), and a guess from the body's shape both leaks a lone
	// surviving line and swallows ordinary diagnostics.
	inKey    bool   // inside a BEGIN … PRIVATE KEY block
	keyLine  bool   // the current line touched a key block
	keyMark  bool   // the last kept line is the block's redaction mark
	keyCarry []byte // end of the current line, so a delimiter split across reads is still seen
	// reaped and settled are the two halves of "nobody will ask for a snapshot again": the child
	// was waited for (Release), and the start either succeeded or took its failure snapshot
	// (Settle / Wrap). They arrive from different goroutines in either order: a CLI that dies
	// at once is reaped by the watch goroutine before spawn's initialize call has even failed.
	// Once both are in, released is set: the lines are dropped and the reader, which keeps
	// draining, discards.
	reaped, settled, released bool
}

// StartWithStderrTail starts cmd with its stderr going to a fresh StderrTail.
//
// The pipe is an *os.File rather than cmd.StderrPipe or an io.Writer: StderrPipe's read end
// is closed by Wait, which drivers call concurrently from their watch goroutine, so the
// output of a child that dies at once would be lost; with an io.Writer, exec's copy goroutine
// makes Wait block for as long as a grandchild holds stderr open.
func StartWithStderrTail(cmd *exec.Cmd) (*StderrTail, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	_ = w.Close() // the child holds its own copy; EOF arrives when every holder exits
	t := &StderrTail{done: make(chan struct{})}
	go t.read(r)
	return t, nil
}

// Release is called once the child has been reaped. Together with Settle it drops the kept
// lines, and the reader discards whatever still arrives. The reader itself runs on until EOF,
// i.e. for as long as a grandchild that inherited stderr is alive. Closing the read end instead
// would turn that grandchild's next write into SIGPIPE/EPIPE and kill a background task that
// ran fine when stderr was /dev/null; an idle goroutine and one fd per such grandchild is the
// cheaper side.
func (t *StderrTail) Release() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.reaped = true
	t.dropIfDoneLocked()
	t.mu.Unlock()
}

// Settle marks the start as over: it succeeded, or its failure snapshot has been taken. Starters
// defer it right after StartWithStderrTail, so every return path settles after its Wrap.
func (t *StderrTail) Settle() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.settled = true
	t.dropIfDoneLocked()
	t.mu.Unlock()
}

func (t *StderrTail) dropIfDoneLocked() {
	if t.reaped && t.settled && !t.released {
		t.released = true
		t.lines, t.size, t.partial, t.skipped = nil, 0, nil, 0
		t.keyCarry = nil // may hold raw key bytes
	}
}

func (t *StderrTail) read(r io.ReadCloser) {
	defer close(t.done)
	defer r.Close()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.write(buf[:n])
		}
		if err != nil {
			t.mu.Lock()
			if !t.released {
				t.flushLocked()
			}
			t.mu.Unlock()
			return
		}
	}
}

// write splits p into lines; it is the only mutator besides the final flush.
func (t *StderrTail) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		t.scanKeyLocked(chunk)
		if t.skipped > 0 || len(t.partial)+len(chunk) > tailMaxLine {
			t.skipped += len(t.partial) + len(chunk)
			t.partial = t.partial[:0]
		} else {
			t.partial = append(t.partial, chunk...)
		}
		if i < 0 {
			return
		}
		t.flushLocked()
		p = p[i+1:]
	}
}

// pemDelimRe matches a private-key block's BEGIN or END delimiter, in any case. The label
// between them ("RSA ", "ENCRYPTED ", …) is bounded so that pemCarry can always hold a whole
// delimiter split across reads; real labels are a word or two, far below the bound.
var pemDelimRe = regexp.MustCompile(`(?i)-----(BEGIN|END)[A-Z ]{0,64}PRIVATE KEY-----`)

const pemCarry = len("-----BEGIN") + 64 + len("PRIVATE KEY-----")

// scanKeyLocked follows the key-block state through one segment of the current line, whether
// that line is being kept or skipped as over-long.
func (t *StderrTail) scanKeyLocked(chunk []byte) {
	if t.inKey {
		t.keyLine = true
	}
	buf := make([]byte, 0, len(t.keyCarry)+len(chunk))
	buf = append(append(buf, t.keyCarry...), chunk...)
	for _, m := range pemDelimRe.FindAllSubmatchIndex(buf, -1) {
		if m[1] <= len(t.keyCarry) {
			continue // ended inside the carry: already seen with the previous segment
		}
		t.keyLine = true
		t.inKey = strings.EqualFold(string(buf[m[2]:m[3]]), "BEGIN")
	}
	if len(buf) > pemCarry {
		buf = buf[len(buf)-pemCarry:]
	}
	t.keyCarry = append(t.keyCarry[:0], buf...)
}

// flushLocked ends the current line (a trailing partial line counts at EOF).
func (t *StderrTail) flushLocked() {
	hidden := t.keyLine || t.inKey
	t.keyLine, t.keyCarry = false, t.keyCarry[:0]
	if hidden {
		// The whole block, delimiters included, leaves one mark behind.
		t.partial, t.skipped = t.partial[:0], 0
		if !t.keyMark {
			t.keyMark = true
			t.appendLocked(bridge.RedactedMark)
		}
		return
	}
	var line string
	switch {
	case t.skipped > 0:
		line = fmt.Sprintf("[line of %d bytes omitted]", t.skipped)
	case len(t.partial) > 0:
		line = string(t.partial)
	default:
		return
	}
	t.partial = t.partial[:0]
	t.skipped = 0
	t.keyMark = false
	t.appendLocked(line)
}

func (t *StderrTail) appendLocked(line string) {
	t.lines = append(t.lines, line)
	t.size += len(line) + 1
	for len(t.lines) > tailMaxLines || t.size > tailMaxBytes {
		t.size -= len(t.lines[0]) + 1
		t.lines = t.lines[1:]
	}
}

// ansiRe matches terminal escape sequences (colours, cursor moves) CLIs write even to a pipe.
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// Snapshot returns the redacted tail, at most maxLines lines and budget bytes. It first waits
// up to wait for EOF, so a child that is already dying gets to say its last words.
//
// Redact, then truncate: cutting first can leave half a secret in the kept fragment, where no
// pattern recognises it any more. Pattern redaction still misses secrets written in prose, so
// treat the result as sensitive and keep it out of logs.
func (t *StderrTail) Snapshot(wait time.Duration, maxLines, budget int) string {
	if t == nil {
		return ""
	}
	if wait > 0 {
		select {
		case <-t.done:
		case <-time.After(wait):
		}
	}
	t.mu.Lock()
	lines := append([]string(nil), t.lines...)
	if t.keyLine || t.inKey {
		if !t.keyMark {
			lines = append(lines, bridge.RedactedMark)
		}
	} else if t.skipped > 0 {
		lines = append(lines, fmt.Sprintf("[line of %d bytes omitted]", t.skipped))
	} else if len(t.partial) > 0 {
		lines = append(lines, string(t.partial))
	}
	t.mu.Unlock()

	for i, l := range lines {
		l = ansiRe.ReplaceAllString(l, "")
		lines[i] = strings.Map(func(r rune) rune {
			if r == '\t' || r >= ' ' && r != 0x7f {
				return r
			}
			return -1
		}, l)
	}
	// Line by line: a line-local pattern let loose across a newline swallows the next line,
	// e.g. "API_KEY=" followed by "Unauthorized". Key blocks never reached the ring.
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, bridge.ScrubSecrets(l))
		}
	}
	if len(kept) > maxLines {
		kept = kept[len(kept)-maxLines:]
	}
	return tailWithin(kept, budget)
}

// tailWithin keeps whole lines from the end while they fit budget; when even the last line
// does not fit, its end is kept.
func tailWithin(lines []string, budget int) string {
	if len(lines) == 0 || budget <= 0 {
		return ""
	}
	size, from := 0, len(lines)
	for from > 0 {
		n := len(lines[from-1])
		if from < len(lines) {
			n++ // the joining newline
		}
		if size+n > budget {
			break
		}
		size += n
		from--
	}
	if from == len(lines) {
		last := lines[len(lines)-1]
		cut := len(last) - budget
		for cut < len(last) && !isRuneStart(last[cut]) {
			cut++
		}
		return last[cut:]
	}
	return strings.Join(lines[from:], "\n")
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Wrap attaches the tail to a start failure. Call it before cleaning the child up: a stop
// sequence can make the CLI print shutdown noise that pushes the real cause out of the budget.
// err is returned unchanged when the tail is empty.
func (t *StderrTail) Wrap(err error) error {
	if err == nil {
		return nil
	}
	s := t.Snapshot(StartErrStderrWait, StartErrStderrLines, StartErrStderrBudget)
	if s == "" {
		return err
	}
	return &StartError{Err: err, Stderr: s}
}

// StartError is a managed child's start failure with the end of its stderr. Error() leaves the
// tail out on purpose — callers log start errors — so only writeRuntimeErr, which answers the
// Console, renders it.
type StartError struct {
	Err    error
	Stderr string // redacted, bounded; still sensitive
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// StartErrStderr returns the stderr tail carried anywhere in err's chain, or "".
func StartErrStderr(err error) string {
	var se *StartError
	if errors.As(err, &se) {
		return se.Stderr
	}
	return ""
}

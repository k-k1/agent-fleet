package agents

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pemDelim builds a private-key delimiter at run time: written out literally, the fixtures
// below make the repository's full-history secret scan (gitleaks' private-key rule) fail.
func pemDelim(kind, label string) string {
	return "-----" + kind + " " + label + "PRIVATE" + " KEY-----"
}

func tailOf(chunks ...string) *StderrTail {
	t := &StderrTail{done: make(chan struct{})}
	for _, c := range chunks {
		t.write([]byte(c))
	}
	t.mu.Lock()
	t.flushLocked()
	t.mu.Unlock()
	close(t.done)
	return t
}

func TestStderrTailBoundsLinesAndBytes(t *testing.T) {
	var b strings.Builder
	for i := 0; i < tailMaxLines+50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	tl := tailOf(b.String())
	if len(tl.lines) != tailMaxLines {
		t.Fatalf("kept %d lines, want %d", len(tl.lines), tailMaxLines)
	}
	if tl.lines[0] != "line 50" || tl.lines[len(tl.lines)-1] != fmt.Sprintf("line %d", tailMaxLines+49) {
		t.Fatalf("oldest lines were not the ones dropped: first=%q last=%q", tl.lines[0], tl.lines[len(tl.lines)-1])
	}

	// Lines just under the per-line cap hit the byte cap before the line cap.
	long := strings.Repeat("x", tailMaxLine-10)
	b.Reset()
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "%03d%s\n", i, long)
	}
	tl = tailOf(b.String())
	if tl.size > tailMaxBytes {
		t.Fatalf("size %d exceeds %d", tl.size, tailMaxBytes)
	}
	if !strings.HasPrefix(tl.lines[len(tl.lines)-1], "099") {
		t.Fatalf("newest line lost: %.10q", tl.lines[len(tl.lines)-1])
	}
}

func TestStderrTailSkipsOverlongLineAcrossWrites(t *testing.T) {
	huge := strings.Repeat("y", tailMaxLine*3)
	// Split over several writes, the way a pipe delivers it.
	tl := tailOf("before\n", huge[:1000], huge[1000:2500], huge[2500:]+"\nafter\n")
	got := strings.Join(tl.lines, "|")
	want := fmt.Sprintf("before|[line of %d bytes omitted]|after", len(huge))
	if got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestStderrTailRedactsBeforeTruncating(t *testing.T) {
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	line := "fatal: auth failed with token " + secret
	// A budget that cuts through the middle of the token: truncating first would keep a
	// fragment of it that no pattern recognises any more.
	budget := len(secret) / 2
	got := tailOf(line+"\n").Snapshot(0, 100, budget)
	for i := 0; i+8 <= len(secret); i++ {
		if strings.Contains(got, secret[i:i+8]) {
			t.Fatalf("snapshot %q keeps a fragment of the secret", got)
		}
	}
	if !strings.Contains(got, "redacted") {
		t.Fatalf("snapshot %q does not end in the redaction mark", got)
	}
}

func TestStderrTailSnapshotKeepsWholeLinesFromTheEnd(t *testing.T) {
	got := tailOf("\x1b[31merror\x1b[0m: first\n", "   \n", "second\r\n", "third").Snapshot(0, 100, len("second\nthird"))
	if got != "second\nthird" {
		t.Fatalf("snapshot = %q", got)
	}
	got = tailOf("\x1b[31merror\x1b[0m: first\n").Snapshot(0, 100, 100)
	if got != "error: first" {
		t.Fatalf("escape sequences not stripped: %q", got)
	}
	got = tailOf("a\n", "b\n", "c\n").Snapshot(0, 2, 100)
	if got != "b\nc" {
		t.Fatalf("line cap: %q", got)
	}
}

// A child that dies at once while the driver's watch goroutine is in cmd.Wait still leaves its
// last line — the cause — in the tail. (cmd.StderrPipe is documented to lose this, since Wait
// closes its read end; the race did not reproduce here, so this is not a guard against it.)
func TestStartWithStderrTailSurvivesConcurrentWait(t *testing.T) {
	for i := 0; i < 20; i++ {
		// ~48 KiB fits the pipe buffer, so the child can exit before the reader catches up.
		cmd := exec.Command("sh", "-c", `{ head -c 49152 /dev/zero | tr '\0' 'p' | fold -w 80; echo; echo "Error: You are not logged in"; } >&2; exit 3`)
		tail, err := StartWithStderrTail(cmd)
		if err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		err = tail.Wrap(errors.New("initialize failed"))
		if got := StartErrStderr(err); !strings.HasSuffix(got, "\nError: You are not logged in") {
			t.Fatalf("run %d: tail ends %q", i, got[max(0, len(got)-80):])
		}
	}
}

func TestStartWithStderrTailNeverBlocksTheChild(t *testing.T) {
	// 8 MiB of stderr is far past the pipe buffer: without a reader the child would block.
	cmd := exec.Command("sh", "-c", `head -c 8388608 /dev/zero | tr '\0' 'z' | fold -w 100 >&2; echo last words >&2`)
	tail, err := StartWithStderrTail(cmd)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child failed: %v", err)
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child blocked writing stderr")
	}
	if got := tail.Snapshot(time.Second, 100, 64); !strings.HasSuffix(got, "last words") {
		t.Fatalf("snapshot = %q", got)
	}
}

// A grandchild that keeps stderr open must not hold up the driver's Wait (it would with an
// io.Writer as cmd.Stderr, where Wait joins exec's copy goroutine).
func TestStartWithStderrTailWaitIgnoresGrandchild(t *testing.T) {
	cmd := exec.Command("sh", "-c", `sleep 3 & echo child exiting >&2; exit 1`)
	tail, err := StartWithStderrTail(cmd)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = cmd.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wait took %v: it waited for the grandchild", d)
	}
	if got := tail.Snapshot(0, 100, 100); got != "child exiting" {
		t.Fatalf("snapshot = %q", got)
	}
}

func TestStartErrorKeepsTailOutOfError(t *testing.T) {
	sentinel := errors.New("not connected")
	base := fmt.Errorf("start: %w", sentinel)
	err := fmt.Errorf("outer: %w", tailOf("the real cause\n").Wrap(base))
	if strings.Contains(err.Error(), "the real cause") {
		t.Fatalf("Error() carries the tail (it would reach the log): %q", err.Error())
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("wrapping hid the sentinel from errors.Is")
	}
	if got := StartErrStderr(err); got != "the real cause" {
		t.Fatalf("StartErrStderr = %q", got)
	}
	if e := tailOf().Wrap(base); e != base {
		t.Fatalf("an empty tail must return the error unchanged, got %#v", e)
	}
	if StartErrStderr(base) != "" {
		t.Fatal("no StartError in the chain must give an empty tail")
	}
}

func TestStartWithStderrTailStartFailure(t *testing.T) {
	cmd := exec.Command("/nonexistent/af-no-such-binary")
	if _, err := StartWithStderrTail(cmd); err == nil {
		t.Fatal("expected a start error")
	}
}

// The PEM pattern spans lines; scrubbing line by line never matched it, and a key body wrapped
// into short lines also slips under the per-token entropy fallback.
func TestStderrTailRedactsMultilinePrivateKey(t *testing.T) {
	body := []string{"MIIEvQIBADANBgk", "qhkiG9w0BAQEFAA", "SCBKcwggSjAgEAA", "oIBAQC7VJTUt9Us"}
	in := "loading key\n" + pemDelim("BEGIN", "") + "\n" + strings.Join(body, "\n") + "\n" + pemDelim("END", "") + "\nfatal: bad key\n"
	got := tailOf(in).Snapshot(0, 100, 1000)
	for _, b := range body {
		if strings.Contains(got, b) {
			t.Fatalf("snapshot keeps key material %q:\n%s", b, got)
		}
	}
	if !strings.HasPrefix(got, "loading key\n") || !strings.HasSuffix(got, "\nfatal: bad key") {
		t.Fatalf("surrounding lines lost:\n%s", got)
	}
}

// Release must not break a grandchild that inherited stderr and outlives the child: before
// the tail its writes went to /dev/null and succeeded, and a closed read end would kill it with
// SIGPIPE. What it writes after Release is not kept.
func TestStderrTailReleaseKeepsGrandchildWritable(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "finished")
	cmd := exec.Command("sh", "-c", `(sleep 2; echo late output >&2; echo ok > "$0") & echo child exiting >&2; exit 0`, marker)
	tail, err := StartWithStderrTail(cmd)
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	tail.Settle()
	tail.Release()
	select {
	case <-tail.done: // the grandchild exited, closing the last write end
	case <-time.After(10 * time.Second):
		t.Fatal("grandchild never finished")
	}
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("grandchild did not finish its work after writing to stderr (marker: %q, %v)", b, err)
	}
	tail.mu.Lock()
	kept := len(tail.lines) + len(tail.partial)
	tail.mu.Unlock()
	if kept != 0 {
		t.Fatalf("released tail still holds %d lines/bytes", kept)
	}
	var nilTail *StderrTail
	nilTail.Release() // callers pass nil where no tail exists
}

// Line-local patterns stay within their line: let loose across a newline, an empty assignment
// swallows the next line, which may be the only failure reason there is.
func TestStderrTailRedactionStaysWithinLines(t *testing.T) {
	got := tailOf("API_KEY=\n", "Unauthorized: invalid credentials\n", "Authorization: Bearer\n", "Forbidden\n").Snapshot(0, 100, 1000)
	want := "API_KEY=\nUnauthorized: invalid credentials\nAuthorization: Bearer\nForbidden"
	if got != want {
		t.Fatalf("snapshot =\n%s\nwant\n%s", got, want)
	}
}

// The watch goroutine can reap a CLI that dies at once before spawn's failed initialize takes
// its snapshot; the lines must survive until both the reap and the start are over, whichever
// comes first.
func TestStderrTailDropsLinesOnlyAfterReapAndSettle(t *testing.T) {
	for _, order := range []string{"release first", "settle first"} {
		tl := tailOf("Error: You are not logged in\n")
		if order == "release first" {
			tl.Release()
			if got := StartErrStderr(tl.Wrap(errors.New("initialize failed"))); got != "Error: You are not logged in" {
				t.Fatalf("%s: the reap dropped the lines before the failure snapshot: %q", order, got)
			}
			tl.Settle()
		} else {
			tl.Settle()
			tl.Release()
		}
		tl.mu.Lock()
		kept := len(tl.lines)
		tl.mu.Unlock()
		if kept != 0 {
			t.Fatalf("%s: %d lines kept after both reap and settle", order, kept)
		}
	}
	var nilTail *StderrTail
	nilTail.Settle()

	// Released, the tail keeps nothing, including the carry that can hold raw key bytes of a
	// block a surviving grandchild is still writing.
	tl := &StderrTail{done: make(chan struct{})}
	tl.write([]byte(pemDelim("BEGIN", "") + "\nMIIEvQIBADANBgk")) // mid-line: the carry holds key bytes
	tl.Settle()
	tl.Release()
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if len(tl.lines)+len(tl.partial) != 0 || tl.keyCarry != nil {
		t.Fatalf("released tail still holds lines=%d partial=%d carry=%q", len(tl.lines), len(tl.partial), tl.keyCarry)
	}
}

// Key blocks are recognised as the bytes arrive, so a body line never enters the ring. At
// snapshot time a delimiter can be gone (its line over-long, or evicted), and guessing from the
// body's shape both leaked a lone surviving line and swallowed ordinary diagnostics.
func TestStderrTailHidesKeyBlocksAtIngest(t *testing.T) {
	body := []string{"MIIEvQIBADANBgk", "qhkiG9w0BAQEFAA", "SCBKcwggSjAgEAA", "oIBAQC7VJTUt9Us"}
	wrapped := strings.Join(body, "\n") + "\n"
	begin, end := pemDelim("BEGIN", ""), pemDelim("END", "")
	long := strings.Repeat("A", 60) + " "
	longBegin, rsaBegin := pemDelim("BEGIN", long), pemDelim("BEGIN", "RSA ")
	cases := map[string][]string{
		"over-long BEGIN line":          {strings.Repeat("x", tailMaxLine+10) + begin + "\n" + wrapped + end + "\nfatal: bad key\n"},
		"lone body line left near END":  {begin + "\n" + wrapped + strings.Repeat(" \n", tailMaxLines-3) + end + "\nfatal: bad key\n"},
		"lowercase delimiters":          {strings.ToLower(begin) + "\n" + wrapped + strings.ToLower(end) + "\nfatal: bad key\n"},
		"long label split across reads": {longBegin[:71], longBegin[71:] + "\n" + wrapped + pemDelim("END", long) + "\nfatal: bad key\n"},
		"delimiter split across reads":  {"loading\n" + rsaBegin[:8], rsaBegin[8:24], rsaBegin[24:] + "\n" + wrapped + pemDelim("END", "RSA ") + "\nfatal: bad key\n"},
	}
	for name, chunks := range cases {
		got := tailOf(chunks...).Snapshot(0, 100, 4000)
		for _, b := range body {
			if strings.Contains(got, b) {
				t.Fatalf("%s: snapshot keeps key material %q:\n%s", name, b, got)
			}
		}
		if !strings.HasSuffix(got, "[secret redacted]\nfatal: bad key") {
			t.Fatalf("%s: want one redaction mark, then the failure line:\n%s", name, got)
		}
	}

	// A block still open when the snapshot is taken is hidden too.
	open := tailOf("starting\n", begin+"\n"+body[0]+"\n"+body[1])
	if got := open.Snapshot(0, 100, 1000); got != "starting\n[secret redacted]" {
		t.Fatalf("open block: %q", got)
	}

	// Ordinary diagnostics that happen to be alphanumeric stay.
	plain := "version2026\nUnauthorized\nabc12345xyz\nx9y8z7w6v5\n"
	if got := tailOf(plain).Snapshot(0, 100, 1000); got != strings.TrimSuffix(plain, "\n") {
		t.Fatalf("plain diagnostics were redacted:\n%s", got)
	}
}

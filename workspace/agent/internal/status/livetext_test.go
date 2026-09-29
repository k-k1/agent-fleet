package status

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// app appends one flush of prompt p1.
func app(sid, turn, msg string, index int, final bool, delta string) {
	AppendLiveText(sid, LiveFlush{Prompt: "p1", Turn: turn, Msg: msg, Index: index, Final: final, Delta: delta})
}

func readLive(t *testing.T, sid string) LiveReply {
	t.Helper()
	lr, ok := ReadLiveText(sid)
	if !ok {
		t.Fatalf("ReadLiveText(%q): nothing read", sid)
	}
	return lr
}

// claude keeps several MessageDisplay hooks in flight, so a later flush can be appended before
// an earlier one. The text follows the flush counter, not the file.
func TestLiveTextAssemblesInIndexOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 1, false, "second\n")
	app("s", "t1", "m1", 0, false, "first\n")
	lr := readLive(t, "s")
	if lr.Text != "first\nsecond\n" || lr.Final || lr.Prompt != "p1" {
		t.Fatalf("got %+v, want the two flushes in index order, not final, of prompt p1", lr)
	}
}

// A missing index is a flush whose hook is still running. What comes after it waits, or its
// line would show above the one it follows.
func TestLiveTextStopsAtAGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "a\n")
	app("s", "t1", "m1", 2, false, "c\n")
	if lr := readLive(t, "s"); lr.Text != "a\n" {
		t.Fatalf("with flush 1 missing got %q, want only flush 0", lr.Text)
	}
	app("s", "t1", "m1", 1, false, "b\n")
	if lr := readLive(t, "s"); lr.Text != "a\nb\nc\n" {
		t.Fatalf("once flush 1 landed got %q, want all three", lr.Text)
	}
}

// The same holds for the message's first flush: until it lands, the second one cannot be shown
// as the start of the reply.
func TestLiveTextWaitsForTheFirstFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 1, false, "second\n")
	if lr, ok := ReadLiveText("s"); ok && lr.Text != "" {
		t.Fatalf("with flush 0 missing got %q, want nothing yet", lr.Text)
	}
	app("s", "t1", "m1", 0, false, "first\n")
	if lr := readLive(t, "s"); lr.Text != "first\nsecond\n" {
		t.Fatalf("got %q, want both flushes", lr.Text)
	}
}

// A message that ends on a newline gets an empty final flush. It carries no text but is what
// says the message is complete, so it must be recorded.
func TestLiveTextFinalFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "line\n")
	before := time.Now()
	app("s", "t1", "m1", 1, true, "")
	lr := readLive(t, "s")
	if !lr.Final || lr.Text != "line\n" {
		t.Fatalf("got %+v, want a final message with the one line", lr)
	}
	if lr.FinalAt.Before(before) || !lr.LastAt.Equal(lr.FinalAt) {
		t.Fatalf("FinalAt %v / LastAt %v, want both at the final flush (after %v)", lr.FinalAt, lr.LastAt, before)
	}
}

func TestLiveTextSkipsEmptyNonFinalFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "")
	app("s", "t1", "", 0, false, "no message id")
	if _, ok := ReadLiveText("s"); ok {
		t.Fatal("a flush with no text (or no message id) was recorded")
	}
}

// The final flush of one message can land after the next message has begun: its hook process
// ran late. The newest message is the one that started last.
func TestLiveTextNewestMessageIsTheLastToStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "one\n")
	app("s", "t1", "m2", 0, false, "two\n")
	app("s", "t1", "m1", 1, true, "tail")
	lr := readLive(t, "s")
	if lr.Text != "two\n" || lr.Final {
		t.Fatalf("got %+v, want m2 (still streaming), not m1's straggling tail", lr)
	}
	// The next turn's first message wins over what is left of the previous turn.
	app("s", "t2", "m1", 0, false, "next turn\n")
	if lr := readLive(t, "s"); lr.Text != "next turn\n" {
		t.Fatalf("got %q, want the new turn's message", lr.Text)
	}
}

// fill writes one message of n 1 KiB flushes, enough to push a file past the read window.
func fill(sid, msg string, n int) {
	line := strings.Repeat("x", 1000) + "\n"
	for i := range n {
		app(sid, "t1", msg, i, false, fmt.Sprintf("%04d", i)+line)
	}
}

// A message longer than the read window is still read from its first flush: the read widens
// until that flush is in it.
func TestLiveTextLongMessageIsReadWhole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := liveTextWindow/1000 + 50
	fill("s", "m1", n)
	lr := readLive(t, "s")
	if !strings.HasPrefix(lr.Text, "0000") || !strings.Contains(lr.Text, fmt.Sprintf("%04d", n-1)) {
		t.Fatalf("got %d bytes starting %q, want all %d flushes from the first", len(lr.Text), lr.Text[:4], n)
	}
}

// In a file past the window, the newest message's second flush landing before its first must
// still wait for it: the window holding only the second is not a message cut off at the head.
func TestLiveTextBigFileStillWaitsForTheFirstFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fill("s", "m1", liveTextWindow/1000+50)
	app("s", "t1", "m2", 1, false, "second\n")
	if lr, ok := ReadLiveText("s"); ok && lr.Text != "" {
		t.Fatalf("got %q before m2's first flush landed, want nothing", lr.Text)
	}
	app("s", "t1", "m2", 0, false, "first\n")
	if lr := readLive(t, "s"); lr.Text != "first\nsecond\n" {
		t.Fatalf("got %q, want m2 whole", lr.Text)
	}
}

// A final flush that straggles in after the next message has written a window's worth: inside
// the first window it is the message seen last, i.e. it would pass for the newest. Reading back
// to the heads shows m1 began first.
func TestLiveTextStragglerIsNotTheNewestMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := liveTextWindow/1000 + 50
	fill("s", "m1", n)
	fill("s", "m2", n)
	app("s", "t1", "m1", n, true, "late end of m1")
	lr := readLive(t, "s")
	if !strings.HasPrefix(lr.Text, "0000") || strings.Contains(lr.Text, "late end of m1") || lr.Final {
		t.Fatalf("got %d bytes (%q…, final=%v), want m2 from its first flush", len(lr.Text), lr.Text[:min(12, len(lr.Text))], lr.Final)
	}
}

func TestLiveTextStopsGrowingAtTheCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(liveTexts.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	path := liveTexts.Path("s")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", liveTextFileCap)), 0o600); err != nil {
		t.Fatal(err)
	}
	app("s", "t1", "m1", 0, false, "x\n")
	if fi, err := os.Stat(path); err != nil || fi.Size() != liveTextFileCap {
		t.Fatalf("the file grew past its cap (size %v, err %v)", fi.Size(), err)
	}
}

// Many writers at once must neither lose a record nor interleave two of them inside a line.
// (The real writers are separate processes; O_APPEND gives them the same guarantee.)
func TestLiveTextConcurrentAppends(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const n = 200
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { app("s", "t1", "m1", i, false, fmt.Sprintf("%03d\n", i)) })
	}
	wg.Wait()
	var want strings.Builder
	for i := range n {
		fmt.Fprintf(&want, "%03d\n", i)
	}
	if lr := readLive(t, "s"); lr.Text != want.String() {
		t.Fatalf("concurrent appends lost or mangled records: got %d bytes, want %d", len(lr.Text), want.Len())
	}
}

// The previous message of the turn tells the reader which transcript rows are too old to be the
// newest message's: its final flush is the boundary. The first message of a turn has none, and
// another turn's message does not count.
func TestLiveTextPrevFinalAt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "first\n")
	if lr := readLive(t, "s"); !lr.PrevFinalAt.IsZero() {
		t.Fatalf("the first message got PrevFinalAt %v, want zero", lr.PrevFinalAt)
	}
	app("s", "t1", "m1", 1, true, "")
	m1 := readLive(t, "s").FinalAt
	app("s", "t1", "m2", 0, false, "second\n")
	if lr := readLive(t, "s"); !lr.PrevFinalAt.Equal(m1) {
		t.Fatalf("got PrevFinalAt %v, want m1's final flush %v", lr.PrevFinalAt, m1)
	}
	app("s", "t2", "m1", 0, false, "next turn\n")
	if lr := readLive(t, "s"); !lr.PrevFinalAt.IsZero() {
		t.Fatalf("a new turn's first message got PrevFinalAt %v from the previous turn", lr.PrevFinalAt)
	}
}

// The running turn's prompt id is a plain overwrite, and it outlives RemoveLiveText: the next
// turn's first hook is what replaces it.
func TestLivePrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := ReadLivePrompt("s"); got != "" {
		t.Fatalf("got %q before anything was written", got)
	}
	WriteLivePrompt("s", "p1")
	WriteLivePrompt("s", "p2")
	RemoveLiveText("s")
	if got := ReadLivePrompt("s"); got != "p2" {
		t.Fatalf("got %q, want the last written id p2", got)
	}
}

func TestRemoveLiveText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app("s", "t1", "m1", 0, false, "x\n")
	RemoveLiveText("s")
	if _, ok := ReadLiveText("s"); ok {
		t.Fatal("the streamed text survived RemoveLiveText")
	}
}

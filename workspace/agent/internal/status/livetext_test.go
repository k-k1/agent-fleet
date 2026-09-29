package status

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	AppendLiveText("s", "t1", "m1", 1, false, "second\n")
	AppendLiveText("s", "t1", "m1", 0, false, "first\n")
	lr := readLive(t, "s")
	if lr.Text != "first\nsecond\n" || lr.Final || lr.Partial {
		t.Fatalf("got %+v, want the two flushes in index order, not final, not partial", lr)
	}
}

// A missing index is a flush whose hook is still running. What comes after it waits, or its
// line would show above the one it follows.
func TestLiveTextStopsAtAGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 0, false, "a\n")
	AppendLiveText("s", "t1", "m1", 2, false, "c\n")
	if lr := readLive(t, "s"); lr.Text != "a\n" {
		t.Fatalf("with flush 1 missing got %q, want only flush 0", lr.Text)
	}
	AppendLiveText("s", "t1", "m1", 1, false, "b\n")
	if lr := readLive(t, "s"); lr.Text != "a\nb\nc\n" {
		t.Fatalf("once flush 1 landed got %q, want all three", lr.Text)
	}
}

// The same holds for the message's first flush: until it lands, the second one cannot be shown
// as the start of the reply. (Only a message too long for the read window starts mid-way.)
func TestLiveTextWaitsForTheFirstFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 1, false, "second\n")
	if lr, ok := ReadLiveText("s"); ok && lr.Text != "" {
		t.Fatalf("with flush 0 missing got %q, want nothing yet", lr.Text)
	}
	AppendLiveText("s", "t1", "m1", 0, false, "first\n")
	if lr := readLive(t, "s"); lr.Text != "first\nsecond\n" || lr.Partial {
		t.Fatalf("got %+v, want both flushes and not partial", lr)
	}
}

// A message that ends on a newline gets an empty final flush. It carries no text but is what
// says the message is complete, so it must be recorded.
func TestLiveTextFinalFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 0, false, "line\n")
	before := time.Now()
	AppendLiveText("s", "t1", "m1", 1, true, "")
	lr := readLive(t, "s")
	if !lr.Final || lr.Text != "line\n" {
		t.Fatalf("got %+v, want a final message with the one line", lr)
	}
	if lr.FinalAt.Before(before) {
		t.Fatalf("FinalAt %v predates the final flush (%v)", lr.FinalAt, before)
	}
}

func TestLiveTextSkipsEmptyNonFinalFlush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 0, false, "")
	AppendLiveText("s", "t1", "", 0, false, "no message id")
	if _, ok := ReadLiveText("s"); ok {
		t.Fatal("a flush with no text (or no message id) was recorded")
	}
}

// The final flush of one message can land after the next message has begun: its hook process
// ran late. The newest message is the one that started last.
func TestLiveTextNewestMessageIsTheLastToStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 0, false, "one\n")
	AppendLiveText("s", "t1", "m2", 0, false, "two\n")
	AppendLiveText("s", "t1", "m1", 1, true, "tail")
	lr := readLive(t, "s")
	if lr.Text != "two\n" || lr.Final {
		t.Fatalf("got %+v, want m2 (still streaming), not m1's straggling tail", lr)
	}
	// The next turn's first message wins over what is left of the previous turn.
	AppendLiveText("s", "t2", "m1", 0, false, "next turn\n")
	if lr := readLive(t, "s"); lr.Text != "next turn\n" {
		t.Fatalf("got %q, want the new turn's message", lr.Text)
	}
}

// A message longer than the read window comes back as its tail, flagged Partial.
func TestLiveTextWindowKeepsTheTail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	line := strings.Repeat("x", 1000) + "\n"
	n := liveTextWindow/len(line) + 50
	for i := range n {
		AppendLiveText("s", "t1", "m1", i, false, fmt.Sprintf("%04d", i)+line)
	}
	lr := readLive(t, "s")
	if !lr.Partial {
		t.Fatal("a message bigger than the window was not marked Partial")
	}
	if want := fmt.Sprintf("%04d", n-1) + line; !strings.HasSuffix(lr.Text, want) {
		t.Fatalf("the tail is missing its last flush (%q…)", want[:8])
	}
	if strings.HasPrefix(lr.Text, "0000") || len(lr.Text) >= n*(len(line)+4) {
		t.Fatalf("got %d bytes from the head; want only what fits the window", len(lr.Text))
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
	AppendLiveText("s", "t1", "m1", 0, false, "x\n")
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
		wg.Go(func() { AppendLiveText("s", "t1", "m1", i, false, fmt.Sprintf("%03d\n", i)) })
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

func TestRemoveLiveText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendLiveText("s", "t1", "m1", 0, false, "x\n")
	RemoveLiveText("s")
	if _, ok := ReadLiveText("s"); ok {
		t.Fatal("the streamed text survived RemoveLiveText")
	}
}

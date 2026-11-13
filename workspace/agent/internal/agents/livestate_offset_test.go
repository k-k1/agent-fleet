package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isEnd(line []byte) bool   { return strings.Contains(string(line), `"end"`) }
func isStart(line []byte) bool { return strings.Contains(string(line), `"u"`) }

func TestEndedAfterOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n")
	mark, ok := FileSizeMark(p)
	if !ok {
		t.Fatal("no mark")
	}
	// Nothing written since the mark: the existing end is the previous turn's.
	if after, known := EndedAfterOffset(p, mark, 1<<20, isStart, isEnd); after || !known {
		t.Fatalf("stale end: after=%v known=%v", after, known)
	}
	// The previous turn's late end lands after the mark, before the new input shows up.
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"end\"}\n")
	if after, known := EndedAfterOffset(p, mark, 1<<20, isStart, isEnd); after || !known {
		t.Fatalf("a late previous end counted: after=%v known=%v", after, known)
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"u\"}\n")
	if after, known := EndedAfterOffset(p, mark, 1<<20, isStart, isEnd); after || !known {
		t.Fatalf("new turn running: after=%v known=%v", after, known)
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"u\"}\n{\"t\":\"end\"}\n")
	if after, known := EndedAfterOffset(p, mark, 1<<20, isStart, isEnd); !after || !known {
		t.Fatalf("new end: after=%v known=%v", after, known)
	}
	// More than a window written since the mark. The previous run's huge tail and end, with no
	// new input yet, is not this turn's; a real start followed by a huge turn and its end is.
	huge := strings.Repeat("{\"t\":\"x\"}\n", 40)
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n" + huge + "{\"t\":\"end\"}\n")
	if after, known := EndedAfterOffset(p, "14", 64, isStart, isEnd); after || !known {
		t.Fatalf("a previous run's big tail invented a new input: after=%v known=%v", after, known)
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"u\"}\n" + huge + "{\"t\":\"end\"}\n")
	if after, known := EndedAfterOffset(p, "14", 64, isStart, isEnd); !after || !known {
		t.Fatalf("a real start before a turn longer than the window: after=%v known=%v", after, known)
	}
	// Truncated below the mark: offsets mean nothing any more.
	write("{\"t\":\"end\"}\n")
	if _, known := EndedAfterOffset(p, mark, 1<<20, isStart, isEnd); known {
		t.Fatal("a source smaller than the mark was compared")
	}
	if _, known := EndedAfterOffset(p, "junk", 1<<20, isStart, isEnd); known {
		t.Fatal("an unparseable mark was compared")
	}
	if _, known := EndedAfterOffset(filepath.Join(t.TempDir(), "none"), mark, 1<<20, isStart, isEnd); known {
		t.Fatal("a missing file was compared")
	}
}

// With a window smaller than the file the cut first line is dropped, and offsets stay absolute.
func TestScanLinesTailWindow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := "{\"t\":\"end\"}\n" + strings.Repeat("{\"t\":\"x\"}\n", 50) + "{\"t\":\"end\"}\n{\"t\":\"x\"}\n"
	os.WriteFile(p, []byte(body), 0o600)
	last := int64(-1)
	size, _, err := scanLines(p, 64, func(off int64, line []byte) {
		if isEnd(line) {
			last = off
		}
	})
	if err != nil || size != int64(len(body)) {
		t.Fatalf("%d %v", size, err)
	}
	if want := int64(strings.LastIndex(body, "{\"t\":\"end\"}")); last != want {
		t.Fatalf("start %d, want %d", last, want)
	}
}

func TestFileSizeMarkMissingFileIsZero(t *testing.T) {
	if m, ok := FileSizeMark(filepath.Join(t.TempDir(), "none")); !ok || m != "0" {
		t.Fatalf("%q %v", m, ok)
	}
}

func TestMarkFingerprintDetectsAReplacedFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.jsonl")
	old := `{"role":"user","old":true}` + "\n" + `{"type":"end"}` + "\n"
	os.WriteFile(p, []byte(old), 0o600)
	mark, _ := FileSizeMark(p)
	isUser := func(l []byte) bool { return bytes.Contains(l, []byte(`"role":"user"`)) }
	// Replaced by a different file that regrew beyond the old size.
	os.WriteFile(p, []byte(`{"role":"user","new":true}`+"\n"+strings.Repeat(`{"type":"tool"}`+"\n", 20)), 0o600)
	if _, known := InputSeenOffset(p, mark, isUser); known {
		t.Fatal("a replaced file was compared by offset")
	}
	if _, known := EndedAfterOffset(p, mark, 1<<20, isUser, isUser); known {
		t.Fatal("a replaced file was compared by offset (end)")
	}
	// The same file, grown, still compares.
	os.WriteFile(p, []byte(old+`{"role":"user"}`+"\n"), 0o600)
	if seen, known := InputSeenOffset(p, mark, isUser); !seen || !known {
		t.Fatalf("grown file: %v %v", seen, known)
	}
}

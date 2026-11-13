package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isEnd(line []byte) bool { return strings.Contains(string(line), `"end"`) }

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
	if after, known := EndedAfterOffset(p, mark, 1<<20, isEnd); after || !known {
		t.Fatalf("stale end: after=%v known=%v", after, known)
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"u\"}\n")
	if after, known := EndedAfterOffset(p, mark, 1<<20, isEnd); after || !known {
		t.Fatalf("new turn running: after=%v known=%v", after, known)
	}
	write("{\"t\":\"u\"}\n{\"t\":\"end\"}\n{\"t\":\"u\"}\n{\"t\":\"end\"}\n")
	if after, known := EndedAfterOffset(p, mark, 1<<20, isEnd); !after || !known {
		t.Fatalf("new end: after=%v known=%v", after, known)
	}
	// Truncated below the mark: offsets mean nothing any more.
	write("{\"t\":\"end\"}\n")
	if _, known := EndedAfterOffset(p, mark, 1<<20, isEnd); known {
		t.Fatal("a source smaller than the mark was compared")
	}
	if _, known := EndedAfterOffset(p, "junk", 1<<20, isEnd); known {
		t.Fatal("an unparseable mark was compared")
	}
	if _, known := EndedAfterOffset(filepath.Join(t.TempDir(), "none"), mark, 1<<20, isEnd); known {
		t.Fatal("a missing file was compared")
	}
}

// With a window smaller than the file the cut first line is dropped, and offsets stay absolute.
func TestLastLineStartTailWindow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := "{\"t\":\"end\"}\n" + strings.Repeat("{\"t\":\"x\"}\n", 50) + "{\"t\":\"end\"}\n{\"t\":\"x\"}\n"
	os.WriteFile(p, []byte(body), 0o600)
	start, size, found, err := LastLineStart(p, 64, isEnd)
	if err != nil || !found || size != int64(len(body)) {
		t.Fatalf("%d %d %v %v", start, size, found, err)
	}
	if want := int64(strings.LastIndex(body, "{\"t\":\"end\"}")); start != want {
		t.Fatalf("start %d, want %d", start, want)
	}
}

func TestFileSizeMarkMissingFileIsZero(t *testing.T) {
	if m, ok := FileSizeMark(filepath.Join(t.TempDir(), "none")); !ok || m != "0" {
		t.Fatalf("%q %v", m, ok)
	}
}

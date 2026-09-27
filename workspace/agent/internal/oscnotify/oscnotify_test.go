package oscnotify

import (
	"reflect"
	"strings"
	"testing"
)

func scanAll(chunks ...string) []Notification {
	var s Scanner
	var got []Notification
	for _, c := range chunks {
		s.Feed([]byte(c), func(n Notification) { got = append(got, n) })
	}
	return got
}

// tmuxWrap is what a CLI that detects $TMUX writes: the sequence inside a DCS
// passthrough with every ESC doubled.
func tmuxWrap(seq string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

func TestFeed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Notification
	}{
		{"osc9 BEL", "a\x1b]9;Build done\x07b", []Notification{{Proto: "osc9", Body: "Build done"}}},
		{"osc9 ST", "\x1b]9;Build done\x1b\\", []Notification{{Proto: "osc9", Body: "Build done"}}},
		{"osc9 body starting with a number", "\x1b]9;3 tests failed\x07", []Notification{{Proto: "osc9", Body: "3 tests failed"}}},
		{"osc9;4 progress is not a notification", "\x1b]9;4;1;50\x07\x1b]9;4;0\x07", nil},
		{"osc9;9 cwd is not a notification", "\x1b]9;9;/home/dev\x07", nil},
		{"osc9 bare digits", "\x1b]9;12\x07", nil},
		{"osc9 empty", "\x1b]9;\x07", nil},
		{"osc777 title and body", "\x1b]777;notify;Claude Code;Needs your permission\x07",
			[]Notification{{Proto: "osc777", Title: "Claude Code", Body: "Needs your permission"}}},
		{"osc777 body keeps later semicolons", "\x1b]777;notify;T;a;b\x07", []Notification{{Proto: "osc777", Title: "T", Body: "a;b"}}},
		{"osc777 title only", "\x1b]777;notify;Done\x07", []Notification{{Proto: "osc777", Title: "Done"}}},
		{"osc777 other subcommand", "\x1b]777;preexec\x07", nil},
		{"osc99 single", "\x1b]99;;Hello\x1b\\", []Notification{{Proto: "osc99", Title: "Hello"}}},
		{"osc99 title then body", "\x1b]99;i=1:d=0;Title\x1b\\\x1b]99;i=1:p=body;The body\x1b\\",
			[]Notification{{Proto: "osc99", Title: "Title", Body: "The body"}}},
		{"osc99 base64", "\x1b]99;e=1;SGVsbG8=\x1b\\", []Notification{{Proto: "osc99", Title: "Hello"}}},
		{"osc99 finished by an icon chunk", "\x1b]99;i=1:d=0;Title\x1b\\\x1b]99;i=1:p=body:d=0;Body\x1b\\\x1b]99;i=1:p=icon;aWNvbg==\x1b\\",
			[]Notification{{Proto: "osc99", Title: "Title", Body: "Body"}}},
		{"osc99 close is not a notification", "\x1b]99;i=1:p=close;\x1b\\", nil},
		{"osc99 capability query", "\x1b]99;i=opentui-notifications:p=?;\x1b\\", nil},
		{"osc99 unfinished chunk", "\x1b]99;i=1:d=0;Title\x1b\\", nil},
		{"title and hyperlink OSCs are ignored", "\x1b]0;window\x07\x1b]8;;https://x\x1b\\x\x1b]8;;\x1b\\", nil},
		{"tmux passthrough BEL", tmuxWrap("\x1b]9;Wrapped\x07"), []Notification{{Proto: "osc9", Body: "Wrapped"}}},
		{"tmux passthrough ST", tmuxWrap("\x1b]777;notify;T;B\x1b\\"), []Notification{{Proto: "osc777", Title: "T", Body: "B"}}},
		{"other DCS body is not scanned", "\x1bPq\x1b]9;hidden\x07\x1b\\\x1b]9;shown\x07", []Notification{{Proto: "osc9", Body: "shown"}}},
		{"APC body is not scanned", "\x1b_G\x1b]9;hidden\x07\x1b\\", nil},
		{"CAN aborts", "\x1b]9;gone\x18\x1b]9;kept\x07", []Notification{{Proto: "osc9", Body: "kept"}}},
		{"unterminated OSC ended by a new ESC", "\x1b]9;lost\x1b]9;kept\x07", []Notification{{Proto: "osc9", Body: "kept"}}},
		{"control characters are flattened", "\x1b]9;line1\nline2\tx\x07", []Notification{{Proto: "osc9", Body: "line1 line2 x"}}},
		// 0x9c / 0x9d are continuation bytes here (U+305C ぜ, U+305D そ): they must not be read
		// as C1 ST / OSC.
		{"japanese body", "\x1b]9;ぜそ完了\x07", []Notification{{Proto: "osc9", Body: "ぜそ完了"}}},
		{"plain text", "hello world", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scanAll(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

// A pipe read can split a sequence at any byte; the result must not depend on where.
func TestFeedSplitAnywhere(t *testing.T) {
	in := "x\x1b]9;ぜそ one\x07" + tmuxWrap("\x1b]777;notify;T;B\x1b\\") +
		"\x1b]99;i=a:d=0;Ti\x1b\\\x1b]99;i=a:p=body;Bo\x1b\\y"
	want := scanAll(in)
	if len(want) != 3 {
		t.Fatalf("whole input: got %#v", want)
	}
	for i := 0; i <= len(in); i++ {
		for j := i; j <= len(in); j++ {
			if got := scanAll(in[:i], in[i:j], in[j:]); !reflect.DeepEqual(got, want) {
				t.Fatalf("split at %d,%d: got %#v, want %#v", i, j, got, want)
			}
		}
	}
}

func TestOversizedOSCIsDiscarded(t *testing.T) {
	big := "\x1b]9;" + strings.Repeat("a", maxBody+10) + "\x07"
	if got := scanAll(big + "\x1b]9;after\x07"); !reflect.DeepEqual(got, []Notification{{Proto: "osc9", Body: "after"}}) {
		t.Fatalf("got %#v", got)
	}
}

func TestLongBodyIsTruncated(t *testing.T) {
	got := scanAll("\x1b]9;" + strings.Repeat("あ", maxBodyRunes+5) + "\x07")
	if len(got) != 1 || !strings.HasSuffix(got[0].Body, "…") || len([]rune(got[0].Body)) != maxBodyRunes+1 {
		t.Fatalf("got %d notifications, body runes %d", len(got), len([]rune(got[0].Body)))
	}
}

func TestNestedPassthroughIsBounded(t *testing.T) {
	seq := "\x1b]9;deep\x07"
	for i := 0; i < maxPassthroughDepth+2; i++ {
		seq = tmuxWrap(seq)
	}
	_ = scanAll(seq) // must terminate without recursion blow-up
	if got := scanAll(tmuxWrap(tmuxWrap("\x1b]9;two\x07"))); len(got) != 1 {
		t.Fatalf("two levels: got %#v", got)
	}
}

func FuzzFeed(f *testing.F) {
	f.Add([]byte("\x1b]9;x\x07"), 3)
	f.Add([]byte(tmuxWrap("\x1b]99;i=1:d=0;T\x1b\\")), 1)
	f.Fuzz(func(t *testing.T, in []byte, cut int) {
		if cut < 0 || cut > len(in) {
			cut = len(in)
		}
		var s Scanner
		s.Feed(in[:cut], func(Notification) {})
		s.Feed(in[cut:], func(n Notification) {
			if len(n.Body) > 4*(maxBodyRunes+1) || strings.ContainsRune(n.Body, 0x1b) {
				t.Fatalf("unbounded or unsanitized body %q", n.Body)
			}
		})
	})
}

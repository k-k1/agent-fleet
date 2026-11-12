package tmuxx

import (
	"bytes"
	"testing"
)

// tmuxAttachPrelude is what tmux 3.5a writes right after an attach (measured with
// workspace/tmux.conf): the three device queries, then the two colour queries.
const tmuxAttachPrelude = "\x1b[?1049h\x1b[c\x1b[>c\x1b[>q\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b[H\x1b[2J"

const tmuxAttachPreludeFiltered = "\x1b[?1049h\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b[H\x1b[2J"

func filterAll(f *AttachQueryFilter, chunks ...string) string {
	var out bytes.Buffer
	for _, c := range chunks {
		out.Write(f.Filter([]byte(c)))
	}
	out.Write(f.Flush())
	return out.String()
}

func TestAttachQueryFilter(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"attach prelude", []string{tmuxAttachPrelude}, tmuxAttachPreludeFiltered},
		{"no queries", []string{"hello \x1b[1;31mred\x1b[0m\r\n"}, "hello \x1b[1;31mred\x1b[0m\r\n"},
		{"each query once only", []string{"\x1b[c\x1b[c"}, "\x1b[c"},
		{"cursor forward is not DA1", []string{"\x1b[5C\x1b[C"}, "\x1b[5C\x1b[C"},
		{"DA3 and DECRQM untouched", []string{"\x1b[=c\x1b[?2026$p"}, "\x1b[=c\x1b[?2026$p"},
		{"colour queries untouched", []string{"\x1b]11;?\x07"}, "\x1b]11;?\x07"},
		{"lone ESC at the end is flushed", []string{"a\x1b"}, "a\x1b"},
		{"held prefix that turns out not to be a query", []string{"x\x1b[", "1mY"}, "x\x1b[1mY"},
		{"after all three, pass-through", []string{"\x1b[c\x1b[>c\x1b[>q", "\x1b[>c"}, "\x1b[>c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterAll(NewAttachQueryFilter(), tc.in...); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A PTY read can end anywhere, so every split of the prelude must give the same result.
func TestAttachQueryFilterSplitAnywhere(t *testing.T) {
	for i := 0; i <= len(tmuxAttachPrelude); i++ {
		for j := i; j <= len(tmuxAttachPrelude); j++ {
			got := filterAll(NewAttachQueryFilter(), tmuxAttachPrelude[:i], tmuxAttachPrelude[i:j], tmuxAttachPrelude[j:])
			if got != tmuxAttachPreludeFiltered {
				t.Fatalf("split at %d,%d: got %q", i, j, got)
			}
		}
	}
}

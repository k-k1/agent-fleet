package termquery

import "testing"

func TestStrip(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"DA2 query", "a\x1b[>cb", "ab"},
		{"DA2 query with 0", "a\x1b[>0cb", "ab"},
		{"DA1 query", "\x1b[c\x1b[0cx", "x"},
		{"DA3 query", "\x1b[=cx", "x"},
		{"DA1 reply kept", "\x1b[?1;2c", "\x1b[?1;2c"},
		{"DA2 reply kept", "\x1b[>0;276;0c", "\x1b[>0;276;0c"},
		{"DSR and CPR", "\x1b[5n\x1b[6n\x1b[?6nx", "x"},
		{"DSR reply kept", "\x1b[0n", "\x1b[0n"},
		{"modifier keys reset kept", "\x1b[>4n", "\x1b[>4n"},
		{"DECRQM", "\x1b[?2004$p\x1b[4$px", "x"},
		{"XTVERSION", "\x1b[>q\x1b[>0qx", "x"},
		{"cursor style kept", "\x1b[2 q", "\x1b[2 q"},
		{"kitty flags query", "\x1b[?ux", "x"},
		{"kitty push and pop kept", "\x1b[>5u\x1b[<u", "\x1b[>5u\x1b[<u"},
		{"window size reports", "\x1b[14t\x1b[16t\x1b[18tx", "x"},
		{"title push kept", "\x1b[22;0t", "\x1b[22;0t"},
		{"OSC color query BEL", "\x1b]11;?\ax", "x"},
		{"OSC color query ST", "\x1b]4;1;?\x1b\\x", "x"},
		{"OSC clipboard read", "\x1b]52;c;?\ax", "x"},
		{"OSC color set kept", "\x1b]11;#000000\a", "\x1b]11;#000000\a"},
		{"OSC title kept", "\x1b]0;?\a", "\x1b]0;?\a"},
		{"DECRQSS", "\x1bP$qm\x1b\\x", "x"},
		{"XTGETTCAP", "\x1bP+q544e\x1b\\x", "x"},
		{"other DCS kept", "\x1bPtmux;\x1b\x1b]9;hi\a\x1b\\", "\x1bPtmux;\x1b\x1b]9;hi\a\x1b\\"},
		{"SGR kept", "\x1b[1;31mred\x1b[0m", "\x1b[1;31mred\x1b[0m"},
		{"cut-off query kept", "x\x1b[>", "x\x1b[>"},
		{"cut-off OSC kept", "x\x1b]11;?", "x\x1b]11;?"},
		{"lone ESC kept", "\x1b", "\x1b"},
		{"malformed CSI then query", "\x1b[\x1b[>cx", "\x1b[x"},
		{"repeated queries", "$ \x1b[>c\x1b[>c\x1b[>c$ ", "$ $ "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Strip([]byte(c.in))); got != c.want {
				t.Fatalf("Strip(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func TestStripReturnsInputWhenClean(t *testing.T) {
	in := []byte("\x1b[32mhello\x1b[0m\r\n")
	if got := Strip(in); &got[0] != &in[0] {
		t.Fatal("a history without queries was copied")
	}
}

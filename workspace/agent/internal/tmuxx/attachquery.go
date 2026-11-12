package tmuxx

import "bytes"

// AttachQueries are the device queries a tmux client writes to its terminal right after
// attaching (tmux 3.5a tty_send_requests: DA1, DA2, XTVERSION, each as this exact literal).
// tmux waits only TTY_QUERY_TIMEOUT (5 s) for the answers and then hands any late one to
// the pane as typed input, so a shell or an SSM remote prints "1;2c0;276;0c" (#3981). The
// Console answers late whenever a long history replay sits in front of the live bytes.
// xterm.js's answers carry nothing tmux uses (no DA1 level 61-65 or sixel, no DA2 type
// M/T/U, no known XTVERSION name), so the queries are removed rather than answered faster.
// The OSC 10/11 colour queries are left alone: tmux accepts their replies at any time and
// uses them to answer pane programs' own colour queries (theme detection).
var AttachQueries = [][]byte{
	[]byte("\x1b[c"),
	[]byte("\x1b[>c"),
	[]byte("\x1b[>q"),
}

// AttachQueryFilter removes each attach query once from a tmux client's output stream.
// tmux sends them once per client (later requests are colour-only), so the filter turns
// into a plain pass-through after it has removed all of them. A read that ends inside a
// possible query holds those few bytes back until the next read decides them; tmux never
// ends a write there, so the rest follows at once. Not for a plain shell PTY: there a
// program's DA query is meant for xterm.js and must be answered.
type AttachQueryFilter struct {
	held []byte
	seen [3]bool
	left int
}

// NewAttachQueryFilter returns a filter for one attach client's output.
func NewAttachQueryFilter() *AttachQueryFilter {
	return &AttachQueryFilter{left: len(AttachQueries)}
}

// Filter returns the bytes of p (after any held from the previous call) that should be
// relayed. The result may alias p.
func (f *AttachQueryFilter) Filter(p []byte) []byte {
	if f.left == 0 && len(f.held) == 0 {
		return p
	}
	in := p
	if len(f.held) > 0 {
		in = append(f.held, p...)
		f.held = nil
	}
	if f.left == 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	for len(in) > 0 {
		i := bytes.IndexByte(in, 0x1b)
		if i < 0 {
			out = append(out, in...)
			break
		}
		out = append(out, in[:i]...)
		in = in[i:]
		n, partial := f.match(in)
		switch {
		case n > 0:
			in = in[n:]
		case partial:
			f.held = append([]byte(nil), in...)
			return out
		default:
			out = append(out, in[0])
			in = in[1:]
		}
	}
	return out
}

// Flush returns the bytes held back by the last Filter call, for the end of the stream.
func (f *AttachQueryFilter) Flush() []byte {
	b := f.held
	f.held = nil
	return b
}

// match reports whether b (which starts with ESC) begins with an attach query not yet
// removed (n = its length, and marks it seen), or is a strict prefix of one (partial).
func (f *AttachQueryFilter) match(b []byte) (n int, partial bool) {
	for i, q := range AttachQueries {
		if f.seen[i] {
			continue
		}
		if bytes.HasPrefix(b, q) {
			f.seen[i] = true
			f.left--
			return len(q), false
		}
		if len(b) < len(q) && bytes.HasPrefix(q, b) {
			partial = true
		}
	}
	return 0, partial
}

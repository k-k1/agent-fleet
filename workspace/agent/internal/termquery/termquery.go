// Package termquery finds the terminal queries in recorded terminal output.
package termquery

import "bytes"

// Strip removes the terminal queries (device attributes, status and
// mode reports, color and setting queries) from recorded pane output before it is
// replayed to a browser terminal.
//
// The history is the pane program's raw output, so it still holds every query that
// program once sent. Replayed, xterm.js answers each one, and the answer reaches the
// live PTY as input: tmux swallows only the reply it asked for itself and hands any
// other to the pane, where a shell prints "0;276;0c" for a DA2 reply (#3337). A query
// in the history has no asker any more, so dropping it loses nothing on screen.
//
// Only complete, well-formed sequences are matched; anything else is kept verbatim.
// The input is returned unchanged (not copied) when it contains no query.
func Strip(b []byte) []byte {
	var out []byte
	stripped := false
	last := 0
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], 0x1b)
		if j < 0 || i+j+1 >= len(b) {
			break
		}
		i += j
		var end int
		var query bool
		switch b[i+1] {
		case '[':
			end, query = scanCSIQuery(b, i+2)
		case ']':
			end, query = scanStringQuery(b, i+2, isOSCQuery)
		case 'P':
			end, query = scanStringQuery(b, i+2, isDCSQuery)
		}
		if end <= 0 {
			i++
			continue
		}
		if query {
			out = append(out, b[last:i]...)
			last, stripped = end, true
		}
		i = end
	}
	if !stripped {
		return b
	}
	return append(out, b[last:]...)
}

// scanCSIQuery parses the CSI sequence whose parameters start at p and returns the
// index just past it (0 when malformed or cut off) and whether it is a query.
func scanCSIQuery(b []byte, p int) (int, bool) {
	k := p
	for k < len(b) && b[k] >= 0x30 && b[k] <= 0x3f {
		k++
	}
	params := b[p:k]
	m := k
	for m < len(b) && b[m] >= 0x20 && b[m] <= 0x2f {
		m++
	}
	if m >= len(b) || b[m] < 0x40 || b[m] > 0x7e {
		return 0, false
	}
	return m + 1, isCSIQuery(params, string(b[k:m]), b[m])
}

func isCSIQuery(params []byte, inter string, final byte) bool {
	var prefix byte
	if len(params) > 0 && params[0] >= '<' && params[0] <= '?' {
		prefix, params = params[0], params[1:]
	}
	first := firstParam(params)
	switch {
	case inter == "$" && final == 'p': // DECRQM, ANSI and DEC modes
		return prefix == 0 || prefix == '?'
	case inter != "":
		return false
	case final == 'c': // DA1, DA2, DA3 take at most a 0; the replies carry several parameters
		return (prefix == 0 || prefix == '>' || prefix == '=') && (len(params) == 0 || string(params) == "0")
	case final == 'n': // DSR / CPR; "CSI > … n" sets modifier keys instead
		return prefix == '?' || (prefix == 0 && (first == "5" || first == "6"))
	case final == 'q': // XTVERSION
		return prefix == '>'
	case final == 'u': // kitty keyboard flags query; the other prefixes push/pop/set
		return prefix == '?' && len(params) == 0
	case final == 't': // XTWINOPS reports; the other operations change the window
		if prefix != 0 {
			return false
		}
		switch first {
		case "11", "13", "14", "15", "16", "18", "19", "20", "21":
			return true
		}
	}
	return false
}

func firstParam(params []byte) string {
	if i := bytes.IndexByte(params, ';'); i >= 0 {
		return string(params[:i])
	}
	return string(params)
}

// scanStringQuery finds the end of an OSC or DCS string starting at p (terminated by
// BEL or ESC \) and classifies its body. An unterminated string returns 0.
func scanStringQuery(b []byte, p int, isQuery func([]byte) bool) (int, bool) {
	for k := p; k < len(b); k++ {
		switch b[k] {
		case 0x07:
			return k + 1, isQuery(b[p:k])
		case 0x1b:
			if k+1 < len(b) && b[k+1] == '\\' {
				return k + 2, isQuery(b[p:k])
			}
			return 0, false // a new sequence began: the string was cut off
		}
	}
	return 0, false
}

// isOSCQuery matches the color queries (OSC 4, 5, 10-19 with a "?" field) and the
// OSC 52 clipboard read request.
func isOSCQuery(body []byte) bool {
	fields := bytes.Split(body, []byte{';'})
	switch string(fields[0]) {
	case "4", "5", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "52":
	default:
		return false
	}
	for _, f := range fields[1:] {
		if string(f) == "?" {
			return true
		}
	}
	return false
}

// isDCSQuery matches DECRQSS ("$q") and XTGETTCAP ("+q").
func isDCSQuery(body []byte) bool {
	return bytes.HasPrefix(body, []byte("$q")) || bytes.HasPrefix(body, []byte("+q"))
}

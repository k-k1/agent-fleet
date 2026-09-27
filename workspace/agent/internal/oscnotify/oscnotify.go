// Package oscnotify picks desktop-notification escape sequences out of a raw PTY
// byte stream: OSC 9 (iTerm2), OSC 99 (kitty) and OSC 777;notify (rxvt/Ghostty).
//
// The stream it reads is tmux's pipe-pane copy of the pane program's output, so it
// sees the CLI's bytes before tmux parses them. A CLI that knows it runs under tmux
// wraps its sequence in tmux passthrough (`ESC P tmux; … ESC \`, every inner ESC
// doubled); the wrapper is unwrapped and its content scanned like top-level output.
//
// Only 7-bit introducers and terminators (ESC ], ESC P, ESC \, BEL) are recognised.
// The panes carry UTF-8, where 0x90/0x9c/0x9d are ordinary continuation bytes of
// Japanese text; treating them as C1 controls would cut notification bodies apart and
// start phantom sequences in the middle of a line.
//
// The framing follows cmux's terminal_metadata.rs / RemoteTmuxNotificationOSCFilter
// (bounded body, CAN/SUB cancel, string controls other than OSC ignored so an embedded
// OSC cannot leak). Which kinds emit what is recorded in docs/build/04-agent.md.
package oscnotify

import (
	"bytes"
	"encoding/base64"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Notification is one desktop notification a program asked its terminal to show.
type Notification struct {
	// Proto names the sequence it came from: "osc9", "osc99" or "osc777".
	Proto string
	Title string
	Body  string
}

const (
	// maxBody caps one OSC payload. A longer sequence is discarded to its terminator
	// rather than buffered, so a corrupt or hostile stream cannot pin memory.
	maxBody = 4096
	// maxTitleRunes / maxBodyRunes bound what reaches the notification outbox.
	maxTitleRunes = 200
	maxBodyRunes  = 1000
	// maxKittyPending bounds the kitty chunks awaiting their d=1 terminator.
	maxKittyPending = 4
	// maxPassthroughDepth stops a nested tmux passthrough from recursing without end.
	maxPassthroughDepth = 2
)

type state uint8

const (
	stGround state = iota
	stEsc
	stOSC
	stOSCEsc
	stDiscard    // an OSC over maxBody, waiting for its terminator
	stDiscardEsc //
	stDCSHead    // inside ESC P, deciding whether it is tmux passthrough
	stDCSHeadEsc //
	stPass       // inside tmux passthrough: unwrapped bytes go to the inner scanner
	stPassEsc    //
	stIgnore     // DCS/SOS/PM/APC body: ignored up to ST
	stIgnoreEsc  //
)

var tmuxPassthroughPrefix = []byte("tmux;")

// Scanner is the stateful parser. A sequence may be split across Feed calls at any
// byte. The zero value is ready to use; a Scanner is not safe for concurrent use.
type Scanner struct {
	st    state
	body  []byte
	depth int
	inner *Scanner
	kitty []kittyPart
}

type kittyPart struct {
	id          string
	title, body []byte
}

// Feed scans p and calls emit once per complete notification, in stream order.
func (s *Scanner) Feed(p []byte, emit func(Notification)) {
	// Plain output with no sequence in flight is the common case.
	if s.st == stGround && bytes.IndexByte(p, 0x1b) < 0 {
		return
	}
	for _, b := range p {
		s.step(b, emit)
	}
}

func (s *Scanner) step(b byte, emit func(Notification)) {
	// CAN and SUB abort any control string (ECMA-48).
	if (b == 0x18 || b == 0x1a) && s.st != stGround && s.st != stPass {
		s.reset()
		return
	}
	switch s.st {
	case stGround:
		if b == 0x1b {
			s.st = stEsc
		}
	case stEsc:
		switch b {
		case ']':
			s.body = s.body[:0]
			s.st = stOSC
		case 'P':
			s.body = s.body[:0]
			s.st = stDCSHead
		case 'X', '^', '_':
			s.st = stIgnore
		case 0x1b:
			// ESC ESC: the second one may still open a sequence.
		default:
			s.st = stGround
		}
	case stOSC:
		switch b {
		case 0x07:
			s.finish(emit)
		case 0x1b:
			s.st = stOSCEsc
		default:
			if len(s.body) >= maxBody {
				s.body = s.body[:0]
				s.st = stDiscard
				return
			}
			s.body = append(s.body, b)
		}
	case stOSCEsc:
		if b == '\\' {
			s.finish(emit)
			return
		}
		// An ESC that is not ST ends the OSC unterminated and begins a new sequence.
		s.body = s.body[:0]
		s.st = stEsc
		s.step(b, emit)
	case stDiscard:
		if b == 0x07 {
			s.st = stGround
		} else if b == 0x1b {
			s.st = stDiscardEsc
		}
	case stDiscardEsc:
		if b == '\\' {
			s.st = stGround
		} else if b != 0x1b {
			s.st = stDiscard
		}
	case stDCSHead:
		if b == 0x1b {
			s.st = stDCSHeadEsc
			return
		}
		s.body = append(s.body, b)
		if !bytes.HasPrefix(tmuxPassthroughPrefix, s.body) {
			s.body = s.body[:0]
			s.st = stIgnore
			return
		}
		if len(s.body) == len(tmuxPassthroughPrefix) {
			s.body = s.body[:0]
			if s.depth >= maxPassthroughDepth {
				s.st = stIgnore
				return
			}
			s.inner = &Scanner{depth: s.depth + 1}
			s.st = stPass
		}
	case stDCSHeadEsc:
		s.body = s.body[:0]
		if b == '\\' {
			s.st = stGround
		} else {
			s.st = stIgnore
		}
	case stPass:
		if b == 0x1b {
			s.st = stPassEsc
			return
		}
		s.inner.step(b, emit)
	case stPassEsc:
		switch b {
		case 0x1b:
			// A doubled ESC is one ESC of the wrapped sequence.
			s.inner.step(0x1b, emit)
			s.st = stPass
		case '\\':
			s.inner = nil
			s.st = stGround
		default:
			// A lone ESC cannot occur inside a well-formed wrapper: end it here and let
			// the ESC start whatever comes next at this level.
			s.inner = nil
			s.st = stEsc
			s.step(b, emit)
		}
	case stIgnore:
		if b == 0x1b {
			s.st = stIgnoreEsc
		}
	case stIgnoreEsc:
		if b == '\\' {
			s.st = stGround
		} else if b != 0x1b {
			s.st = stIgnore
		}
	}
}

func (s *Scanner) reset() {
	s.body = s.body[:0]
	s.inner = nil
	s.st = stGround
}

func (s *Scanner) finish(emit func(Notification)) {
	body := s.body
	s.st = stGround
	defer func() { s.body = s.body[:0] }()
	num, rest, ok := bytes.Cut(body, []byte(";"))
	if !ok {
		return
	}
	switch string(num) {
	case "9":
		// ConEmu reuses OSC 9 for control sub-commands — `9;4;…` progress (agy and
		// opencode emit it every turn), `9;9;<cwd>` from shell prompts. A notification
		// body that merely starts with a digit ("3 tests failed") has no ';' right after
		// the number, so it is not mistaken for one.
		if conEmuCommand(rest) {
			return
		}
		if n, ok := clean("osc9", "", string(rest)); ok {
			emit(n)
		}
	case "777":
		sub, args, _ := bytes.Cut(rest, []byte(";"))
		if string(sub) != "notify" {
			return
		}
		title, text, _ := bytes.Cut(args, []byte(";"))
		if n, ok := clean("osc777", string(title), string(text)); ok {
			emit(n)
		}
	case "99":
		s.kittyChunk(rest, emit)
	}
}

// conEmuCommand reports whether an OSC 9 payload is `<digits>` or `<digits>;…`.
func conEmuCommand(p []byte) bool {
	i := 0
	for i < len(p) && p[i] >= '0' && p[i] <= '9' {
		i++
	}
	return i > 0 && (i == len(p) || p[i] == ';')
}

// kittyChunk handles `99;<metadata>;<payload>` (kitty desktop-notification protocol).
// Title and body may arrive in several chunks sharing an id, the last one with d=1
// (the default). p=? is the capability query a program sends before deciding to
// notify, and the other p= values (close, icon, buttons, alive) carry no text.
func (s *Scanner) kittyChunk(rest []byte, emit func(Notification)) {
	meta, payload, _ := bytes.Cut(rest, []byte(";"))
	id, done, part, b64 := "", true, "title", false
	for _, kv := range bytes.Split(meta, []byte(":")) {
		k, v, _ := bytes.Cut(kv, []byte("="))
		switch string(k) {
		case "i":
			id = string(v)
		case "d":
			done = string(v) != "0"
		case "p":
			part = string(v)
		case "e":
			b64 = string(v) == "1"
		}
	}
	if part != "title" && part != "body" {
		return
	}
	text := payload
	if b64 {
		dec, err := base64.StdEncoding.DecodeString(string(payload))
		if err != nil {
			return
		}
		text = dec
	}
	kp := s.kittyFor(id)
	dst := &kp.title
	if part == "body" {
		dst = &kp.body
	}
	if len(*dst)+len(text) <= maxBody {
		*dst = append(*dst, text...)
	}
	if !done {
		return
	}
	title, bodyText := string(kp.title), string(kp.body)
	s.kittyDrop(id)
	if n, ok := clean("osc99", title, bodyText); ok {
		emit(n)
	}
}

func (s *Scanner) kittyFor(id string) *kittyPart {
	for i := range s.kitty {
		if s.kitty[i].id == id {
			return &s.kitty[i]
		}
	}
	if len(s.kitty) >= maxKittyPending {
		s.kitty = s.kitty[1:]
	}
	s.kitty = append(s.kitty, kittyPart{id: id})
	return &s.kitty[len(s.kitty)-1]
}

func (s *Scanner) kittyDrop(id string) {
	for i := range s.kitty {
		if s.kitty[i].id == id {
			s.kitty = append(s.kitty[:i:i], s.kitty[i+1:]...)
			return
		}
	}
}

// clean makes the text safe to store and display: valid UTF-8, no control
// characters (a stray ESC would otherwise ride into the Console), bounded length.
// A notification with neither title nor body is dropped.
func clean(proto, title, body string) (Notification, bool) {
	n := Notification{Proto: proto, Title: sanitize(title, maxTitleRunes), Body: sanitize(body, maxBodyRunes)}
	return n, n.Title != "" || n.Body != ""
}

func sanitize(s string, max int) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		if n >= max {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

package egressagent

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// LogFormat is the Squid logformat the parser reads (see the package doc). Changing it
// means a new version token, because a running agent must recognise old lines too.
const LogFormat = "af1 %tS %>a %<a %Ss %ssl::bump_mode %#ssl::>sni %#>rd"

const formatVersion = "af1"

// maxHost is the longest DNS name. A longer SNI or request domain is not a destination
// name; the event is kept, by address, with no host.
const maxHost = 253

// Event is one connection as POSTed to /internal/egress. The CP's egressEvent decodes it
// (a test in the control-plane package keeps the two in step).
type Event struct {
	ID      string `json:"id"`
	At      int64  `json:"at"`
	Source  string `json:"source"`
	Dest    string `json:"dest,omitempty"`
	Host    string `json:"host"`
	Allowed bool   `json:"allowed"`
	Count   int    `json:"count"`
}

// logLine is a parsed line before a verdict is attached.
type logLine struct {
	at     int64
	source string
	dest   string
	host   string
	// denied is Squid's own refusal: a terminated TLS connection or a DENIED status.
	denied bool
}

// parseLine reads one line in LogFormat. ok is false when the line is not ours or lacks
// the time or the source address, which are what attribution joins on and which the CP
// refuses an event without. A line whose later fields are damaged still yields its time
// and source with no host, and is reported as not allowed: the connection happened and
// must appear in the record even if its name cannot be read.
func parseLine(s string) (logLine, bool) {
	f := strings.Fields(s)
	if len(f) < 3 || f[0] != formatVersion {
		return logLine{}, false
	}
	at, ok := parseSquidTime(f[1])
	if !ok {
		return logLine{}, false
	}
	src, err := netip.ParseAddr(f[2])
	if err != nil {
		return logLine{}, false
	}
	l := logLine{at: at, source: src.Unmap().String(), denied: true}
	if len(f) > 3 {
		if a, err := netip.ParseAddr(f[3]); err == nil {
			l.dest = a.Unmap().String()
		}
	}
	if len(f) != 8 {
		return l, true
	}
	status, bump := f[4], f[5]
	l.denied = strings.Contains(status, "DENIED") || bump == "terminate"
	l.host = hostOf(f[6], f[7])
	return l, true
}

// parseSquidTime reads "%tS" (seconds, a dot, a fraction; Squid prints milliseconds) as
// unix milliseconds. Digits past the third are dropped.
func parseSquidTime(s string) (int64, bool) {
	sec, frac, ok := strings.Cut(s, ".")
	if !ok || frac == "" || len(frac) > 9 {
		return 0, false
	}
	a, err1 := strconv.ParseInt(sec, 10, 64)
	_, err2 := strconv.ParseUint(frac, 10, 64)
	if err1 != nil || err2 != nil || a <= 0 {
		return 0, false
	}
	ms, _ := strconv.ParseInt((frac + "00")[:3], 10, 64)
	return a*1000 + ms, true
}

// hostOf picks the name the connection claimed: the SNI, else the request domain when it
// is a name rather than an address (an intercepted TLS connection's request domain is the
// destination address). "" means the connection has no usable name.
func hostOf(sni, rd string) string {
	for _, v := range []string{sni, rd} {
		h := decodeField(v)
		if h == "" {
			continue
		}
		if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
			continue
		}
		if len(h) > maxHost {
			return ""
		}
		return h
	}
	return ""
}

// decodeField undoes Squid's URL encoding ("%#") and normalises a name the way the
// matcher compares it. "-" is Squid's "no value".
func decodeField(v string) string {
	if v == "-" || v == "" {
		return ""
	}
	if d, err := url.PathUnescape(v); err == nil {
		v = d
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
}

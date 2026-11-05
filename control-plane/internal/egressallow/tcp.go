package egressallow

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TCPPrefix marks an allowlist entry as a TCP passthrough: "tcp:<host>:<port>" or
// "tcp:<host>:<from>-<to>" (ADR 0122, addendum 2026-11-05). Such an entry names a host
// whose resolved addresses a workspace may reach on those ports through the gateway's TCP
// relay. It is not a dstdomain entry and never reaches Squid's allow file.
const TCPPrefix = "tcp:"

// MaxTCPRange is the widest port range one entry may cover. Managed SQL services redirect
// clients into a range about a thousand ports wide; a wider one is a port scan's worth of
// access to the named host.
const MaxTCPRange = 2048

// tcpRefusedPorts stay off the passthrough path: 80, 443 and 3128 are the recorded HTTP
// path, and 53 / 853 are DNS, which the gateway refuses to every resolver but the VPC's.
// A range that contains one is refused too, so an entry never reads as covering a port it
// does not.
var tcpRefusedPorts = []int{53, 80, 443, 853, 3128}

// TCPRefusedPort reports whether port can never be a passthrough destination.
func TCPRefusedPort(port int) bool {
	for _, p := range tcpRefusedPorts {
		if p == port {
			return true
		}
	}
	return false
}

// tcpHostRe is an exact DNS name: no wildcard and no ".suffix", because the gateway
// resolves the name to addresses and a suffix has none to resolve.
var tcpHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// TCPEntry is a parsed passthrough entry. Host is a lowercase DNS name or an IPv4 literal.
type TCPEntry struct {
	Host string `json:"host"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

// IsTCPEntry reports whether an allowlist entry is a passthrough entry.
func IsTCPEntry(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), TCPPrefix)
}

// String is the entry's canonical spelling, the form it is stored in.
func (e TCPEntry) String() string {
	if e.From == e.To {
		return fmt.Sprintf("%s%s:%d", TCPPrefix, e.Host, e.From)
	}
	return fmt.Sprintf("%s%s:%d-%d", TCPPrefix, e.Host, e.From, e.To)
}

// Covers reports whether port is inside the entry's range.
func (e TCPEntry) Covers(port int) bool { return port >= e.From && port <= e.To }

// ParseTCPEntry reads and validates a passthrough entry.
func ParseTCPEntry(s string) (TCPEntry, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	rest, ok := strings.CutPrefix(s, TCPPrefix)
	if !ok {
		return TCPEntry{}, fmt.Errorf("a tcp entry starts with %q", TCPPrefix)
	}
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return TCPEntry{}, fmt.Errorf("a tcp entry is tcp:<host>:<port> or tcp:<host>:<from>-<to>")
	}
	host, ports := strings.TrimSuffix(rest[:i], "."), rest[i+1:]
	e := TCPEntry{Host: host}
	if a, err := netip.ParseAddr(host); err == nil {
		// Only IPv4: the relay is IPv4-only, and an entry that could never match is the
		// silent failure the entry grammar exists to prevent.
		if !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() {
			return TCPEntry{}, fmt.Errorf("%q is not a usable IPv4 destination", host)
		}
	} else if !tcpHostRe.MatchString(host) || strings.Contains(host, "..") || len(host) > 253 {
		return TCPEntry{}, fmt.Errorf("%q is not an exact host name or IPv4 address", host)
	}
	lo, hi, isRange := strings.Cut(ports, "-")
	var err1, err2 error
	e.From, err1 = strconv.Atoi(lo)
	e.To = e.From
	if isRange {
		e.To, err2 = strconv.Atoi(hi)
	}
	if err1 != nil || err2 != nil || strconv.Itoa(e.From) != lo || (isRange && strconv.Itoa(e.To) != hi) {
		return TCPEntry{}, fmt.Errorf("%q is not a port or a port range", ports)
	}
	if e.From < 1 || e.To > 65535 || e.From > e.To {
		return TCPEntry{}, fmt.Errorf("port range %q is outside 1-65535 or reversed", ports)
	}
	if e.To-e.From+1 > MaxTCPRange {
		return TCPEntry{}, fmt.Errorf("a range covers at most %d ports", MaxTCPRange)
	}
	for _, p := range tcpRefusedPorts {
		if e.Covers(p) {
			return TCPEntry{}, fmt.Errorf("port %d stays on the recorded HTTP / DNS path and cannot be a passthrough destination", p)
		}
	}
	return e, nil
}

// SplitTCP separates passthrough entries from the HTTP allowlist. An entry that fails to
// parse is dropped from both: it is not a dstdomain entry either. The result is sorted and
// de-duplicated so the same policy always gives the same bytes.
func SplitTCP(entries []string) (httpEntries []string, tcp []TCPEntry) {
	seen := map[string]bool{}
	for _, e := range entries {
		if !IsTCPEntry(e) {
			httpEntries = append(httpEntries, e)
			continue
		}
		t, err := ParseTCPEntry(e)
		if err != nil || seen[t.String()] {
			continue
		}
		seen[t.String()] = true
		tcp = append(tcp, t)
	}
	sort.Slice(tcp, func(i, j int) bool { return tcp[i].String() < tcp[j].String() })
	return httpEntries, tcp
}

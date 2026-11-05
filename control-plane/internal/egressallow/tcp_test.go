package egressallow

import (
	"slices"
	"strings"
	"testing"
)

func TestParseTCPEntry(t *testing.T) {
	good := map[string]string{
		"tcp:git.example.com:22":         "tcp:git.example.com:22",
		" TCP:Git.Example.com.:22 ":      "tcp:git.example.com:22",
		"tcp:db.example.com:11000-11999": "tcp:db.example.com:11000-11999",
		"tcp:db.example.com:1433-1433":   "tcp:db.example.com:1433",
		"tcp:203.0.113.7:22":             "tcp:203.0.113.7:22",
		"tcp:db.example.com:20000-22047": "tcp:db.example.com:20000-22047", // exactly MaxTCPRange ports
		"tcp:db.example.com:65535":       "tcp:db.example.com:65535",
	}
	for in, want := range good {
		e, err := ParseTCPEntry(in)
		if err != nil || e.String() != want {
			t.Errorf("%q: got %q, %v; want %q", in, e.String(), err, want)
		}
	}
	bad := []string{
		"git.example.com:22", "tcp:git.example.com", "tcp:git.example.com:", "tcp::22",
		"tcp:.example.com:22", "tcp:*.example.com:22", "tcp:example:22", // suffix, wildcard, single label
		"tcp:git.example.com:0", "tcp:git.example.com:65536", "tcp:git.example.com:22-21",
		"tcp:git.example.com:022", "tcp:git.example.com:22-", "tcp:git.example.com:x",
		"tcp:db.example.com:20000-22048", // one past MaxTCPRange
		"tcp:db.example.com:80", "tcp:db.example.com:443", "tcp:db.example.com:3128",
		"tcp:db.example.com:53", "tcp:db.example.com:853", // refused ports
		"tcp:db.example.com:400-500",                                                       // a range that contains 443
		"tcp:127.0.0.1:22", "tcp:169.254.169.254:80", "tcp:0.0.0.0:22", "tcp:224.0.0.1:22", // never usable
		"tcp:2001:db8::5:22", "tcp:[2001:db8::5]:22", // IPv6: the relay is IPv4-only
		"tcp:a..example.com:22",
	}
	for _, in := range bad {
		if e, err := ParseTCPEntry(in); err == nil {
			t.Errorf("%q accepted as %q", in, e.String())
		}
	}
}

func TestSplitTCP(t *testing.T) {
	httpE, tcp := SplitTCP([]string{
		"github.com", "tcp:git.example.com:22", ".example.org", "TCP:git.example.com:22",
		"tcp:bad", "tcp:db.example.com:1433",
	})
	if !slices.Equal(httpE, []string{"github.com", ".example.org"}) {
		t.Errorf("http entries: %v", httpE)
	}
	var got []string
	for _, e := range tcp {
		got = append(got, e.String())
	}
	if !slices.Equal(got, []string{"tcp:db.example.com:1433", "tcp:git.example.com:22"}) {
		t.Errorf("tcp entries (sorted, de-duplicated, bad dropped): %v", got)
	}
	if !IsTCPEntry(" TCP:x") || IsTCPEntry("tcp.example.com") || strings.Contains(strings.Join(httpE, ""), "tcp") {
		t.Error("IsTCPEntry / split disagree")
	}
}

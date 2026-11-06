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
		"tcp:127.0.0.1:22", "tcp:169.254.169.254:22", "tcp:0.0.0.0:22", "tcp:224.0.0.1:22", // never usable
		"tcp:10.0.0.5:2222", "tcp:172.16.0.1:22", "tcp:192.168.1.1:22", // private: the relay always refuses
		"tcp:255.255.255.255:22",
		"tcp:0.1.2.3:22", "tcp:0.255.255.255:22", // 0/8
		"tcp:100.64.0.1:22", "tcp:100.127.255.254:22", // shared address space
		"tcp:192.0.0.7:22", "tcp:198.18.0.7:22", "tcp:198.19.255.255:22", "tcp:240.0.0.1:22", // the relay's fixed fence
		"tcp:172.16.0.1:22", "tcp:172.31.255.255:22", "tcp:10.255.255.255:22",
		"tcp:::ffff:8.8.8.8:22", "tcp:::ffff:10.0.0.5:22", "tcp:::ffff:808:808:22", // IPv4-mapped IPv6, even a public one
		"tcp:2001:db8::5:22", "tcp:[2001:db8::5]:22", // IPv6: the relay is IPv4-only
		"tcp:a..example.com:22",
	}
	for _, in := range bad {
		if e, err := ParseTCPEntry(in); err == nil {
			t.Errorf("%q accepted as %q", in, e.String())
		}
	}
}

// Addresses just outside each refused range stay accepted, so the fence cannot creep.
func TestParseTCPEntryFenceBoundaries(t *testing.T) {
	for _, h := range []string{"1.0.0.1", "100.63.255.255", "100.128.0.1", "172.15.255.255", "172.32.0.1",
		"192.0.1.1", "192.167.255.255", "192.169.0.1", "198.17.255.255", "198.20.0.1", "223.255.255.254", "203.0.113.7"} {
		if _, err := ParseTCPEntry("tcp:" + h + ":22"); err != nil {
			t.Errorf("%s refused: %v", h, err)
		}
	}
}

func TestParseTCPEntryPrivateMessage(t *testing.T) {
	_, err := ParseTCPEntry("tcp:10.0.0.5:2222")
	if err == nil || !strings.Contains(err.Error(), "private or not publicly routable") {
		t.Errorf("error should say the address is private or not routable: %v", err)
	}
	if _, err := ParseTCPEntry("tcp:203.0.113.7:2222"); err != nil {
		t.Errorf("public control refused: %v", err)
	}
}

// A private entry stored before the check existed is left out of the shipped policy
// instead of being sent to a relay that can never connect to it.
func TestSplitTCPSkipsStoredPrivateEntry(t *testing.T) {
	_, tcp := SplitTCP([]string{"tcp:10.0.0.5:2222", "tcp:203.0.113.7:2222"})
	if len(tcp) != 1 || tcp[0].Host != "203.0.113.7" {
		t.Errorf("tcp entries: %+v", tcp)
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

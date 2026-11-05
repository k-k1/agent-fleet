package egressagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTable(t *testing.T, path string, enforce bool, entries ...tcpTableEntry) {
	t.Helper()
	b, _ := json.Marshal(tcpTable{Enforce: enforce, Entries: entries})
	if err := writeAtomic(path, b); err != nil {
		t.Fatal(err)
	}
}

func newTestTCPRelay(t *testing.T) (*TCPRelay, string, string) {
	t.Helper()
	dir := t.TempDir()
	r := &TCPRelay{
		Table: filepath.Join(dir, "af-tcp.json"),
		Log:   &TCPLog{Path: filepath.Join(dir, "af-tcp.log")},
		Local: func(netip.Addr) bool { return false },
		Logf:  t.Logf,
		Now:   func() time.Time { return time.Unix(1_800_000_000, 5_000_000) },
	}
	t.Cleanup(func() { r.Log.Close() })
	return r, r.Table, r.Log.Path
}

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestTCPRelayDecide(t *testing.T) {
	r, table, _ := newTestTCPRelay(t)
	git := tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}}
	db := tcpTableEntry{Host: "db.example.com", From: 11000, To: 11999, Addrs: []string{"198.51.100.4", "203.0.113.9"}}
	dup := tcpTableEntry{Host: "a.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}}
	r.Fence = []netip.Prefix{netip.MustParsePrefix("198.18.0.0/24")}

	cases := []struct {
		name    string
		enforce bool
		have    bool
		dst     string
		want    Decision
	}{
		{"named host, port", true, true, "203.0.113.9:22", Decision{VerdictAdmit, "a.example.com", true}}, // alphabetically first entry wins a tie
		{"named, inside the range", true, true, "198.51.100.4:11500", Decision{VerdictAdmit, "db.example.com", true}},
		{"named address, range edge", true, true, "198.51.100.4:11999", Decision{VerdictAdmit, "db.example.com", true}},
		{"named address, port outside the range", true, true, "198.51.100.4:12000", Decision{VerdictDeny, "", false}},
		{"named address, other port", true, true, "198.51.100.4:22", Decision{VerdictDeny, "", false}},
		{"unnamed address, enforce", true, true, "203.0.113.77:22", Decision{VerdictDeny, "", false}},
		{"unnamed address, log-only", false, true, "203.0.113.77:22", Decision{VerdictWouldDeny, "", true}},
		{"named stays admitted in log-only", false, true, "198.51.100.4:11000", Decision{VerdictAdmit, "db.example.com", true}},
		{"metadata service, log-only", false, true, "169.254.169.254:8080", Decision{VerdictDeny, "", false}},
		{"VPC range, log-only", false, true, "10.0.1.5:22", Decision{VerdictDeny, "", false}},
		{"loopback, log-only", false, true, "127.0.0.1:22", Decision{VerdictDeny, "", false}},
		{"extra fence, log-only", false, true, "198.18.0.7:22", Decision{VerdictDeny, "", false}},
		{"DNS port, log-only", false, true, "203.0.113.77:53", Decision{VerdictDeny, "", false}},
		{"DNS over TLS, log-only", false, true, "203.0.113.77:853", Decision{VerdictDeny, "", false}},
		{"no table yet", false, false, "203.0.113.9:22", Decision{VerdictDeny, "", false}},
	}
	for _, c := range cases {
		if c.have {
			writeTable(t, table, c.enforce, git, db, dup)
		} else {
			os.Remove(table)
		}
		r.loaded, r.tableAt = false, time.Time{} // force a re-read
		if got := r.Decide(ap(c.dst)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}

	// This host's own addresses are never a destination.
	writeTable(t, table, false, git)
	r.loaded = false
	r.Local = func(a netip.Addr) bool { return a == netip.MustParseAddr("203.0.113.9") }
	if d := r.Decide(ap("203.0.113.9:22")); d.Pass {
		t.Errorf("the gateway's own address passed: %+v", d)
	}
}

// A changed table file is picked up without a restart, within tcpTableCheck.
func TestTCPRelayRereadsTheTable(t *testing.T) {
	r, table, _ := newTestTCPRelay(t)
	now := time.Unix(1_800_000_000, 0)
	r.Now = func() time.Time { return now }
	writeTable(t, table, true)
	if d := r.Decide(ap("203.0.113.9:22")); d.Pass {
		t.Fatal("empty table passed")
	}
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	if d := r.Decide(ap("203.0.113.9:22")); d.Pass {
		t.Fatal("re-read before tcpTableCheck")
	}
	now = now.Add(tcpTableCheck)
	if d := r.Decide(ap("203.0.113.9:22")); !d.Pass || d.Entry != "git.example.com" {
		t.Fatalf("table change not picked up: %+v", d)
	}
}

// startTCPRelay runs the relay on a loopback port with a stubbed original destination and
// dialer: dst is what the "workspace" dialled, target is where the pass-through lands.
func startTCPRelay(t *testing.T, r *TCPRelay, dst netip.AddrPort, target string) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.Listener = ln
	r.OrigDst = func(*net.TCPConn) (netip.AddrPort, error) { return dst, nil }
	r.Dial = func(ctx context.Context, got netip.AddrPort) (net.Conn, error) {
		if got != dst {
			return nil, fmt.Errorf("dialled %s, not the original destination %s", got, dst)
		}
		return net.Dial("tcp4", target)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return ln.Addr().String()
}

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func roundTrip(t *testing.T, addr, msg string) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp4", addr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, msg)
	return bufio.NewReader(c).ReadString('\n')
}

func TestTCPRelayPassesANamedDestinationAndRecordsIt(t *testing.T) {
	r, table, logPath := newTestTCPRelay(t)
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	addr := startTCPRelay(t, r, ap("203.0.113.9:22"), echoServer(t))
	got, err := roundTrip(t, addr, "SSH-2.0-test\n")
	if err != nil || got != "SSH-2.0-test\n" {
		t.Fatalf("pass-through: %q, %v", got, err)
	}
	lines := readLog(t, logPath)
	if len(lines) != 1 || lines[0] != "af2 1800000000.005 127.0.0.1 203.0.113.9 22 admit git.example.com" {
		t.Fatalf("log: %q", lines)
	}
	// The agent reads it back as an attributed, allowed event with its port.
	ev, ok := tcpConv(lines[0])
	if !ok || !ev.Allowed || ev.Host != "git.example.com" || ev.Dest != "203.0.113.9" || ev.Port != 22 || ev.Source != "127.0.0.1" || ev.At != 1_800_000_000_005 {
		t.Fatalf("event: %+v ok=%v", ev, ok)
	}
}

func TestTCPRelayRefusesAnUnnamedDestinationAndRecordsIt(t *testing.T) {
	r, table, logPath := newTestTCPRelay(t)
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	addr := startTCPRelay(t, r, ap("203.0.113.77:1433"), echoServer(t))
	if got, err := roundTrip(t, addr, "x\n"); err == nil {
		t.Fatalf("an unnamed destination was passed: %q", got)
	}
	lines := readLog(t, logPath)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " 203.0.113.77 1433 deny -") {
		t.Fatalf("log: %q", lines)
	}
	ev, _ := tcpConv(lines[0])
	if ev.Allowed || ev.Host != "203.0.113.77:1433" || ev.Port != 1433 {
		t.Fatalf("event: %+v", ev)
	}
}

// log-only passes an unnamed destination and records it as a refusal.
func TestTCPRelayLogOnlyPassesAnUnnamedDestination(t *testing.T) {
	r, table, logPath := newTestTCPRelay(t)
	writeTable(t, table, false)
	addr := startTCPRelay(t, r, ap("203.0.113.77:1433"), echoServer(t))
	if got, err := roundTrip(t, addr, "x\n"); err != nil || got != "x\n" {
		t.Fatalf("log-only did not pass: %q, %v", got, err)
	}
	lines := readLog(t, logPath)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " 203.0.113.77 1433 would-deny -") {
		t.Fatalf("log: %q", lines)
	}
	if ev, _ := tcpConv(lines[0]); ev.Allowed {
		t.Fatalf("a would-deny must not read as allowed: %+v", ev)
	}
}

// When the record cannot be written the connection is not passed.
func TestTCPRelayFailsClosedWhenItCannotRecord(t *testing.T) {
	r, table, _ := newTestTCPRelay(t)
	writeTable(t, table, false)
	r.Log = &TCPLog{Path: filepath.Join(t.TempDir(), "missing-dir", "af-tcp.log")}
	addr := startTCPRelay(t, r, ap("203.0.113.9:22"), echoServer(t))
	if got, err := roundTrip(t, addr, "x\n"); err == nil {
		t.Fatalf("passed without a record: %q", got)
	}
}

// At the connection cap the next connection is refused and recorded as deny-cap, and the
// connections already open keep working.
func TestTCPRelayRecordsTheConnectionCap(t *testing.T) {
	r, table, logPath := newTestTCPRelay(t)
	r.MaxConns = 1
	var logs []string
	var mu sync.Mutex
	r.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	addr := startTCPRelay(t, r, ap("203.0.113.9:22"), echoServer(t))

	first, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	fmt.Fprint(first, "one\n")
	first.SetDeadline(time.Now().Add(2 * time.Second))
	if s, err := bufio.NewReader(first).ReadString('\n'); err != nil || s != "one\n" {
		t.Fatalf("first: %q %v", s, err)
	}
	if got, err := roundTrip(t, addr, "two\n"); err == nil {
		t.Fatalf("second passed over the cap: %q", got)
	}
	var capLine string
	for i := 0; i < 100 && capLine == ""; i++ {
		for _, l := range readLog(t, logPath) {
			if strings.Contains(l, " deny-cap ") {
				capLine = l
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasSuffix(capLine, " 203.0.113.9 22 deny-cap -") {
		t.Fatalf("cap hit not recorded: %q", readLog(t, logPath))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "refused") {
		t.Fatalf("cap hit not logged: %q", logs)
	}
}

func TestTCPLogRotatesLikeSquid(t *testing.T) {
	dir := t.TempDir()
	l := &TCPLog{Path: filepath.Join(dir, "af-tcp.log"), MaxBytes: 100, Keep: 3}
	defer l.Close()
	for i := 0; i < 12; i++ {
		if err := l.Record(time.Unix(1_800_000_000+int64(i), 0), netip.MustParseAddr("10.0.1.5"), ap("203.0.113.9:22"), VerdictAdmit, "git.example.com"); err != nil {
			t.Fatal(err)
		}
	}
	files := logFiles(l.Path)
	if len(files) < 3 || len(files) > 4 {
		t.Fatalf("files: %v", files)
	}
	for _, f := range files {
		for _, line := range readLog(t, f) {
			if _, ok := tcpConv(line); !ok {
				t.Errorf("%s: unreadable line %q", f, line)
			}
		}
	}
}

// A peer that never closes after the client has gone must not hold its slot for ever.
func TestTCPRelayReleasesTheSlotWhenThePeerNeverCloses(t *testing.T) {
	r, table, logPath := newTestTCPRelay(t)
	r.MaxConns = 1
	r.HalfClose = 20 * time.Millisecond
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // reads until the client's EOF, then holds the connection open
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(io.Discard, c); time.Sleep(time.Minute); c.Close() }()
		}
	}()
	addr := startTCPRelay(t, r, ap("203.0.113.9:22"), ln.Addr().String())
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(c, "hello\n")
	c.Close()
	time.Sleep(300 * time.Millisecond)
	c2, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	fmt.Fprint(c2, "again\n")
	time.Sleep(100 * time.Millisecond)
	for _, l := range readLog(t, logPath) {
		if strings.Contains(l, "deny-cap") {
			t.Fatalf("the slot was never released: %q", readLog(t, logPath))
		}
	}
}

// A client half-close is passed on and the answer still flows back (SSH, a query).
func TestTCPRelayKeepsHalfClose(t *testing.T) {
	r, table, _ := newTestTCPRelay(t)
	writeTable(t, table, true, tcpTableEntry{Host: "git.example.com", From: 22, To: 22, Addrs: []string{"203.0.113.9"}})
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // answers only after the client's EOF
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintf(c, "got %d bytes\n", len(b))
	}()
	addr := startTCPRelay(t, r, ap("203.0.113.9:22"), ln.Addr().String())
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "hello")
	c.(*net.TCPConn).CloseWrite()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || got != "got 5 bytes\n" {
		t.Fatalf("answer after a half-close: %q, %v", got, err)
	}
}

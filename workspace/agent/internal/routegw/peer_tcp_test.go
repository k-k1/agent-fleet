package routegw

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeProcTCP builds a /proc with one net/tcp table and the given processes, each holding the
// listed socket inodes.
func fakeProcTCP(t *testing.T, tcp, tcp6 string, holders map[int][]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	hdr := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	_ = os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(hdr+tcp), 0o644)
	_ = os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(hdr+tcp6), 0o644)
	for pid, inodes := range holders {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		stat := strconv.Itoa(pid) + " (opencode) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5000 0 0"
		_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
		_ = os.Symlink("/dev/null", filepath.Join(dir, "fd", "0"))
		for i, ino := range inodes {
			_ = os.Symlink("socket:["+ino+"]", filepath.Join(dir, "fd", strconv.Itoa(10+i)))
		}
	}
	return root
}

// The gateway listens on 127.0.0.1:43011 (A803) and the client connected from port 51234 (C822).
// The client's row has local=client, remote=gateway; the gateway's own accepted row is the
// reverse and must not be taken for it.
const (
	rowClient  = "   0: 0100007F:C822 0100007F:A803 01 00000000:00000000 00:00000000 00000000  1000        0 777001 1\n"
	rowGateway = "   1: 0100007F:A803 0100007F:C822 01 00000000:00000000 00:00000000 00000000  1000        0 777002 1\n"
)

var (
	gwAddr     = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0xA803}
	clientAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0xC822}
)

func TestTCPPeerIsTheOneProcessHoldingTheClientSocket(t *testing.T) {
	root := fakeProcTCP(t, rowClient+rowGateway, "", map[int][]string{4242: {"777001"}, 99: {"777002"}})
	id, ok := tcpPeer(root, gwAddr, clientAddr)
	if !ok || id.pid != 4242 || id.start != 5000 {
		t.Fatalf("tcpPeer = %+v %v, want pid 4242 start 5000", id, ok)
	}
}

func TestTCPPeerRefusesWhatItCannotProve(t *testing.T) {
	v4mapped := "   0: 0000000000000000FFFF00000100007F:C822 0000000000000000FFFF00000100007F:A803 01 00000000:00000000 00:00000000 00000000  1000        0 777001 1\n"
	cases := map[string]struct {
		tcp, tcp6 string
		holders   map[int][]string
		remote    *net.TCPAddr
		wantPID   int
	}{
		"socket held by two processes (inherited across fork)": {tcp: rowClient, holders: map[int][]string{4242: {"777001"}, 4343: {"777001"}}},
		"no process holds it (another user's, or gone)":        {tcp: rowClient, holders: map[int][]string{4242: {"1"}}},
		"no such connection": {tcp: rowGateway, holders: map[int][]string{4242: {"777002"}}},
		"not established":    {tcp: strings.Replace(rowClient, " 01 ", " 06 ", 1), holders: map[int][]string{4242: {"777001"}}},
		"inode 0":            {tcp: strings.Replace(rowClient, " 777001 ", " 0 ", 1), holders: map[int][]string{4242: {"0"}}},
		"not a loopback client": {tcp: rowClient, holders: map[int][]string{4242: {"777001"}},
			remote: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 0xC822}},
		"the same connection in both tables": {tcp: rowClient, tcp6: v4mapped, holders: map[int][]string{4242: {"777001"}}},
		// The positive control for the IPv6 table: an IPv4-mapped client is the same address.
		"v4-mapped in tcp6": {tcp6: v4mapped, holders: map[int][]string{4242: {"777001"}}, wantPID: 4242},
	}
	for name, c := range cases {
		root := fakeProcTCP(t, c.tcp, c.tcp6, c.holders)
		remote := c.remote
		if remote == nil {
			remote = clientAddr
		}
		id, ok := tcpPeer(root, gwAddr, remote)
		if c.wantPID != 0 {
			if !ok || id.pid != c.wantPID {
				t.Errorf("%s: tcpPeer = %+v %v, want %d", name, id, ok, c.wantPID)
			}
			continue
		}
		if ok {
			t.Errorf("%s: attributed to %+v, want a refusal", name, id)
		}
	}
}

// The holder's pid is reused between its fd table and its start time being read (the old
// process exited, a new one took the pid): the old socket must not be lent to the new process.
func TestTCPPeerRefusesAHolderReplacedDuringTheScan(t *testing.T) {
	root := fakeProcTCP(t, rowClient, "", map[int][]string{4242: {"777001"}})
	if _, ok := tcpPeer(root, gwAddr, clientAddr); !ok {
		t.Fatal("positive control: the unchanged holder is attributed")
	}
	prev := afterFDScan
	afterFDScan = func(pid int) {
		stat := strconv.Itoa(pid) + " (child) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 9000 0 0"
		_ = os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(stat), 0o644)
	}
	t.Cleanup(func() { afterFDScan = prev })
	if id, ok := tcpPeer(root, gwAddr, clientAddr); ok {
		t.Fatalf("attributed to %+v across a pid reuse", id)
	}
}

// serveOnLoopback serves g on a loopback port the way Start does and returns its base URL.
func serveOnLoopback(t *testing.T, g *Gateway) string {
	t.Helper()
	ln, err := listenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(g)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + ln.Addr().String()
}

func loopbackCall(t *testing.T, base, token, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func converseStreamPath() string { return "/model/" + testModel + "/converse-stream" }

// A loopback token is served on the loopback port to the owner's tree, read through the real
// /proc; it is refused on the socket, and a socket token is refused on the port. The call is
// Converse (what opencode sends) and its ledger row carries the metadata event's counts.
func TestLoopbackTokenIsServedOnTheLoopbackPortOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the peer is read from /proc")
	}
	e := newEnv(t)
	base := serveOnLoopback(t, e.g)
	e.g.d.Loopback = func() string { return base }
	e.upFn = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(esFrame("messageStart", []byte(`{"role":"assistant"}`)))
		_, _ = w.Write(esFrame("metadata", []byte(`{"usage":{"inputTokens":11,"outputTokens":3,"cacheReadInputTokens":2}}`)))
	}
	m := e.session("oc", "p1", "")
	m.Kind = session.KindOpencode
	e.mu.Lock()
	e.metas["oc"] = m
	e.mu.Unlock()
	is, ref := e.g.IssueLoopback(m)
	if ref != nil {
		t.Fatalf("IssueLoopback: %+v", ref)
	}
	if is.Socket != "" || is.BaseURL != base {
		t.Fatalf("loopback grant = socket %q base %q, want no socket and %q", is.Socket, is.BaseURL, base)
	}
	resp, body := loopbackCall(t, base, is.Token, converseStreamPath())
	if resp.StatusCode != 200 {
		t.Fatalf("owner on the loopback port = %d %s", resp.StatusCode, body)
	}
	rows := e.waitRows(1)
	if r := rows[0]; r.In != 11 || r.Out != 3 || r.CacheRead != 2 || r.Kind != session.KindOpencode || r.Ref != "oc" {
		t.Fatalf("ledger row = %+v", r)
	}
	// The same token on the socket: refused, and not revoked.
	if resp, _ := e.call(is.Token, converseStreamPath(), `{}`); resp.StatusCode != 401 || resp.Header.Get("X-Amzn-Errortype") != "route_caller_forbidden" {
		t.Fatalf("loopback token on the socket = %d %q", resp.StatusCode, resp.Header.Get("X-Amzn-Errortype"))
	}
	if resp, _ := loopbackCall(t, base, is.Token, converseStreamPath()); resp.StatusCode != 200 {
		t.Fatalf("the token was revoked by a call on the wrong transport (%d)", resp.StatusCode)
	}
	// A socket token on the loopback port: refused.
	sock := e.issue(e.session("cl", "p1", ""))
	if resp, _ := loopbackCall(t, base, sock, converseStreamPath()); resp.StatusCode != 401 || resp.Header.Get("X-Amzn-Errortype") != "route_caller_forbidden" {
		t.Fatalf("socket token on the loopback port = %d %q", resp.StatusCode, resp.Header.Get("X-Amzn-Errortype"))
	}
	// A caller outside the owner's tree on the loopback port: refused before any upstream call.
	e.mu.Lock()
	e.roots["oc"] = []int{1 << 30}
	before := len(e.upReqs)
	e.mu.Unlock()
	if resp, _ := loopbackCall(t, base, is.Token, converseStreamPath()); resp.StatusCode != 401 || resp.Header.Get("X-Amzn-Errortype") != "route_caller_forbidden" {
		t.Fatalf("foreign caller on the loopback port = %d %q", resp.StatusCode, resp.Header.Get("X-Amzn-Errortype"))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.upReqs) != before {
		t.Fatal("a refused loopback call reached the upstream")
	}
}

func TestIssueLoopbackRefusesWithoutALoopbackListener(t *testing.T) {
	e := newEnv(t)
	e.g.d.Loopback = func() string { return "" }
	if _, ref := e.g.IssueLoopback(e.session("s1", "p1", "")); ref == nil || e.g.Registry().Len() != 0 {
		t.Fatalf("IssueLoopback without a listener = %+v, tokens held %d", ref, e.g.Registry().Len())
	}
	e.g.d.Loopback = nil
	if _, ref := e.g.IssueLoopback(e.session("s2", "p1", "")); ref == nil {
		t.Fatal("IssueLoopback with no loopback at all was accepted")
	}
}

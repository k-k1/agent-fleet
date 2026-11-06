package routegw

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The real listener: a Unix socket at SocketPath, this user's only, a token that works through
// it from the owner's process, no TCP port, and nothing left behind once stopped.
func TestStartServesOnAUnixSocketAndStops(t *testing.T) {
	home, err := os.MkdirTemp("", "rgwh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	e := newEnv(t)
	stop, err := Start(Deps{
		Now:         e.g.d.Now,
		ReadMeta:    e.g.d.ReadMeta,
		Settings:    e.g.d.Settings,
		Held:        e.g.d.Held,
		Credentials: e.g.d.Credentials,
		Append:      func([]usagex.Record) error { return nil },
		Endpoint:    e.g.d.Endpoint,
		Roots:       e.g.d.Roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	g := Default()
	if g == nil {
		t.Fatal("Default is nil while running")
	}
	m := e.session("s1", "p1", "")
	is, ref := g.Issue(m)
	if ref != nil {
		t.Fatalf("Issue: %+v", ref)
	}
	if is.Socket != SocketPath() || is.BaseURL != SocketBaseURL {
		t.Fatalf("issued %q %q", is.Socket, is.BaseURL)
	}
	fi, err := os.Stat(is.Socket)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", fi, err)
	}
	c := socketClient(is.Socket)
	req, _ := http.NewRequest("POST", is.BaseURL+invokePath(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+is.Token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("through the real listener = %d", resp.StatusCode)
	}
	// The same listener refuses a call with no token.
	if resp, _ := c.Post(is.BaseURL+invokePath(), "application/json", strings.NewReader(`{}`)); resp.StatusCode != 401 {
		t.Fatalf("without a token = %d", resp.StatusCode)
	}
	// Ending the session at once, through the package entry the lifecycle exits call.
	if n := EndSession("s1"); n != 1 {
		t.Fatalf("EndSession ended %d", n)
	}
	req, _ = http.NewRequest("POST", is.BaseURL+invokePath(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+is.Token)
	if resp, err := c.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("after EndSession = %v %v", resp, err)
	}
	stop()
	if Default() != nil || EndSession("s1") != 0 {
		t.Fatal("Default is set after stop")
	}
	if _, err := os.Lstat(is.Socket); err == nil {
		t.Fatal("the socket is left behind after stop")
	}
	// A stale socket from an Agent that died is replaced; a file that is not a socket is not ours.
	ln, err := net.Listen("unix", SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	stop2, err := Start(Deps{Settings: e.g.d.Settings})
	if err != nil {
		t.Fatalf("a stale socket blocked the start: %v", err)
	}
	if _, ok := Default().Registry().Lookup(is.Token); ok {
		t.Fatal("a token survived an Agent restart")
	}
	stop2()
	if err := os.WriteFile(SocketPath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(Deps{Settings: e.g.d.Settings}); err == nil {
		t.Fatal("a regular file at the socket path was removed")
	}
}

func TestIssueRefusesWhileNotListening(t *testing.T) {
	e := newEnv(t)
	e.g.d.Socket = func() string { return "" }
	_, ref := e.g.Issue(e.session("s1", "p1", ""))
	if ref == nil || e.g.Registry().Len() != 0 {
		t.Fatalf("Issue without a listener = %+v, tokens held %d", ref, e.g.Registry().Len())
	}
}

func TestSettingsSourcePullsAtMostEveryIntervalAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	pulls, ok := 0, true
	list := map[string]awsx.Profile{"p": {ID: "1"}}
	s := &SettingsSource{
		Now: func() time.Time { return now }, MinInterval: 30 * time.Second, MaxAge: 10 * time.Minute,
		Pull: func() (map[string]awsx.Profile, bool) { pulls++; return list, ok },
	}
	if _, got := s.Get(); !got || pulls != 1 {
		t.Fatalf("first Get: ok=%v pulls=%d", got, pulls)
	}
	now = now.Add(10 * time.Second)
	s.Get()
	if pulls != 1 {
		t.Fatalf("pulled again inside the interval (%d)", pulls)
	}
	// A change in Settings is seen within the interval, not the Agent's five-minute poll.
	list = map[string]awsx.Profile{"p": {ID: "1", ModelRoute: true}}
	now = now.Add(25 * time.Second)
	if m, _ := s.Get(); !m["p"].ModelRoute || pulls != 2 {
		t.Fatalf("a change was not picked up: %+v pulls=%d", m, pulls)
	}
	// The CP unreachable: the last copy serves for MaxAge, then the source cannot say.
	ok = false
	now = now.Add(5 * time.Minute)
	if _, got := s.Get(); !got {
		t.Fatal("a recent copy was refused")
	}
	now = now.Add(6 * time.Minute)
	if _, got := s.Get(); got {
		t.Fatal("a copy older than MaxAge was still served")
	}
	// Never pulled successfully: nothing to serve.
	fresh := &SettingsSource{Now: func() time.Time { return now }, MinInterval: time.Second, MaxAge: time.Minute,
		Pull: func() (map[string]awsx.Profile, bool) { return nil, false }}
	if _, got := fresh.Get(); got {
		t.Fatal("a source that never pulled served a list")
	}
}

func TestRegistryHoldsOnlyHashes(t *testing.T) {
	r := NewRegistry(nil)
	tok, _, err := r.Mint(Binding{Session: "s", Incarnation: "i", Route: "bedrock", ProfileID: "p"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	for k := range r.byKey {
		if strings.Contains(string(k[:]), tok) {
			t.Fatal("the registry holds the token itself")
		}
	}
	r.mu.Unlock()
	if _, ok := r.Lookup(tok[:len(tok)-1] + "0"); ok && !strings.HasSuffix(tok, "0") {
		t.Fatal("a near-miss token was accepted")
	}
	if _, _, err := r.Mint(Binding{}, time.Hour); err == nil {
		t.Fatal("an empty binding was minted")
	}
	r.Revoke(tok)
	if _, ok := r.Lookup(tok); ok {
		t.Fatal("a revoked token still looks up")
	}
}

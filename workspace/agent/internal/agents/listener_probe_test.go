package agents

import (
	"net"
	"path/filepath"
	"testing"
)

func TestListenerMayBeUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := "http://" + ln.Addr().String()
	if !ListenerMayBeUp(up) || !ListenerMayBeUp("ws://"+ln.Addr().String()) {
		t.Fatal("a listening daemon was reported as not running")
	}
	ln.Close()
	if ListenerMayBeUp(up) {
		t.Fatal("a refused connection was reported as maybe running")
	}
	if !ListenerMayBeUp("http://[::1") || !ListenerMayBeUp("") || !ListenerMayBeUp("nonsense") {
		t.Fatal("an address that cannot be parsed must read as maybe running")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	if ListenerMayBeUp("unix://" + sock) {
		t.Fatal("a missing unix socket was reported as maybe running")
	}
	uln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	if !ListenerMayBeUp("unix://" + sock) {
		t.Fatal("a listening unix socket was reported as not running")
	}
}

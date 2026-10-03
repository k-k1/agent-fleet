package listenports

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// tcpLine renders one /proc/net/tcp row the way the kernel prints it.
func tcpLine(local, state, inode string) string {
	return "   0: " + local + " 00000000:0000 " + state + " 00000000:00000000 00:00000000 00000000  1000        0 " + inode + " 1 0000000000000000 100 0 0 10 0\n"
}

// fakeProc lays out a /proc with the given tables and processes. procs maps pid → (environ
// entries, socket inodes held).
func fakeProc(t *testing.T, tcp, tcp6 string, procs map[string]struct {
	env    []string
	inodes []string
}) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "net"), 0o755))
	must(os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcpHeader+tcp), 0o644))
	must(os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(tcpHeader+tcp6), 0o644))
	for pid, p := range procs {
		dir := filepath.Join(root, pid)
		must(os.MkdirAll(filepath.Join(dir, "fd"), 0o755))
		must(os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(p.env, "\x00")+"\x00"), 0o644))
		// A non-socket fd beside the sockets, as every real process has.
		must(os.Symlink("/dev/null", filepath.Join(dir, "fd", "0")))
		for i, ino := range p.inodes {
			must(os.Symlink("socket:["+ino+"]", filepath.Join(dir, "fd", string(rune('3'+i)))))
		}
	}
	return root
}

type proc = struct {
	env    []string
	inodes []string
}

func TestScanAttributesPortsByTheProcessSessionOnly(t *testing.T) {
	tcp := tcpLine("0100007F:1435", "0A", "100") + // 127.0.0.1:5173, session a
		tcpLine("00000000:1F90", "0A", "101") + // 0.0.0.0:8080, session b
		tcpLine("0100007F:0BB8", "0A", "102") + // 127.0.0.1:3000, a process with no session
		tcpLine("0100007F:1E14", "0A", "103") + // 127.0.0.1:7700, the Agent's port
		tcpLine("0100007F:1F91", "01", "104") + // ESTABLISHED, not a listener
		tcpLine("0B00007F:1F92", "0A", "105") // 127.0.0.11:8082 (Docker DNS), unreachable on 127.0.0.1
	tcp6 := tcpLine("00000000000000000000000000000000:2328", "0A", "200") + // [::]:9000, session a
		tcpLine("00000000000000000000000001000000:2329", "0A", "201") + // [::1]:9001, ::1 only
		tcpLine("0000000000000000FFFF00000100007F:232A", "0A", "202") // v4-mapped 127.0.0.1:9002, session b
	root := fakeProc(t, tcp, tcp6, map[string]proc{
		"10": {env: []string{"PATH=/bin", "AF_SESSION_NAME=a"}, inodes: []string{"100", "200", "201"}},
		"11": {env: []string{"AF_SESSION_NAME=b"}, inodes: []string{"101", "104", "105", "202"}},
		"12": {env: []string{"PATH=/bin"}, inodes: []string{"102"}},
		"13": {env: []string{"AF_SESSION_NAME=a"}, inodes: []string{"103"}},
		// A second process of session a holding the same listener (a forked worker).
		"14": {env: []string{"AF_SESSION_NAME=a"}, inodes: []string{"100"}},
	})
	got := Scan(root)
	want := map[string][]int{"a": {5173, 9000}, "b": {8080, 9002}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan = %v, want %v", got, want)
	}
}

func TestScanWithoutListenersSkipsTheProcessWalk(t *testing.T) {
	root := fakeProc(t, tcpLine("0100007F:1F91", "01", "104"), "", map[string]proc{
		"10": {env: []string{"AF_SESSION_NAME=a"}, inodes: []string{"104"}},
	})
	// An unreadable pid dir would fail the walk; with nothing listening it must not be reached.
	if err := os.Chmod(filepath.Join(root, "10"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "10"), 0o755) })
	if got := Scan(root); len(got) != 0 {
		t.Fatalf("Scan = %v, want empty", got)
	}
}

func TestScanCapsPortsPerSession(t *testing.T) {
	var tcp string
	var inodes []string
	for i := 0; i < maxPortsPerSession+3; i++ {
		ino := string(rune('a' + i))
		tcp += tcpLine("0100007F:"+strings.ToUpper(hexPort(4000+i)), "0A", ino)
		inodes = append(inodes, ino)
	}
	root := fakeProc(t, tcp, "", map[string]proc{"10": {env: []string{"AF_SESSION_NAME=a"}, inodes: inodes}})
	got := Scan(root)["a"]
	if len(got) != maxPortsPerSession || got[0] != 4000 {
		t.Fatalf("Scan[a] = %v, want the lowest %d ports", got, maxPortsPerSession)
	}
}

func hexPort(p int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[p>>12&15], digits[p>>8&15], digits[p>>4&15], digits[p&15]})
}

func TestCacheRescansOnlyAfterTTL(t *testing.T) {
	root := fakeProc(t, tcpLine("0100007F:1435", "0A", "100"), "", map[string]proc{
		"10": {env: []string{"AF_SESSION_NAME=a"}, inodes: []string{"100"}},
	})
	c := &Cache{Root: root, TTL: 10 * time.Second}
	t0 := time.Unix(1000, 0)
	if got := c.Get(t0)["a"]; !reflect.DeepEqual(got, []int{5173}) {
		t.Fatalf("first Get = %v", got)
	}
	// The server stops: within the TTL the held scan answers, after it the new one does.
	if err := os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcpHeader), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := c.Get(t0.Add(5 * time.Second))["a"]; !reflect.DeepEqual(got, []int{5173}) {
		t.Fatalf("Get within TTL = %v, want the held scan", got)
	}
	if got := c.Get(t0.Add(11 * time.Second)); len(got) != 0 {
		t.Fatalf("Get after TTL = %v, want a fresh empty scan", got)
	}
}

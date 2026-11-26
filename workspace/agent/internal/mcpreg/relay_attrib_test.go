package mcpreg

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeCaller makes the relay see its caller as pid, whose environment under a fake /proc is env.
// It records the addresses it was asked about.
func fakeCaller(t *testing.T, pid int, env string) *[2]*net.TCPAddr {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen [2]*net.TCPAddr
	oldPID, oldRoot := CallerPID, procRoot
	CallerPID = func(local, remote *net.TCPAddr) (int, bool) {
		seen[0], seen[1] = local, remote
		return pid, true
	}
	procRoot = root
	t.Cleanup(func() { CallerPID, procRoot = oldPID, oldRoot })
	return &seen
}

func TestElicitRelayAttributesTheHeldCallToTheCallersSession(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	seen := fakeCaller(t, 4242, "PATH=/bin\x00AF_SESSION_NAME=fix-mail\x00HOME=/h\x00")
	ch := e.startCall(elicitCallBody, sseHeaders)

	p := waitPending(t, 1)[0]
	if p.Session != "fix-mail" {
		t.Fatalf("session %q, want fix-mail", p.Session)
	}
	// The lookup is asked about this very connection: the relay's end and the CLI's.
	_, port, _ := net.SplitHostPort(e.base[len("http://"):])
	if seen[0] == nil || seen[1] == nil || strconv.Itoa(seen[0].Port) != port || !seen[1].IP.IsLoopback() || seen[1].Port == seen[0].Port {
		t.Fatalf("asked about local %v remote %v (relay port %s)", seen[0], seen[1], port)
	}
	got, err := AnswerElicitation(p.ID, allowOnce)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || got.Session != "fix-mail" || got.Tool != "send_mail" || got.ServerID != relayTestID {
		t.Fatalf("answered %+v", got)
	}
	waitCall(t, ch)
}

func TestCallerSessionNamesOnlyAWellFormedSession(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
	}{
		{"named", "AF_SESSION_NAME=abc-1\x00", "abc-1"},
		{"none", "PATH=/bin\x00", ""},
		{"empty", "AF_SESSION_NAME=\x00", ""},
		{"not a slug", "AF_SESSION_NAME=../x\x00", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, jsonAnswer(askThenComplete))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			fakeCaller(t, 77, tc.env)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if p.Session != tc.want {
				t.Fatalf("session %q, want %q", p.Session, tc.want)
			}
			if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); err != nil {
				t.Fatal(err)
			}
			waitCall(t, ch)
		})
	}
}

func TestElicitRelayLeavesACallUnattributedWithoutALookup(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	old := CallerPID
	CallerPID = func(*net.TCPAddr, *net.TCPAddr) (int, bool) { return 0, false }
	t.Cleanup(func() { CallerPID = old })
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	if p.Session != "" {
		t.Fatalf("session %q, want none", p.Session)
	}
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); err != nil {
		t.Fatal(err)
	}
	waitCall(t, ch)
}

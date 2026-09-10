package main

// The wrapper against a REAL authenticating server.
//
// Everything else about the wrapper is argv analysis, and argv analysis is exactly the
// kind of thing that passes its own unit tests while the command still fails: the unit
// tests assert what we BUILD, and only svn can say whether it accepts it. So this starts
// an svnserve that refuses anonymous access and drives the wrapper end to end — checkout,
// update and commit, none of them carrying a credential on the command line.
//
// The positive control is the point: the same command with no stored credential must
// FAIL. Without it, "the checkout worked" would also be the result of a server that never
// asked for a password.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// startSvnserve brings up an authenticating svnserve over a loopback port and returns its
// repository URL. Skips (never fails) when svnserve is absent or the port cannot be
// taken — the workspace shares one network namespace between sessions, so a port is not
// ours to insist on.
func startSvnserve(t *testing.T, root, user, pass string) string {
	t.Helper()
	if _, err := exec.LookPath("svnserve"); err != nil {
		t.Skip("svnserve not installed")
	}
	srv := filepath.Join(root, "srv")
	if out, err := exec.Command("svnadmin", "create", srv).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	conf := "[general]\nanon-access = none\nauth-access = write\npassword-db = passwd\nrealm = af-test\n"
	if err := os.WriteFile(filepath.Join(srv, "conf", "svnserve.conf"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "conf", "passwd"), []byte(fmt.Sprintf("[users]\n%s = %s\n", user, pass)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Ask the OS for a free port, then let it go: svnserve has no "port 0" of its own.
	// The gap is why a failure to come up is a skip.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot reserve a loopback port")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cmd := exec.Command("svnserve", "-d", "--foreground", "--listen-host", "127.0.0.1",
		"--listen-port", fmt.Sprint(port), "-r", root)
	if err := cmd.Start(); err != nil {
		t.Skipf("svnserve start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return fmt.Sprintf("svn://%s/srv", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skipf("svnserve did not answer on %s", addr)
	return ""
}

func TestSvnWrapperAuthenticatesAgainstRealServer(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	const user, pass = "alice", "s3cret"
	// Built BEFORE HOME is redirected below: `go build` under a throwaway HOME re-downloads
	// the module cache into it (measured: 62s, and a temp dir that then cannot be removed).
	bin := buildAgentBinary(t)
	root := t.TempDir()
	base := startSvnserve(t, root, user, pass)
	trunk := base + "/trunk"

	// Seed one revision, with the credential passed explicitly — this is the Agent's own
	// REST shape (runSvnAuthed), not the wrapper's.
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	creds := &secrets.SVNCred{URLPrefix: base, Username: user, Password: pass}
	if out, err := runSvnAuthed(t.Context(), creds, "import", "-m", "seed commit", seed, trunk); err != nil {
		t.Fatalf("seed import: %v: %s", err, out)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "") // plaintext store in the throwaway home
	wc := filepath.Join(root, "wc")

	// Positive control FIRST: with nothing stored, the wrapper must change nothing, and
	// the server must refuse. If this passes, the checkout below proves nothing.
	if out, err := runWrappedSvn(t, bin, "checkout", trunk, wc); err == nil {
		t.Fatalf("anonymous checkout succeeded — the server is not authenticating: %s", out)
	} else if !svnAuthFailure(out) {
		t.Fatalf("expected an auth failure, got: %v: %s", err, out)
	}

	if err := svnSaveCred(base, user, pass, false); err != nil {
		t.Fatal(err)
	}
	if out, err := runWrappedSvn(t, bin, "checkout", trunk, wc); err != nil {
		t.Fatalf("checkout through the wrapper: %v: %s", err, out)
	}
	if !isSvnRepo(wc) {
		t.Fatal("no working copy after the wrapped checkout")
	}
	// update: no URL on the command line at all, so the wrapper has to ask the working
	// copy which server it belongs to.
	if out, err := runWrappedSvn(t, bin, "update", wc); err != nil {
		t.Fatalf("update through the wrapper: %v: %s", err, out)
	}
	// commit: a network write, with the message on the command line so stdin stays ours.
	if err := os.WriteFile(filepath.Join(wc, "hello.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runWrappedSvn(t, bin, "commit", "-m", "wrapped commit", wc); err != nil {
		t.Fatalf("commit through the wrapper: %v: %s", err, out)
	}
	out, err := runWrappedSvn(t, bin, "log", "-l", "1", trunk)
	if err != nil {
		t.Fatalf("log through the wrapper: %v: %s", err, out)
	}
	if !strings.Contains(out, "wrapped commit") {
		t.Fatalf("the commit did not reach the server: %s", out)
	}
}

// runWrappedSvn runs one svn command the way a session would: through
// `workspace-agent svn-run`, with no credential in sight. bin is the real binary
// (buildAgentBinary) — the wrapper is a SUBCOMMAND of it, so exercising it any other way
// would exercise a copy.
func runWrappedSvn(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"svn-run"}, args...)...)
	cmd.Env = append(os.Environ(), svnRealEnv+"="+realSvnPath(), svnWrappedEnv+"=")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

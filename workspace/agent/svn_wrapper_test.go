package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// seedSvnWorkingCopy builds a real file:// repository with a trunk and checks it out,
// returning the working copy and its URL. A file:// server needs no auth, which is what
// makes it usable for the parts of this that are about URLs and storage rather than
// passwords.
func seedSvnWorkingCopy(t *testing.T) (wc, url string) {
	t.Helper()
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	root := t.TempDir()
	srv := filepath.Join(root, "srv")
	if out, err := exec.Command("svnadmin", "create", srv).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	url = "file://" + srv + "/trunk"
	if out, err := exec.Command("svn", "import", "--non-interactive", "-m", "seed commit", seed, url).CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}
	wc = filepath.Join(root, "wc")
	if out, err := runSvnAuthedHealing(context.Background(), wc, nil, "checkout", url, wc); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	return wc, url
}

func cred() *secrets.SVNCred {
	return &secrets.SVNCred{URLPrefix: "https://svn.example.com/proj", Username: "alice", Password: "s3cret"}
}

// The wrapper's whole contract in one table: it either injects the stored credential, or
// it leaves the command exactly as typed. There is no third outcome — a wrapper that
// changed what a command MEANS would be worse than no wrapper.
func TestPlanSvnWrapper(t *testing.T) {
	trust := &secrets.SVNCred{URLPrefix: "https://svn.example.com", TrustCert: true}
	userTrust := &secrets.SVNCred{URLPrefix: "https://svn.example.com", Username: "alice", Password: "s3cret", TrustCert: true}
	cases := []struct {
		name  string
		args  []string
		creds *secrets.SVNCred
		want  []string
		feed  bool
	}{
		{
			name: "nothing stored: passes through untouched",
			args: []string{"update"}, creds: nil, want: []string{"update"},
		},
		{
			name: "update takes the credential on stdin",
			args: []string{"update"}, creds: cred(),
			want: []string{"update", "--username", "alice", "--password-from-stdin"}, feed: true,
		},
		{
			name: "the injection goes after the subcommand, before its arguments",
			args: []string{"checkout", "https://svn.example.com/proj/trunk", "wc"}, creds: cred(),
			want: []string{"checkout", "--username", "alice", "--password-from-stdin", "https://svn.example.com/proj/trunk", "wc"},
			feed: true,
		},
		{
			name: "a local-only subcommand is left alone — svn rejects --username there",
			args: []string{"add", "f.txt"}, creds: cred(), want: []string{"add", "f.txt"},
		},
		{
			name: "cleanup likewise (it is the fix for a wedged lock; it must never gain options)",
			args: []string{"cleanup", "/home/dev/repos/docs"}, creds: cred(), want: []string{"cleanup", "/home/dev/repos/docs"},
		},
		{
			name: "an explicit --password wins",
			args: []string{"update", "--username", "bob", "--password", "x"}, creds: cred(),
			want: []string{"update", "--username", "bob", "--password", "x"},
		},
		{
			name: "an explicit --password-from-stdin wins (this is how the Agent's own REST path calls svn)",
			args: []string{"update", "--username", "alice", "--password-from-stdin"}, creds: cred(),
			want: []string{"update", "--username", "alice", "--password-from-stdin"},
		},
		{
			name: "another account named on the command line gets no password of ours",
			args: []string{"update", "--username", "bob"}, creds: cred(), want: []string{"update", "--username", "bob"},
		},
		{
			name: "the same account named explicitly still gets the password, and no second --username",
			args: []string{"update", "--username", "alice"}, creds: cred(),
			want: []string{"update", "--password-from-stdin", "--username", "alice"}, feed: true,
		},
		{
			name: "commit with no -m opens an editor: stdin stays the terminal's",
			args: []string{"commit"}, creds: cred(), want: []string{"commit"},
		},
		{
			name: "commit WITH -m reads no stdin, so it is authenticated",
			args: []string{"commit", "-m", "fix"}, creds: cred(),
			want: []string{"commit", "--username", "alice", "--password-from-stdin", "-m", "fix"}, feed: true,
		},
		{
			name: "the attached short form counts as a message too",
			args: []string{"commit", "-mfix"}, creds: cred(),
			want: []string{"commit", "--username", "alice", "--password-from-stdin", "-mfix"}, feed: true,
		},
		{
			name: "a command reading its message from stdin keeps stdin",
			args: []string{"commit", "-F", "-"}, creds: cred(), want: []string{"commit", "-F", "-"},
		},
		{
			name: "--targets - likewise",
			args: []string{"commit", "-m", "x", "--targets", "-"}, creds: cred(), want: []string{"commit", "-m", "x", "--targets", "-"},
		},
		{
			name: "a trust-only entry contributes cert trust and no password",
			args: []string{"update"}, creds: trust, want: []string{"update", svnTrustFailures},
		},
		{
			name: "trust plus a credential contributes both",
			args: []string{"update"}, creds: userTrust,
			want: []string{"update", svnTrustFailures, "--username", "alice", "--password-from-stdin"}, feed: true,
		},
		{
			name: "trust already on the command line is not repeated",
			args: []string{"update", "--trust-server-cert-failures=unknown-ca"}, creds: trust,
			want: []string{"update", "--trust-server-cert-failures=unknown-ca"},
		},
		{
			name: "a global option before the subcommand does not hide it",
			args: []string{"--config-dir", "/tmp/cfg", "update"}, creds: cred(),
			want: []string{"--config-dir", "/tmp/cfg", "update", "--username", "alice", "--password-from-stdin"}, feed: true,
		},
		{
			name: "no subcommand at all (svn --version)",
			args: []string{"--version"}, creds: cred(), want: []string{"--version"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planSvnWrapper(c.args, c.creds)
			if !reflect.DeepEqual(got.Args, c.want) {
				t.Errorf("args = %q, want %q", got.Args, c.want)
			}
			if feed := got.Password != ""; feed != c.feed {
				t.Errorf("feed password = %v, want %v", feed, c.feed)
			}
		})
	}
}

func TestSvnWrapperURLDetection(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://svn.example.com/proj/trunk", "https://svn.example.com/proj/trunk"},
		{"svn+ssh://host/repo", "svn+ssh://host/repo"},
		{"file:///srv/repo", "file:///srv/repo"},
		{"/home/dev/repos/docs", ""},
		{"docs", ""},
		{"://nope", ""},
	}
	for _, c := range cases {
		if got := svnFirstURL([]string{"info", c.in}); got != c.want {
			t.Errorf("svnFirstURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// An option's VALUE must never be mistaken for the target — the credential would then
	// be looked up for the wrong server.
	if got := svnFirstURL([]string{"update", "--config-option", "servers:global:http-proxy-host=x", "wc"}); got != "" {
		t.Errorf("option value read as a URL: %q", got)
	}
}

func TestSvnFirstPath(t *testing.T) {
	args := []string{"update", "--depth", "infinity", "/home/dev/repos/docs"}
	if got := svnFirstPath(args, 0); got != "/home/dev/repos/docs" {
		t.Errorf("svnFirstPath = %q", got)
	}
	if got := svnFirstPath([]string{"update"}, 0); got != "" {
		t.Errorf("bare update should name no path, got %q", got)
	}
}

// The classifier decides whether the Console offers "enter your password" or reports a
// failure. E170013 is the one that must NOT count: svn prints it for an unreachable host
// just as readily as for a rejected password.
func TestSvnAuthFailure(t *testing.T) {
	yes := []string{
		"svn: E170001: Authorization failed",
		"svn: E215004: No more credentials or we tried too many times.\nsvn: E170001: Authentication failed",
		"svn: E175013: Access to '/repo/!svn/vcc/default' forbidden",
		"svn: Could not authenticate to server: rejected Basic challenge",
	}
	for _, s := range yes {
		if !svnAuthFailure(s) {
			t.Errorf("expected an auth failure: %q", s)
		}
	}
	no := []string{
		"svn: E170013: Unable to connect to a repository at URL 'https://svn.example.com/proj'\nsvn: E730054: Connection reset by peer",
		"svn: E155004: Working copy locked",
		"svn: E160013: Path not found",
		"",
	}
	for _, s := range no {
		if svnAuthFailure(s) {
			t.Errorf("not an auth failure: %q", s)
		}
	}
}

// A trust-only entry saved at checkout time is the state that sends people here: cert
// trust persisted, the password declined. The prefix a re-authentication is stored under
// must be THAT entry's, not a longer one that would merely shadow it.
func TestSvnAuthPrefixPrefersTheGoverningEntry(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "")
	wc, url := seedSvnWorkingCopy(t)
	root := strings.TrimSuffix(url, "/trunk")

	if prefix, got := svnAuthPrefixFor(wc); got != url || prefix != root {
		t.Fatalf("with no stored entry: prefix=%q url=%q, want %q / %q", prefix, got, root, url)
	}
	if err := svnSaveCred(root, "", "", true); err != nil { // trust-only, as a checkout without "save" leaves it
		t.Fatal(err)
	}
	if prefix, _ := svnAuthPrefixFor(wc); prefix != root {
		t.Fatalf("prefix = %q, want the governing entry %q", prefix, root)
	}
}

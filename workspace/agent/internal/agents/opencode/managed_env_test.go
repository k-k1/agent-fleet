package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func managedMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged}
	session.WriteMeta(m)
	return m
}

func stubServeEnviron(t *testing.T, env ...string) {
	t.Helper()
	old := serveEnviron
	serveEnviron = func(string) ([]string, error) { return env, nil }
	t.Cleanup(func() { serveEnviron = old })
}

// The environment a Managed session's shell gets is the server's, whole, plus its own two
// variables: PUT replaces the shell environment, so anything dropped here vanishes from the shell,
// and anything added is new. An AF_WORK_DIR / AF_SESSION_NAME the server inherited is another
// session's and is replaced.
func TestManagedShellEnvironmentIsTheServersPlusTheSessionsOwn(t *testing.T) {
	server := []string{"PATH=/jdk/bin:/usr/bin", "JAVA_HOME=/jdk", "ANTHROPIC_API_KEY=k", "OPENCODE_SERVER_PASSWORD=pw",
		"AF_WORK_DIR=/elsewhere", "AF_SESSION_NAME=stale"}
	got := managedShellEnvironment(server, "mgd")
	want := map[string]string{"PATH": "/jdk/bin:/usr/bin", "JAVA_HOME": "/jdk", "ANTHROPIC_API_KEY": "k", "OPENCODE_SERVER_PASSWORD": "pw",
		"AF_WORK_DIR": paths.SessionWorkDir("mgd"), "AF_SESSION_NAME": "mgd"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if st, err := os.Stat(want["AF_WORK_DIR"]); err != nil || !st.IsDir() {
		t.Errorf("the session's work dir was not created: %v", err)
	}
	if !strings.HasSuffix(want["AF_WORK_DIR"], filepath.Join(".af-work", "mgd")) {
		t.Errorf("work dir %s", want["AF_WORK_DIR"])
	}
}

// What the plugin asks before a Managed session's shell: the session must carry the name's tag, the
// server's own environment goes on it with that name's two variables, and no other session (or an
// unnamed one) is touched.
func TestPutEnvironmentForAManagedSession(t *testing.T) {
	d := newSharedDaemon(t)
	managedMeta(t, "mgdA")
	managedMeta(t, "mgdB")
	stubServeEnviron(t, "PATH=/x", "TOOLCHAIN=tc")
	d.sessions["ses_a"] = map[string]string{metaManaged: "1", metaManagedName: "mgdA"}
	d.sessions["ses_b"] = map[string]string{metaManaged: "1", metaManagedName: "mgdB"}
	d.sessions["ses_legacy"] = map[string]string{metaManaged: "1"}

	if err := putEnvironmentFor(d.srv.URL, "mgdA", "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := putEnvironmentFor(d.srv.URL, "mgdB", "ses_b"); err != nil {
		t.Fatal(err)
	}
	a, b := d.envs["ses_a"], d.envs["ses_b"]
	if a["AF_SESSION_NAME"] != "mgdA" || b["AF_SESSION_NAME"] != "mgdB" || a["AF_WORK_DIR"] == b["AF_WORK_DIR"] {
		t.Errorf("sessions see each other's values: %v / %v", a, b)
	}
	if a["PATH"] != "/x" || a["TOOLCHAIN"] != "tc" {
		t.Errorf("the server's environment was not kept: %v", a)
	}
	for _, c := range []struct{ name, ses string }{{"mgdA", "ses_b"}, {"mgdA", "ses_legacy"}, {"mgdA", "ses_missing"}, {"nobody", "ses_a"}} {
		if err := putEnvironmentFor(d.srv.URL, c.name, c.ses); err == nil {
			t.Errorf("%s on %s must be refused", c.name, c.ses)
		}
	}
	if _, has := d.envs["ses_legacy"]; has {
		t.Error("an unnamed Managed session got an environment")
	}
	// An unreadable server environment refuses rather than PUTting a stripped one.
	delete(d.envs, "ses_a")
	serveEnviron = func(string) ([]string, error) { return nil, os.ErrNotExist }
	if err := putEnvironmentFor(d.srv.URL, "mgdA", "ses_a"); err == nil {
		t.Error("no server environment must be an error")
	}
	if _, has := d.envs["ses_a"]; has {
		t.Error("a stripped environment was put")
	}
}

// A fork copies the source's metadata, so a re-stamp has to replace the name, and an existing
// session made before the name existed gets it.
func TestTagManagedSessionRestampsAForkAndLegacy(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_fork"] = map[string]string{metaManaged: "1", metaManagedName: "source", "keep": "me"}
	d.sessions["ses_old"] = map[string]string{metaManaged: "1"}
	for _, ses := range []string{"ses_fork", "ses_old"} {
		if err := tagManagedSession(d.srv.URL, ses, "mine"); err != nil {
			t.Fatal(err)
		}
		if got := d.sessions[ses]; got[metaManagedName] != "mine" || got[metaManaged] != "1" {
			t.Errorf("%s: %v", ses, got)
		}
	}
	if d.sessions["ses_fork"]["keep"] != "me" {
		t.Error("other metadata keys must survive")
	}
	n := len(d.calls)
	if err := tagManagedSession(d.srv.URL, "ses_old", "mine"); err != nil || len(d.calls) != n+1 {
		t.Errorf("an already stamped session is read, not written: %v %v", err, d.calls[n:])
	}
}

package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func withLoggedIn(t *testing.T, v bool) {
	t.Helper()
	prev := loggedInProbe
	loggedInProbe = func() bool { return v }
	t.Cleanup(func() { loggedInProbe = prev })
}

func flowOf(t *testing.T, script string) *agents.Flow {
	t.Helper()
	f, err := agents.StartFlow(exec.Command("sh", "-c", script))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

// Re-authentication runs the login over the one it replaces, so status reads loggedIn from
// the start. That alone must not count as the new sign-in.
func TestAwaitLoginIgnoresTheLoginBeingReplaced(t *testing.T) {
	withLoggedIn(t, true)
	f := flowOf(t, "echo 'Paste code here if prompted >'; sleep 30")
	if ok, _ := awaitLogin(f, 1500*time.Millisecond); ok {
		t.Fatal("awaitLogin reported success from the existing login before the CLI finished")
	}
}

func TestAwaitLoginAcceptsTheCLIsSuccess(t *testing.T) {
	withLoggedIn(t, true)
	f := flowOf(t, "echo 'Login successful.'; sleep 30")
	if ok, msg := awaitLogin(f, 5*time.Second); !ok {
		t.Fatalf("awaitLogin = false (%q) after the CLI reported success", msg)
	}
	g := flowOf(t, "exit 0")
	if ok, msg := awaitLogin(g, 5*time.Second); !ok {
		t.Fatalf("awaitLogin = false (%q) after a clean exit", msg)
	}
}

// The marker can come before the CLI's last write. On a re-authentication status already reads
// loggedIn from the old login, so returning on the marker would let the caller's Close kill the
// CLI mid-write: awaitLogin has to see it exit first.
func TestAwaitLoginLetsTheCLIFinishWriting(t *testing.T) {
	withLoggedIn(t, true)
	done := filepath.Join(t.TempDir(), "written")
	f := flowOf(t, "echo 'Login successful.'; sleep 1; touch "+done)
	if ok, msg := awaitLogin(f, 10*time.Second); !ok {
		t.Fatalf("awaitLogin = false (%q)", msg)
	}
	f.Close()
	if _, err := os.Stat(done); err != nil {
		t.Fatal("awaitLogin returned before the CLI finished; Close cut its last write")
	}
}

func TestAwaitLoginFailures(t *testing.T) {
	withLoggedIn(t, true)
	f := flowOf(t, "echo 'OAuth error: invalid code'; sleep 30")
	if ok, msg := awaitLogin(f, 5*time.Second); ok || msg != "OAuth error: invalid code" {
		t.Fatalf("awaitLogin = %v, %q", ok, msg)
	}
	start := time.Now()
	g := flowOf(t, "exit 3")
	if ok, _ := awaitLogin(g, 10*time.Second); ok {
		t.Fatal("awaitLogin = true after a failed exit")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("awaitLogin waited %v after the CLI exited", d)
	}
	withLoggedIn(t, false)
	h := flowOf(t, "echo 'Login successful.'")
	if ok, _ := awaitLogin(h, 5*time.Second); ok {
		t.Fatal("awaitLogin = true with no credentials behind the CLI's success")
	}
}

package routepolicy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

func withoutKey(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// A tmux server started while the Agent still carried the bridge token keeps it in its global
// environment, which every new pane inherits. Opening the shared store registers the token with
// the launch environment, so a pane launched afterwards on that old server does not see it.
func TestSharedKeepsTheTokenOutOfPanesOnAPreexistingServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("HOME", t.TempDir())
	sock := fmt.Sprintf("af-rp-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Setenv("AF_TMUX_SOCKET", sock)
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
	t.Cleanup(func() { tmuxx.SetLaunchEnv(nil, nil) })
	reset := func() { sharedOnce, shared, tokenOnce, token = sync.Once{}, nil, sync.Once{}, "" }
	reset()
	t.Cleanup(reset)

	dirty := tmuxx.Cmd("new-session", "-d", "-s", "old", "sleep 60")
	dirty.Env = append(withoutKey(os.Environ(), EnvToken), EnvToken+"=afc_secret")
	if out, err := dirty.CombinedOutput(); err != nil {
		t.Fatalf("start the old server: %v: %s", err, out)
	}

	t.Setenv(EnvToken, "afc_secret")
	Shared()
	if _, set := os.LookupEnv(EnvToken); set {
		t.Fatal("the token is still in the Agent's environment")
	}

	file := filepath.Join(t.TempDir(), "env")
	probe := `{ [ "${` + EnvToken + `+x}" ] && echo token=set || echo token=unset; } > ` + file
	launch := tmuxx.Cmd("new-session", "-d", "-s", "new", probe)
	launch.Env = withoutKey(os.Environ(), EnvToken)
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	var got string
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(file); err == nil && strings.HasSuffix(string(b), "\n") {
			got = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != "token=unset\n" {
		t.Errorf("a new pane on the old server sees: %q", got)
	}
}

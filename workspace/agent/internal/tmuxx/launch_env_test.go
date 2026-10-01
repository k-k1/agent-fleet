package tmuxx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pane gets its environment from the tmux SERVER, so a server that was started while the
// Agent still carried the workload variables keeps handing them to every new session even
// after the Agent dropped them. The launch has to clean the server's global environment.
func TestNewSessionScrubsAPreexistingServerEnvironment(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("af-launchenv-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Setenv("AF_TMUX_SOCKET", sock)
	t.Cleanup(func() { _ = Cmd("kill-server").Run() })
	t.Cleanup(func() { SetLaunchEnv(nil, nil) })

	// The dirty server: started with the variable, as one started before the Agent's boot.
	dirty := Cmd("new-session", "-d", "-s", "old", "sleep 60")
	dirty.Env = append(os.Environ(), "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/v2/credentials/fake")
	dirty.Env = withoutKey(dirty.Env, "AWS_EC2_METADATA_DISABLED")
	if out, err := dirty.CombinedOutput(); err != nil {
		t.Fatalf("start dirty server: %v: %s", err, out)
	}

	SetLaunchEnv([]string{"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"}, []string{"AWS_EC2_METADATA_DISABLED=true"})

	// The new session is launched by a clean client, the way the Agent launches after boot.
	file := filepath.Join(t.TempDir(), "env")
	probe := `{ [ "${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI+x}" ] && echo uri=set || echo uri=unset; ` +
		`echo imds=${AWS_EC2_METADATA_DISABLED:-unset}; } > ` + file
	launch := Cmd("new-session", "-d", "-s", "new", probe)
	launch.Env = withoutKey(withoutKey(os.Environ(), "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"), "AWS_EC2_METADATA_DISABLED")
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	var got string
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(file); err == nil && strings.Count(string(b), "\n") == 2 {
			got = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != "uri=unset\nimds=true\n" {
		t.Errorf("a new pane on a pre-existing server sees:\n%s", got)
	}
}

func withoutKey(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

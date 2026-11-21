package contractenv

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestIsolateDropsCredentialsAndProfilesAndRestores(t *testing.T) {
	for _, k := range []string{"KIRO_HOME", "KIRO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CODEX_HOME", "XDG_CONFIG_HOME", "E2E_CLAUDE_OAUTH_TOKEN"} {
		t.Setenv(k, "secret")
	}
	t.Run("isolated", func(t *testing.T) {
		home := Isolate(t)
		for _, k := range []string{"KIRO_HOME", "KIRO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CODEX_HOME", "XDG_CONFIG_HOME", "E2E_CLAUDE_OAUTH_TOKEN"} {
			if v, ok := os.LookupEnv(k); ok {
				t.Errorf("%s=%q survived", k, v)
			}
		}
		if os.Getenv("HOME") != home || os.Getenv("PATH") == "" {
			t.Errorf("HOME=%q (want %q), PATH=%q", os.Getenv("HOME"), home, os.Getenv("PATH"))
		}
	})
	if os.Getenv("KIRO_HOME") != "secret" {
		t.Error("environment was not restored")
	}
}

func TestStartRetriesOnlyAnExitedCLI(t *testing.T) {
	re := regexp.MustCompile(`https://x\.example/\S+`)
	wait := func(f *agents.Flow) string { return f.WaitFor(re, 2*time.Second) }
	run := func(script string, calls *int) func() (*agents.Flow, error) {
		return func() (*agents.Flow, error) {
			*calls++
			return agents.StartFlow(exec.Command("sh", "-c", script))
		}
	}
	// Exits without output twice, then prints: retried into success.
	n := 0
	script := `if [ -e "$MARK" ]; then echo https://x.example/ok; sleep 5; else :>"$MARK"; fi`
	t.Setenv("MARK", t.TempDir()+"/m")
	if _, got := Start(t, run(script, &n), wait); got == "" || n != 2 {
		t.Errorf("exited-then-ok: got %q after %d launches, want a URL after 2", got, n)
	}
	// Keeps running with a different URL: never retried.
	n = 0
	if _, got := Start(t, run(`echo https://other.example/y; sleep 5`, &n), wait); got != "" || n != 1 {
		t.Errorf("changed output: got %q after %d launches, want none after 1", got, n)
	}
}

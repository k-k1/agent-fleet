package tmuxx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The launch pane is found by the exit recorder in its start command, across windows, not by
// which pane is active: a split must not make the real claude's hooks look nested.
func TestLaunchPaneIn(t *testing.T) {
	launch := `"export PATH=x; claude --resume abc; __af_ec=$?; ` + session.ExitRecorder("s1") + ` \"$__af_ec\""`
	cases := []struct {
		name string
		out  string
		want int
		ok   bool
	}{
		{"single pane", "100 0 " + launch + "\n", 100, true},
		{"split off and active", "200 0 bash\n100 0 " + launch + "\n300 0 claude\n", 100, true},
		{"another session's recorder", "100 0 " + strings.Replace(launch, "'s1'", "'s12'", 1) + "\n", 0, false},
		{"two launch panes", "100 0 " + launch + "\n101 0 " + launch + "\n", 0, false},
		{"no launch pane", "200 0 bash\n", 0, false},
		// remain-on-exit keeps a dead pane listed with its old pid.
		{"launch pane dead", "100 1 " + launch + "\n", 0, false},
		{"dead launch pane beside a live one", "100 1 " + launch + "\n101 0 " + launch + "\n", 0, false},
		{"a split alive, the launch pane dead", "200 0 bash\n100 1 " + launch + "\n", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := LaunchPaneIn(c.out, "s1")
			if got != c.want || ok != c.ok {
				t.Fatalf("LaunchPaneIn = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// The real lookup, against a tmux that answers like a server with remain-on-exit: the format
// asks for pane_dead, and a dead launch pane is no answer.
func TestLaunchPanePIDAsksForPaneDead(t *testing.T) {
	launch := `"claude; __af_ec=$?; ` + session.ExitRecorder("s1") + ` \"$__af_ec\""`
	for _, c := range []struct {
		dead string
		want int
		ok   bool
	}{{"0", 4242, true}, {"1", 0, false}} {
		bin := t.TempDir()
		script := "#!/bin/sh\ncase \"$*\" in *'#{pane_pid} #{pane_dead} #{pane_start_command}'*) printf '%s\\n' '4242 " + c.dead + " " +
			strings.ReplaceAll(launch, "'", `'"'"'`) + "' ;; *) exit 1 ;; esac\n"
		if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		t.Setenv("AF_TMUX_SOCKET", "")
		if got, ok := LaunchPanePID("s1"); got != c.want || ok != c.ok {
			t.Fatalf("pane_dead=%s: LaunchPanePID = (%d, %v), want (%d, %v)", c.dead, got, ok, c.want, c.ok)
		}
	}
}

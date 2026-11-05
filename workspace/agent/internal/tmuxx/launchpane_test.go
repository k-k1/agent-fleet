package tmuxx

import (
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
		{"single pane", "100 " + launch + "\n", 100, true},
		{"split off and active", "200 bash\n100 " + launch + "\n300 claude\n", 100, true},
		{"another session's recorder", "100 " + strings.Replace(launch, "'s1'", "'s12'", 1) + "\n", 0, false},
		{"two launch panes", "100 " + launch + "\n101 " + launch + "\n", 0, false},
		{"no launch pane", "200 bash\n", 0, false},
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

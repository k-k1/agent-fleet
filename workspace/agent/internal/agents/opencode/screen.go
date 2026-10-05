package opencode

// opencode's permission prompt, read off the pane. It exists only in the TUI process while it
// waits (nothing in the store), so the pane is the only place to see it. `--auto` answers every
// permission itself (measured 1.18.33: an external_directory read went through with --auto even
// under an explicit "ask"), so the prompt is up only on a pane launched without it
// (AGENT_OPENCODE_FLAGS) or after the user turned auto off in the TUI. There a pasted line is
// dropped and the Enter confirms the highlighted "Allow once" (measured 1.18.33):
//
//	△ Permission required
//	  ← Access external directory /home/dev/work/outside
//	Patterns
//	- /home/dev/work/outside/*
//	 Allow once   Allow always   Reject             ctrl+f fullscreen  ⇆ select  enter confirm
//
// "Allow always" opens a second step, "△ Always allow" with Confirm / Cancel, where the Enter
// confirms the always. The captures are in testdata/.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// permissionSteps are the prompt's steps by their title and their row of options.
var permissionSteps = []struct{ title, row string }{
	{"Permission required", "Allow once Allow always Reject"},
	{"Always allow", "Confirm Cancel"},
}

// permissionFooter is the key hint only the live prompt draws after its options.
const permissionFooter = "enter confirm"

// permissionTailMax bounds what may follow the options: the prompt's key hints and nothing
// else, counted on the flattened text so it does not depend on the pane's width.
const permissionTailMax = 120

// promptMarks are drawn under opencode's composer. The prompt takes the composer's place, so
// after its options none of them may appear; that tells it from the same words in the transcript.
var promptMarks = []string{"ctrl+p commands", "esc interrupt"}

// panePermission reports whether one captured frame shows the permission prompt.
func panePermission(s string) bool {
	flat := strings.Join(strings.Fields(strings.NewReplacer("┃", " ", "│", " ", "╹", " ").Replace(s)), " ")
	for _, st := range permissionSteps {
		i := strings.LastIndex(flat, st.title)
		if i < 0 {
			continue
		}
		rest := flat[i+len(st.title):]
		j := strings.Index(rest, st.row)
		if j < 0 {
			continue
		}
		tail := rest[j+len(st.row):]
		if len(tail) > permissionTailMax || !strings.Contains(tail, permissionFooter) {
			continue
		}
		marked := false
		for _, m := range promptMarks {
			marked = marked || strings.Contains(tail, m)
		}
		if !marked {
			return true
		}
	}
	return false
}

// terminalPermission reads the Terminal pane of m for the permission prompt.
func terminalPermission(m session.Meta) bool {
	return panePermission(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

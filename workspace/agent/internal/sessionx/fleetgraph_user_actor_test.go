package sessionx

// ADR 0096 decision 4's ActorId "user" (S-BE review finding B2): a person typing directly
// into the Console composer, with no operator/schedule/peer/bridge/spawn tag at all, is the
// single most common way a session gets steered — and it produced no fleet-graph arrow at
// all before this fix (fleetGraphActorFor's default case dropped it, and the create path's
// noteCreateOrigin had no branch for it either).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// activityEventsFrom returns every line in file whose ev/from match, for asserting on
// InstructEvent/ReportEvent/PeerEvent rows written to an activity-<day>.jsonl file.
func activityEventsFrom(t *testing.T, file, ev, from string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ln := range strings.Split(string(b), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) != nil {
			continue
		}
		if m["ev"] == ev && m["from"] == from {
			out = append(out, m)
		}
	}
	return out
}

// todayActivityPath finds the activity-<day>.jsonl file this test wrote, or "" if nothing
// wrote to the fleet graph at all (a valid outcome for a "must NOT record" assertion) —
// activityEventsFrom treats a missing path the same as an empty file.
func todayActivityPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(paths.AgentStateDir(), "fleet-graph")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "activity-") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func fakeTmuxAlwaysAlive(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%3\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
}

// TestPlainTypedInput_RecordsUserInstruct: a bare {prompt} with no report_to, no source,
// no peer_from — the ordinary case of a person typing in the Console.
func TestPlainTypedInput_RecordsUserInstruct(t *testing.T) {
	fakeTmuxAlwaysAlive(t)
	const name = "slot_plain_user"
	const prompt = "続けて実装して"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	if rec := postInput(t, name, `{"prompt":"`+prompt+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got := activityEventsFrom(t, todayActivityPath(t), "instruct", "user")
	if len(got) != 1 {
		t.Fatalf("instruct(from=user) events = %d, want 1: %v", len(got), got)
	}
	if got[0]["to"] != name {
		t.Errorf("to = %v, want %q", got[0]["to"], name)
	}
}

// TestNoteCreateOrigin_ConsoleLaunchRecordsUserInstruct: the ordinary Console "start work"
// launch — an initial_prompt with no report_to, no schedule tag, not a spawn — used to
// record NOTHING for the fleet graph (noteCreateOrigin's switch had no default case), even
// though it is the single most common way a session's first turn gets steered. Driven
// directly against noteCreateOrigin (rather than the full HTTP create flow, which needs a
// real or faked launch pipeline) — the same level TestNoteCreateOriginBadges already tests
// this function at.
func TestNoteCreateOrigin_ConsoleLaunchRecordsUserInstruct(t *testing.T) {
	fakeTmuxAlwaysAlive(t)
	const name = "slot_console_launch"
	req := CreateReq{InitialPrompt: "最初のタスク"}

	noteCreateOrigin(name, &req, "", session.OriginUser)

	got := activityEventsFrom(t, todayActivityPath(t), "instruct", "user")
	if len(got) != 1 {
		t.Fatalf("instruct(from=user) events = %d, want 1: %v", len(got), got)
	}
	if got[0]["to"] != name {
		t.Errorf("to = %v, want %q", got[0]["to"], name)
	}
}

// TestNoteCreateOrigin_OperatorWithoutReportToStaysUnattributed: an MCP/operator call that
// simply chose not to set report_to must NOT be drawn as a person (origin=operator, not
// user/handoff) — the default branch's origin check exists precisely to keep this apart
// from the Console-launch case above.
func TestNoteCreateOrigin_OperatorWithoutReportToStaysUnattributed(t *testing.T) {
	fakeTmuxAlwaysAlive(t)
	const name = "slot_operator_no_report"
	req := CreateReq{InitialPrompt: "task", Origin: session.OriginOperator}

	noteCreateOrigin(name, &req, "", session.OriginOperator)

	if got := activityEventsFrom(t, todayActivityPath(t), "instruct", "user"); len(got) != 0 {
		t.Fatalf("instruct(from=user) events = %d, want 0 (origin=operator is not a person): %v", len(got), got)
	}
}

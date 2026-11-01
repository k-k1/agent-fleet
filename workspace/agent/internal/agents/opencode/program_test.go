package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildProgramV2FreshSession(t *testing.T) {
	useMajor(t, 2)
	// Nothing chosen: a bare private-server TUI, no config, none of the v1-only flags.
	got := buildProgram("", "", "", false)
	if got != "opencode --standalone --auto" {
		t.Fatalf("plain v2 launch = %q", got)
	}
	// Model and mode of a fresh session ride the inline config; the flags do not exist on the v2 TUI.
	got = buildProgram("stub/stub-2", "plan", "", false)
	for _, bad := range []string{"--model", "--agent", "--fork"} {
		if strings.Contains(got, bad) {
			t.Fatalf("v2 TUI has no %s: %q", bad, got)
		}
	}
	want := `OPENCODE_CONFIG_CONTENT='{"default_agent":"plan","model":"stub/stub-2"}' opencode --standalone --auto`
	if got != want {
		t.Fatalf("fresh v2 launch:\n got %q\nwant %q", got, want)
	}
	if got := buildProgram("", "normal", "", false); !strings.Contains(got, `"default_agent":"build"`) {
		t.Fatalf("normal mode must select the build agent: %q", got)
	}
}

func TestBuildProgramV2Resume(t *testing.T) {
	useMajor(t, 2)
	// Nothing to change: attach straight to the session.
	if got := buildProgram("", "", "ses_abc", false); got != "opencode --standalone --auto --session 'ses_abc'" {
		t.Fatalf("plain resume = %q", got)
	}
	// An existing session ignores the config, so model and agent are set through the API first.
	got := buildProgram("stub/stub-2#high", "plan", "ses_abc", false)
	for _, want := range []string{
		`opencode api --standalone POST /api/session/"$AF_OC_SES"/model -d '{"model":{"id":"stub-2","providerID":"stub","variant":"high"}}'`,
		`opencode api --standalone POST /api/session/"$AF_OC_SES"/agent -d '{"agent":"plan"}'`,
		`AF_OC_SES='ses_abc'; if `,
		`opencode --standalone --auto --session "$AF_OC_SES"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("resume line lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "OPENCODE_CONFIG_CONTENT") || strings.Contains(got, "--fork") {
		t.Fatalf("resume must not use inline config or --fork: %q", got)
	}
}

func TestBuildProgramV2Fork(t *testing.T) {
	useMajor(t, 2)
	got := buildProgram("", "normal", "ses_src", true)
	if !strings.Contains(got, "POST /api/session/'ses_src'/fork") {
		t.Fatalf("fork must go through the API: %q", got)
	}
	if strings.Contains(got, "--fork") {
		t.Fatalf("the v2 TUI has no --fork: %q", got)
	}
	// The copy is what the TUI resumes, and a failed fork must not fall through to the source.
	if !strings.Contains(got, `[ -z "$AF_OC_SES" ]`) || !strings.Contains(got, `--session "$AF_OC_SES"`) {
		t.Fatalf("fork line must guard and resume the copy: %q", got)
	}
	if strings.Contains(got, "--session 'ses_src'") {
		t.Fatalf("fork line must never attach to the source session: %q", got)
	}
	if !strings.Contains(got, `/agent -d '{"agent":"build"}'`) {
		t.Fatalf("fork line must still apply the mode: %q", got)
	}
}

// runLaunch executes a launch line with a fake `opencode` first on PATH and returns the
// calls it saw, the pane output and the exit status the exit recorder would read. The fake
// answers `api … /fork` with a new session id and fails the call named in fail
// ("fork", "model", "agent"); the TUI call exits with tuiExit.
func runLaunch(t *testing.T, program, fail string, tuiExit int) (calls []string, out string, status int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fake := `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1" in
api)
  case "$*" in
  */fork*) [ "$FAKE_FAIL" = fork ] && { echo "HTTP 404 Not Found"; exit 1; }
           echo '{"data":{"id":"ses_copy1","fork":{"sessionID":"ses_src"}}}'; exit 0 ;;
  */model*) [ "$FAKE_FAIL" = model ] && { echo "model boom"; exit 17; }; exit 0 ;;
  */agent*) [ "$FAKE_FAIL" = agent ] && { echo "agent boom"; exit 17; }; exit 0 ;;
  esac ;;
*) echo TUI >> "$FAKE_LOG"; exit "$FAKE_TUI_EXIT" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// "sleep 5" before a failed launch is for a human; the test must not wait for it.
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", program+"; echo STATUS=$?")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FAKE_LOG="+log, "FAKE_FAIL="+fail, "FAKE_TUI_EXIT="+strconv.Itoa(tuiExit))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v\n%s", err, b)
	}
	out = string(b)
	i := strings.LastIndex(out, "STATUS=")
	status, _ = strconv.Atoi(strings.TrimSpace(out[i+len("STATUS="):]))
	if raw, err := os.ReadFile(log); err == nil {
		calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	return calls, out, status
}

func sawTUI(calls []string) bool {
	for _, c := range calls {
		if c == "TUI" {
			return true
		}
	}
	return false
}

// A switch that fails must stop the launch: a plan agent that stayed on build lets the
// session edit when the member chose plan.
func TestBuildProgramV2FailedSwitchDoesNotStartTUI(t *testing.T) {
	useMajor(t, 2)
	for _, tc := range []struct {
		name, program, fail, show string
	}{
		{"resume model", buildProgram("stub/stub-2", "plan", "ses_abc", false), "model", "model boom"},
		{"resume agent", buildProgram("stub/stub-2", "plan", "ses_abc", false), "agent", "agent boom"},
		{"fork then model", buildProgram("stub/stub-2", "plan", "ses_src", true), "model", "model boom"},
		{"fork then agent", buildProgram("", "plan", "ses_src", true), "agent", "agent boom"},
		{"fork itself", buildProgram("stub/stub-2", "plan", "ses_src", true), "fork", "HTTP 404 Not Found"},
	} {
		calls, out, status := runLaunch(t, tc.program, tc.fail, 0)
		if sawTUI(calls) {
			t.Errorf("%s: the TUI started after a failed step\n%s", tc.name, out)
		}
		if status == 0 {
			t.Errorf("%s: exit status 0 would be recorded as a clean exit\n%s", tc.name, out)
		}
		if !strings.Contains(out, tc.show) {
			t.Errorf("%s: the reason is not shown in the pane:\n%s", tc.name, out)
		}
	}
}

func TestBuildProgramV2SuccessLaunchesTheTUIOnTheRightSession(t *testing.T) {
	useMajor(t, 2)
	// Resume: both switches, then the TUI on the same session; its exit status is the line's.
	calls, out, status := runLaunch(t, buildProgram("stub/stub-2", "plan", "ses_abc", false), "", 3)
	if !sawTUI(calls) || status != 3 {
		t.Fatalf("resume: calls %q status %d\n%s", calls, status, out)
	}
	last := calls[len(calls)-2]
	if !strings.Contains(last, "--session ses_abc") {
		t.Fatalf("resume attached to %q", last)
	}
	// Fork: the TUI attaches to the copy, never the source.
	calls, out, status = runLaunch(t, buildProgram("stub/stub-2", "plan", "ses_src", true), "", 0)
	if !sawTUI(calls) || status != 0 {
		t.Fatalf("fork: calls %q status %d\n%s", calls, status, out)
	}
	last = calls[len(calls)-2]
	if !strings.Contains(last, "--session ses_copy1") || strings.Contains(last, "ses_src") {
		t.Fatalf("fork attached to %q", last)
	}
}
